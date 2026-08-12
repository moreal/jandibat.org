package integrations

import (
	"context"
	"errors"
	"testing"
	"time"
)

type revocationStoreFake struct {
	jobs       []OAuthTokenRevocationJob
	completed  []string
	retried    []string
	retryAt    time.Time
	dead       []string
	terminalAt time.Time
	deadErr    error
	claimCalls int
}

func (store *revocationStoreFake) ClaimOAuthTokenRevocations(context.Context, time.Time, time.Time, int) ([]OAuthTokenRevocationJob, error) {
	store.claimCalls++
	return append([]OAuthTokenRevocationJob(nil), store.jobs...), nil
}
func (store *revocationStoreFake) CompleteOAuthTokenRevocation(_ context.Context, id, _ string) error {
	store.completed = append(store.completed, id)
	return nil
}
func (store *revocationStoreFake) RetryOAuthTokenRevocation(_ context.Context, id, _ string, at time.Time) error {
	store.retried = append(store.retried, id)
	store.retryAt = at
	return nil
}
func (store *revocationStoreFake) DeadLetterOAuthTokenRevocation(_ context.Context, id, _ string, at time.Time) error {
	if store.deadErr != nil {
		return store.deadErr
	}
	store.dead = append(store.dead, id)
	store.terminalAt = at
	return nil
}

type revocationCipherFake struct {
	plaintext []byte
	err       error
}

func (cipher revocationCipherFake) Encrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("unused")
}
func (cipher revocationCipherFake) Decrypt(context.Context, []byte) ([]byte, error) {
	return append([]byte(nil), cipher.plaintext...), cipher.err
}

type revocationProviderFake struct {
	calls    int
	provider string
	token    []byte
	err      error
}

type revocationJitterFake float64

func (jitter revocationJitterFake) Float64() float64 { return float64(jitter) }

func (provider *revocationProviderFake) RevokeOAuthToken(_ context.Context, providerID string, token []byte) error {
	provider.calls++
	provider.provider = providerID
	provider.token = append([]byte(nil), token...)
	return provider.err
}

func TestOAuthTokenRevocationWorkerCompletesClaimedJob(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store := &revocationStoreFake{jobs: []OAuthTokenRevocationJob{{ID: "job-1", ProviderID: "github", TokenCiphertext: []byte("encrypted"), ClaimToken: "claim-1", Attempts: 1}}}
	provider := &revocationProviderFake{}
	worker, err := NewOAuthTokenRevocationWorker(store, revocationCipherFake{plaintext: []byte("oauth-token")}, provider, &fixedClock{now: now}, OAuthTokenRevocationWorkerConfig{
		PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: time.Second, BatchSize: 10, Jitter: revocationJitterFake(0.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || provider.provider != "github" || string(provider.token) != "oauth-token" {
		t.Fatalf("provider revoke = calls %d provider %q token %q", provider.calls, provider.provider, provider.token)
	}
	if len(store.completed) != 1 || store.completed[0] != "job-1" || len(store.retried) != 0 {
		t.Fatalf("job disposition = completed %v retried %v", store.completed, store.retried)
	}
}

func TestOAuthTokenRevocationWorkerDurablyRetriesProviderOrDecryptFailure(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		cipher      revocationCipherFake
		providerErr error
		wantCalls   int
	}{
		{name: "provider", cipher: revocationCipherFake{plaintext: []byte("token")}, providerErr: errors.New("sanitized provider failure"), wantCalls: 1},
		{name: "decrypt", cipher: revocationCipherFake{err: errors.New("corrupt envelope")}, wantCalls: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &revocationStoreFake{jobs: []OAuthTokenRevocationJob{{ID: "job-1", ProviderID: "gitlab", TokenCiphertext: []byte("encrypted"), ClaimToken: "claim-1", Attempts: 3}}}
			provider := &revocationProviderFake{err: test.providerErr}
			worker, err := NewOAuthTokenRevocationWorker(store, test.cipher, provider, &fixedClock{now: now}, OAuthTokenRevocationWorkerConfig{
				PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: time.Second, BatchSize: 10, Jitter: revocationJitterFake(0.5),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(context.Background()); err != nil {
				t.Fatalf("durably scheduled provider failure escaped RunOnce: %v", err)
			}
			if provider.calls != test.wantCalls || len(store.completed) != 0 || len(store.retried) != 1 || store.retryAt != now.Add(4*time.Minute) {
				t.Fatalf("retry disposition = calls %d completed %v retried %v at %v", provider.calls, store.completed, store.retried, store.retryAt)
			}
		})
	}
}

func TestOAuthTokenRevocationWorkerDeadLettersAtMaximumAttempts(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store := &revocationStoreFake{jobs: []OAuthTokenRevocationJob{{ID: "job-terminal", ProviderID: "github", TokenCiphertext: []byte("encrypted"), ClaimToken: "claim-terminal", Attempts: 4}}}
	provider := &revocationProviderFake{err: errors.New("sanitized provider failure")}
	worker, err := NewOAuthTokenRevocationWorker(store, revocationCipherFake{plaintext: []byte("token")}, provider, &fixedClock{now: now}, OAuthTokenRevocationWorkerConfig{
		PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: time.Second, BatchSize: 10, MaxAttempts: 4, Jitter: revocationJitterFake(0.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = worker.RunOnce(context.Background())
	if !errors.Is(err, ErrOAuthTokenRevocationDeadLettered) {
		t.Fatalf("RunOnce error = %v", err)
	}
	if provider.calls != 1 || len(store.dead) != 1 || store.dead[0] != "job-terminal" || store.terminalAt != now {
		t.Fatalf("terminal disposition = calls %d dead %v at %v", provider.calls, store.dead, store.terminalAt)
	}
	if len(store.completed) != 0 || len(store.retried) != 0 {
		t.Fatalf("terminal job completed %v or retried %v", store.completed, store.retried)
	}
}

func TestOAuthTokenRevocationWorkerReclaimedExhaustedJobDoesNotCallProvider(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store := &revocationStoreFake{jobs: []OAuthTokenRevocationJob{{ID: "job-reclaimed", ProviderID: "gitlab", TokenCiphertext: []byte("encrypted"), ClaimToken: "claim-reclaimed", Attempts: 5}}}
	provider := &revocationProviderFake{}
	worker, err := NewOAuthTokenRevocationWorker(store, revocationCipherFake{plaintext: []byte("token")}, provider, &fixedClock{now: now}, OAuthTokenRevocationWorkerConfig{
		PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: time.Second, BatchSize: 10, MaxAttempts: 4, Jitter: revocationJitterFake(0.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); !errors.Is(err, ErrOAuthTokenRevocationDeadLettered) {
		t.Fatalf("RunOnce error = %v", err)
	}
	if provider.calls != 0 || len(store.dead) != 1 {
		t.Fatalf("reclaimed disposition = provider calls %d dead %v", provider.calls, store.dead)
	}
}

func TestOAuthTokenRevocationWorkerReportsDeadLetterPersistenceFailure(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	persistErr := errors.New("dead-letter unavailable")
	store := &revocationStoreFake{
		jobs:    []OAuthTokenRevocationJob{{ID: "job-terminal", ProviderID: "github", TokenCiphertext: []byte("encrypted"), ClaimToken: "claim-terminal", Attempts: 1}},
		deadErr: persistErr,
	}
	worker, err := NewOAuthTokenRevocationWorker(store, revocationCipherFake{err: errors.New("corrupt envelope")}, &revocationProviderFake{}, &fixedClock{now: now}, OAuthTokenRevocationWorkerConfig{
		PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: time.Second, BatchSize: 10, MaxAttempts: 1, Jitter: revocationJitterFake(0.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); !errors.Is(err, persistErr) || errors.Is(err, ErrOAuthTokenRevocationDeadLettered) {
		t.Fatalf("RunOnce error = %v", err)
	}
}

func TestOAuthTokenRevocationRetryDelayIsJitteredAndBounded(t *testing.T) {
	if got := revocationRetryDelay(1, revocationJitterFake(-1)); got != 48*time.Second {
		t.Fatalf("minimum first delay = %s", got)
	}
	if got := revocationRetryDelay(1, revocationJitterFake(2)); got != 72*time.Second {
		t.Fatalf("maximum first delay = %s", got)
	}
	if got := revocationRetryDelay(1000, revocationJitterFake(1)); got != 24*time.Hour {
		t.Fatalf("capped delay = %s", got)
	}
}

func TestOAuthTokenRevocationWorkerValidatesAttemptBoundAndTerminalStore(t *testing.T) {
	valid := OAuthTokenRevocationWorkerConfig{PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: time.Second, BatchSize: 10}
	for _, attempts := range []int{-1, maxOAuthTokenRevocationMaxAttempts + 1} {
		config := valid
		config.MaxAttempts = attempts
		if _, err := NewOAuthTokenRevocationWorker(&revocationStoreFake{}, revocationCipherFake{}, &revocationProviderFake{}, nil, config); !errors.Is(err, ErrInvalidRevocationConfig) {
			t.Fatalf("MaxAttempts %d error = %v", attempts, err)
		}
	}
	storeWithoutDeadLetter := struct{ OAuthTokenRevocationStore }{}
	if _, err := NewOAuthTokenRevocationWorker(storeWithoutDeadLetter, revocationCipherFake{}, &revocationProviderFake{}, nil, valid); !errors.Is(err, ErrInvalidRevocationConfig) {
		t.Fatalf("store without dead-letter support error = %v", err)
	}
}
