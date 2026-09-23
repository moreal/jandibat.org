package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
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

func TestConsumeMagicLinkDistinguishesExpiryAfterAtomicClaimMiss(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("token"))
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns:   []string{"consumed_at", "expires_at"},
			Rows:      [][]driver.Value{{nil, now.Add(-time.Second)}},
		},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	_, err := store.ConsumeMagicLink(context.Background(), digest, coreauth.MagicLinkPurposeSignIn, now)
	if !errors.Is(err, coreauth.ErrExpired) {
		t.Fatalf("ConsumeMagicLink() error = %v, want ErrExpired", err)
	}
	calls := script.Calls()
	if len(calls) != 3 || !strings.Contains(calls[0].Query, "purpose = $2") ||
		!strings.Contains(calls[0].Query, "consumed_at IS NULL AND expires_at > $3") ||
		!strings.Contains(calls[1].Query, "magic_link_mail_outbox") ||
		!strings.Contains(calls[2].Query, "token_hash = $1 AND purpose = $2") {
		t.Fatalf("atomic consume calls = %#v", calls)
	}
}

func TestConsumeMagicLinkClaimsProcessingOutboxAndTerminalizesLease(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("outbox token"))
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns:   []string{"id", "email", "hash", "purpose", "created", "expires", "consumed"},
			Rows: [][]driver.Value{{
				"delivery-1", "person@example.com", digest[:], "signin", now.Add(-time.Minute), now.Add(time.Minute), now,
			}},
		},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	link, err := store.ConsumeMagicLink(context.Background(), digest, coreauth.MagicLinkPurposeSignIn, now)
	if err != nil || link.Email != "person@example.com" || link.ConsumedAt == nil {
		t.Fatalf("ConsumeMagicLink() = %#v, %v", link, err)
	}
	calls := script.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Query, "magic_link_mail_outbox") ||
		!strings.Contains(calls[1].Query, "status = CASE WHEN status = 'processing' THEN 'sent'") ||
		!strings.Contains(calls[1].Query, "lease_until = CASE WHEN status = 'processing' THEN NULL") ||
		!strings.Contains(calls[1].Query, "purpose = $2") || !strings.Contains(calls[1].Query, "token_expires_at > $3") {
		t.Fatalf("outbox atomic consume calls = %#v", calls)
	}
}

func TestConsumeMagicLinkReportsConsumedOutboxReplay(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("consumed outbox token"))
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns:   []string{"consumed_at", "expires_at"},
			Rows:      [][]driver.Value{{now.Add(-time.Second), now.Add(time.Minute)}},
		},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	_, err := store.ConsumeMagicLink(context.Background(), digest, coreauth.MagicLinkPurposeSignIn, now)
	if !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("ConsumeMagicLink() error = %v, want ErrConsumed", err)
	}
	if calls := script.Calls(); len(calls) != 3 || !strings.Contains(calls[2].Query, "UNION ALL") ||
		!strings.Contains(calls[2].Query, "magic_link_mail_outbox") {
		t.Fatalf("outbox replay inspection calls = %#v", calls)
	}
}

func TestSaveMagicLinkInvalidatesOlderLinksAndInsertsAtomically(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("new token"))
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 2},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	link := coreauth.MagicLink{
		ID: "cf0e620a-9568-4771-a0c9-54d8833f0950", Email: "person@example.com", TokenHash: digest,
		Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}

	if err := store.SaveMagicLink(context.Background(), link); err != nil {
		t.Fatalf("SaveMagicLink() error = %v", err)
	}
	calls := script.Calls()
	if len(calls) != 4 || calls[0].Operation != fakedb.Begin || calls[3].Operation != fakedb.Commit {
		t.Fatalf("transaction calls = %#v", calls)
	}
	if !strings.Contains(calls[1].Query, "purpose = $2") ||
		calls[1].Args[1].Value != coreauth.MagicLinkPurposeSignIn ||
		!strings.Contains(calls[2].Query, "INSERT INTO magic_link_tokens") ||
		calls[2].Args[3].Value != coreauth.MagicLinkPurposeSignIn {
		t.Fatalf("magic-link statements = %#v", calls)
	}
}

func TestConsumeMagicLinkPurposeMismatchIsNotClaimed(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("verify token"))
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 7)},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"consumed_at", "expires_at"}},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	_, err := store.ConsumeMagicLink(context.Background(), digest, coreauth.MagicLinkPurposeSignIn, now)
	if !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("ConsumeMagicLink() error = %v, want ErrNotFound", err)
	}
	calls := script.Calls()
	if len(calls) != 3 || calls[0].Args[1].Value != coreauth.MagicLinkPurposeSignIn ||
		calls[1].Args[1].Value != coreauth.MagicLinkPurposeSignIn ||
		calls[2].Args[1].Value != coreauth.MagicLinkPurposeSignIn ||
		!strings.Contains(calls[0].Query, "purpose = $2") ||
		!strings.Contains(calls[1].Query, "purpose = $2") ||
		!strings.Contains(calls[2].Query, "purpose = $2") {
		t.Fatalf("purpose-bound consume calls = %#v", calls)
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

func TestUseCredentialReportsCompareAndSwapConflict(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"exists"}, Rows: [][]driver.Value{{true}}},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	err := store.UseCredential(context.Background(), []byte("credential"), 4, 5, json.RawMessage(`{"v":1}`), time.Now())
	if !errors.Is(err, coreauth.ErrConflict) {
		t.Fatalf("UseCredential() error = %v, want ErrConflict", err)
	}
	if calls := script.Calls(); len(calls) != 2 || !strings.Contains(calls[0].Query, "sign_count = $2") {
		t.Fatalf("CAS calls = %#v", calls)
	}
}

func TestGetCredentialRestoresVerifierRecordAndTransports(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "user", "credential", "key", "aaguid", "count", "transports", "verifier", "label", "created", "used"},
		Rows: [][]driver.Value{{
			"cf0e620a-9568-4771-a0c9-54d8833f0950", "user-1", []byte("credential"), []byte("public-key"),
			[]byte("aaguid"), int64(7), []string{"internal", "hybrid"}, []byte(`{"v":1}`), "Laptop", now, nil,
		}},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	credential, err := store.GetCredentialByCredentialID(context.Background(), []byte("credential"))
	if err != nil {
		t.Fatalf("GetCredentialByCredentialID() error = %v", err)
	}
	if credential.SignCount != 7 || len(credential.Transports) != 2 || string(credential.VerifierCredential) != `{"v":1}` {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestGetCredentialAcceptsNullAndDatabaseTextTransports(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		transports driver.Value
		want       []string
	}{
		{name: "null", transports: nil, want: nil},
		{name: "empty array", transports: `{}`, want: []string{}},
		{name: "database text array", transports: `{internal,hybrid}`, want: []string{"internal", "hybrid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := fakedb.New(fakedb.Step{
				Operation: fakedb.Query,
				Columns:   []string{"id", "user", "credential", "key", "aaguid", "count", "transports", "verifier", "label", "created", "used"},
				Rows: [][]driver.Value{{
					"cf0e620a-9568-4771-a0c9-54d8833f0950", "user-1", []byte("credential"), []byte("public-key"),
					nil, int64(0), test.transports, []byte(`{"v":1}`), "", now, nil,
				}},
			})
			db := script.Open()
			t.Cleanup(func() { _ = db.Close() })
			store, _ := New(db)

			credential, err := store.GetCredentialByCredentialID(context.Background(), []byte("credential"))
			if err != nil {
				t.Fatalf("GetCredentialByCredentialID() error = %v", err)
			}
			if len(credential.Transports) != len(test.want) {
				t.Fatalf("transports = %#v, want %#v", credential.Transports, test.want)
			}
			for index := range test.want {
				if credential.Transports[index] != test.want[index] {
					t.Fatalf("transports = %#v, want %#v", credential.Transports, test.want)
				}
			}
		})
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
