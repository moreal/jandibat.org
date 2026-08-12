package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRequestMagicLinkQueuesSecretFreeIntentWithoutCallingMailer(t *testing.T) {
	store := NewMemoryStore()
	issuer := &recordingDeliveryIssuer{}
	mailer := NewMemoryMailer()
	mailer.SetError(errors.New("SMTP must not run in request path"))
	service, err := NewService(Dependencies{Repository: store, Deliveries: issuer, Mailer: mailer}, Config{
		Random: &sequenceReader{}, Now: func() time.Time { return time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequestMagicLink(context.Background(), " PERSON@Example.COM ", "https://app.example/#auth"); err != nil {
		t.Fatal(err)
	}
	if len(mailer.Messages()) != 0 {
		t.Fatal("request path invoked mailer")
	}
	if issuer.intent.RecipientEmail != "person@example.com" || issuer.intent.RedirectURI != "https://app.example/#auth" ||
		issuer.intent.Purpose != MagicLinkPurposeSignIn || issuer.intent.Status != MagicLinkDeliveryPending {
		t.Fatalf("secret-free intent = %#v", issuer.intent)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.magicLinks) != 0 {
		t.Fatalf("API request minted token rows: %#v", store.magicLinks)
	}
}

type recordingDeliveryIssuer struct{ intent MagicLinkDelivery }

func (issuer *recordingDeliveryIssuer) SaveMagicLinkDeliveryIntent(_ context.Context, intent MagicLinkDelivery) error {
	issuer.intent = intent
	return nil
}

func TestMagicLinkDeliveryWorkerMintsFreshTokenPerFailedAttempt(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	repository := newMemoryDeliveryRepository(MagicLinkDelivery{
		ID: "delivery-1", RecipientEmail: "person@example.com", RedirectURI: "https://app.example/#auth",
		Purpose: MagicLinkPurposeSignIn, Status: MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	})
	mailer := NewMemoryMailer()
	mailer.SetError(errors.New("recipient rejected"))
	worker, err := NewMagicLinkDeliveryWorker(repository, mailer, nil, MagicLinkDeliveryWorkerConfig{
		PollInterval: time.Minute, Lease: time.Minute, CallTimeout: time.Second, TokenTTL: 15 * time.Minute,
		BatchSize: 1, MaxAttempts: 5, Random: &sequenceReader{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Retry != 1 || len(repository.activated) != 1 {
		t.Fatalf("first run = %#v, %v activated=%d", result, err, len(repository.activated))
	}
	first := repository.activated[0]
	now = now.Add(2 * time.Minute)
	result, err = worker.RunOnce(context.Background())
	if err != nil || result.Retry != 1 || len(repository.activated) != 2 {
		t.Fatalf("second run = %#v, %v activated=%d", result, err, len(repository.activated))
	}
	second := repository.activated[1]
	if first.ID == second.ID || digestEqual(first.TokenHash, second.TokenHash) {
		t.Fatalf("retry reused token: first=%#v second=%#v", first, second)
	}
	if len(repository.invalidated) != 2 || !digestEqual(repository.invalidated[0], first.TokenHash) || !digestEqual(repository.invalidated[1], second.TokenHash) {
		t.Fatalf("failed attempts were not invalidated: %#v", repository.invalidated)
	}
}

func TestMagicLinkDeliveryWorkerTreatsSupersededClaimAsBenign(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	for _, sendErr := range []error{nil, errors.New("SMTP rejected recipient")} {
		repository := newMemoryDeliveryRepository(MagicLinkDelivery{
			ID: "delivery-1", RecipientEmail: "person@example.com", Purpose: MagicLinkPurposeSignIn,
			Status: MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		})
		repository.finishErr = ErrConflict
		mailer := NewMemoryMailer()
		mailer.SetError(sendErr)
		worker, err := NewMagicLinkDeliveryWorker(repository, mailer, nil, MagicLinkDeliveryWorkerConfig{
			PollInterval: time.Minute, Lease: time.Minute, CallTimeout: time.Second, TokenTTL: 15 * time.Minute,
			BatchSize: 1, MaxAttempts: 5, Random: &sequenceReader{}, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatalf("send error %v stopped worker: %v", sendErr, err)
		}
	}
}

type memoryDeliveryRepository struct {
	mu          sync.Mutex
	job         MagicLinkDelivery
	activated   []MagicLink
	invalidated []Digest
	claimSeq    int
	finishErr   error
	active      bool
}

func newMemoryDeliveryRepository(job MagicLinkDelivery) *memoryDeliveryRepository {
	return &memoryDeliveryRepository{job: job}
}

func (repository *memoryDeliveryRepository) ClaimMagicLinkDeliveries(_ context.Context, now time.Time, lease time.Duration, _ int, maxAttempts int) ([]MagicLinkDelivery, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.job.Status != MagicLinkDeliveryPending || repository.job.AvailableAt.After(now) || repository.job.Attempts >= maxAttempts {
		return nil, nil
	}
	repository.claimSeq++
	repository.job.Status = MagicLinkDeliveryProcessing
	repository.job.Attempts++
	repository.job.ClaimToken = "claim-" + string(rune('0'+repository.claimSeq))
	until := now.Add(lease)
	repository.job.LeaseUntil = &until
	return []MagicLinkDelivery{repository.job}, nil
}

func (repository *memoryDeliveryRepository) ActivateMagicLinkDelivery(_ context.Context, id, claim string, link MagicLink) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.job.ID != id || repository.job.ClaimToken != claim || repository.job.Status != MagicLinkDeliveryProcessing {
		return ErrConflict
	}
	if repository.active && len(repository.activated) > 0 {
		repository.invalidated = append(repository.invalidated, repository.activated[len(repository.activated)-1].TokenHash)
	}
	repository.active = true
	repository.activated = append(repository.activated, link)
	return nil
}

func (repository *memoryDeliveryRepository) CompleteMagicLinkDelivery(_ context.Context, id, claim string, _ time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.job.ID != id || repository.job.ClaimToken != claim {
		return ErrConflict
	}
	if repository.finishErr != nil {
		return repository.finishErr
	}
	repository.job.Status = MagicLinkDeliverySent
	repository.job.ClaimToken = ""
	repository.job.LeaseUntil = nil
	return nil
}

func (repository *memoryDeliveryRepository) RetryMagicLinkDelivery(_ context.Context, id, claim string, _ time.Time, next time.Time, maxAttempts int) (MagicLinkDeliveryStatus, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.job.ID != id || repository.job.ClaimToken != claim {
		return "", ErrConflict
	}
	if repository.finishErr != nil {
		return "", repository.finishErr
	}
	if len(repository.activated) != 0 {
		repository.invalidated = append(repository.invalidated, repository.activated[len(repository.activated)-1].TokenHash)
	}
	repository.active = false
	repository.job.ClaimToken = ""
	repository.job.LeaseUntil = nil
	if repository.job.Attempts >= maxAttempts {
		repository.job.Status = MagicLinkDeliveryDead
		return MagicLinkDeliveryDead, nil
	}
	repository.job.Status = MagicLinkDeliveryPending
	repository.job.AvailableAt = next
	return MagicLinkDeliveryPending, nil
}
