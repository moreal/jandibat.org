package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestSaveMagicLinkDeliveryIntentIsAtomicAndSecretFree(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	err := store.SaveMagicLinkDeliveryIntent(context.Background(), coreauth.MagicLinkDelivery{
		ID: "00f2ecf8-4f85-4bfd-8fbf-a006c4aedb11", RecipientEmail: "person@example.com",
		RedirectURI: "https://app.example/#auth", Purpose: coreauth.MagicLinkPurposeSignIn,
		Status: coreauth.MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 6 || !strings.Contains(calls[1].Query, "UPDATE magic_link_tokens") ||
		!strings.Contains(calls[2].Query, "status = 'sent'") ||
		!strings.Contains(calls[3].Query, "status = 'superseded'") ||
		!strings.Contains(calls[4].Query, "INSERT INTO magic_link_mail_outbox") ||
		strings.Contains(strings.ToLower(calls[4].Query), "cipher") || strings.Contains(strings.ToLower(calls[4].Query), "token_hash") {
		t.Fatalf("intent transaction = %#v", calls)
	}
}

func TestActivateMagicLinkDeliveryFencesClaimAndStoresOnlyDigest(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("raw-token-never-an-argument"))
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"email", "purpose"}, Rows: [][]driver.Value{{"person@example.com", "signin"}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	err := store.ActivateMagicLinkDelivery(context.Background(), "delivery-1", "claim-1", coreauth.MagicLink{
		ID: "9a69e9fe-c3d0-497b-b2c2-06751de328d9", Email: "person@example.com", TokenHash: digest,
		Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if !strings.Contains(calls[1].Query, "claim_token = $2") || !strings.Contains(calls[1].Query, "FOR UPDATE") ||
		!strings.Contains(calls[2].Query, "token_hash = $3") ||
		!strings.Contains(calls[2].Query, "claim_token = $2") {
		t.Fatalf("activation transaction = %#v", calls)
	}
	for _, call := range calls {
		for _, arg := range call.Args {
			if value, ok := arg.Value.(string); ok && value == "raw-token-never-an-argument" {
				t.Fatal("raw token crossed SQL boundary")
			}
		}
	}
}

func TestClaimTerminalizesExpiredFinalAttemptAndClearsToken(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 13)},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	jobs, err := store.ClaimMagicLinkDeliveries(context.Background(), now, time.Minute, 25, 5)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("claim = %#v, %v", jobs, err)
	}
	calls := script.Calls()
	if !strings.Contains(calls[2].Query, "status = 'dead'") || !strings.Contains(calls[2].Query, "token_hash = NULL") ||
		!strings.Contains(calls[2].Query, "attempts >= $2") {
		t.Fatalf("final-attempt recovery = %#v", calls)
	}
}

func TestRetryInvalidatesAttemptTokenAndTransitionsAtomically(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"status"}, Rows: [][]driver.Value{{"pending"}}},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	status, err := store.RetryMagicLinkDelivery(context.Background(), "delivery-1", "claim-1", now, now.Add(time.Minute), 5)
	if err != nil || status != coreauth.MagicLinkDeliveryPending {
		t.Fatalf("retry = %q, %v", status, err)
	}
	calls := script.Calls()
	if calls[0].Operation != fakedb.Begin || calls[2].Operation != fakedb.Commit ||
		!strings.Contains(calls[1].Query, "token_hash = NULL") {
		t.Fatalf("retry transaction = %#v", calls)
	}
}
