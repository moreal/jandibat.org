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
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestNewRejectsNilDatabase(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) || store != nil {
		t.Fatalf("New(nil) = (%v, %v), want (nil, ErrNilDB)", store, err)
	}
}

func TestSessionOperationsRequireGeneratedPGXStore(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("session pgx boundary"))
	script := fakedb.New()
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"save", func() error {
			return store.SaveSession(context.Background(), coreauth.Session{ID: "cf0e620a-9568-4771-a0c9-54d8833f0950", UserID: "user-1", TokenHash: digest, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		}},
		{"use", func() error { _, err := store.UseSession(context.Background(), digest, now); return err }},
		{"get", func() error { _, err := store.GetSession(context.Background(), digest); return err }},
		{"list", func() error { _, err := store.ListSessionsByUser(context.Background(), "user-1"); return err }},
		{"revoke", func() error { return store.RevokeSession(context.Background(), digest, now) }},
		{"revoke others", func() error { return store.RevokeOtherSessions(context.Background(), "user-1", digest, now) }},
		{"revoke by ID", func() error {
			return store.RevokeSessionByID(context.Background(), "user-1", "cf0e620a-9568-4771-a0c9-54d8833f0950", now)
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrNilDB) {
				t.Fatalf("SQL-only session operation error = %v, want ErrNilDB", err)
			}
		})
	}
	if calls := script.Calls(); len(calls) != 0 {
		t.Fatalf("SQL-only session operations made legacy queries: %#v", calls)
	}
}

func TestUserOperationsRequireGeneratedPGXStore(t *testing.T) {
	script := fakedb.New()
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetUserByID(context.Background(), "user-1"); !errors.Is(err, ErrNilDB) {
		t.Fatalf("SQL-only GetUserByID error = %v, want ErrNilDB", err)
	}
	if _, err := store.GetOrCreateUserByEmail(context.Background(), "person@example.com", "user-1", time.Now().UTC()); !errors.Is(err, ErrNilDB) {
		t.Fatalf("SQL-only GetOrCreateUserByEmail error = %v, want ErrNilDB", err)
	}
	if calls := script.Calls(); len(calls) != 0 {
		t.Fatalf("SQL-only user operations made legacy queries: %#v", calls)
	}
}

func TestCeremonyMutationRequiresPGXPool(t *testing.T) {
	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 1})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ceremony := coreauth.PasskeyCeremony{
		ID: "b73db5cc-c6bc-4298-9361-6dd00b876075", Kind: coreauth.CeremonyAuthentication,
		Challenge: "browser-challenge", VerifierSession: json.RawMessage(`{"opaque":true}`),
		CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := store.SaveCeremony(context.Background(), ceremony); !errors.Is(err, ErrNilDB) {
		t.Fatalf("SQL-only ceremony save error=%v, want ErrNilDB", err)
	}
	if len(script.Calls()) != 0 {
		t.Fatal("SQL-only ceremony save executed a legacy query")
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
