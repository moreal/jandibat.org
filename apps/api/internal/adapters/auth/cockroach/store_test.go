package cockroach

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestNewRejectsNilDatabase(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) || store != nil {
		t.Fatalf("New(nil) = (%v, %v), want (nil, ErrNilDB)", store, err)
	}
}

func TestMagicLinkDeliveryAttemptLimitsAreCheckedBeforeDatabase(t *testing.T) {
	store := &Store{}
	now := time.Now()
	for _, attempts := range []int{0, 6} {
		if _, err := store.ClaimMagicLinkDeliveries(context.Background(), now, time.Minute, 1, attempts); !errors.Is(err, coreauth.ErrInvalidInput) {
			t.Fatalf("ClaimMagicLinkDeliveries(maxAttempts=%d) error = %v", attempts, err)
		}
		if _, err := store.RetryMagicLinkDelivery(context.Background(), "id", "claim", now, now.Add(time.Minute), attempts); !errors.Is(err, coreauth.ErrInvalidInput) {
			t.Fatalf("RetryMagicLinkDelivery(maxAttempts=%d) error = %v", attempts, err)
		}
	}
	for _, attempts := range []int{1, 5} {
		if _, err := store.ClaimMagicLinkDeliveries(context.Background(), now, time.Minute, 1, attempts); !errors.Is(err, ErrNilDB) {
			t.Fatalf("ClaimMagicLinkDeliveries(maxAttempts=%d) error = %v, want nil database", attempts, err)
		}
		if _, err := store.RetryMagicLinkDelivery(context.Background(), "id", "claim", now, now.Add(time.Minute), attempts); !errors.Is(err, ErrNilDB) {
			t.Fatalf("RetryMagicLinkDelivery(maxAttempts=%d) error = %v, want nil database", attempts, err)
		}
	}
}

func TestEncodeCeremonyHashesChallengeAndKeepsVerifierPayload(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	ceremony := coreauth.PasskeyCeremony{
		ID: "b73db5cc-c6bc-4298-9361-6dd00b876075", Kind: coreauth.CeremonyAuthentication,
		Challenge: "browser-challenge", VerifierSession: json.RawMessage(`{"opaque":true}`),
		CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}

	payload, gotHash, err := encodeCeremony(ceremony)
	if err != nil {
		t.Fatalf("encodeCeremony() error = %v", err)
	}
	wantHash := sha256.Sum256([]byte(ceremony.Challenge))
	if gotHash != wantHash {
		t.Fatalf("challenge hash = %x, want %x", gotHash, wantHash)
	}
	if !strings.Contains(string(payload), `"challenge":"browser-challenge"`) ||
		!strings.Contains(string(payload), `"verifier_session":{"opaque":true}`) {
		t.Fatalf("payload = %s", payload)
	}
	if kind := databaseCeremonyKind(ceremony.Kind); kind != "passkey_authentication" {
		t.Fatalf("database ceremony kind = %v", kind)
	}
}

func TestCeremonyFromGeneratedRestoresPayload(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	ceremony, err := ceremonyFromGenerated(generated.ConsumeCeremonyRow{
		Id: "cf0e620a-9568-4771-a0c9-54d8833f0950", Kind: "passkey_registration", UserId: "user-1",
		Payload:   json.RawMessage(`{"challenge":"browser-challenge","verifier_session":{"opaque":true}}`),
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), ConsumedAt: &now,
	})
	if err != nil {
		t.Fatalf("ceremonyFromGenerated() error = %v", err)
	}
	if ceremony.Challenge != "browser-challenge" || ceremony.UserID != "user-1" || ceremony.ConsumedAt == nil {
		t.Fatalf("ceremonyFromGenerated() = %#v", ceremony)
	}
}

func TestPersistenceErrorMapsCockroachConstraintCodes(t *testing.T) {
	if err := persistenceError(&pgconn.PgError{Code: "23505"}); !errors.Is(err, coreauth.ErrConflict) {
		t.Fatalf("unique violation = %v", err)
	}
	if err := persistenceError(&pgconn.PgError{Code: "23503"}); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("foreign-key violation = %v", err)
	}
}
