package cockroach

import (
	"bytes"
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
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/identity"
)

func TestNewRejectsNilDatabase(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) || store != nil {
		t.Fatalf("New(nil) = (%v, %v), want (nil, ErrNilDB)", store, err)
	}
}

func TestGetUserByIDThroughDatabaseSQL(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "email", "status", "verified", "created", "updated"},
		Rows: [][]driver.Value{{
			"user-1", "person@example.com", "active", now, now, now,
		}},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	user, err := store.GetUserByID(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetUserByID() error = %v", err)
	}
	if user.ID != "user-1" || user.Status != coreauth.UserStatusActive || user.EmailVerifiedAt == nil {
		t.Fatalf("GetUserByID() = %#v", user)
	}
	call := script.Calls()[0]
	if !strings.Contains(call.Query, "FROM users WHERE id = $1") || call.Args[0].Value != "user-1" {
		t.Fatalf("query call = %#v", call)
	}
}

func TestGetOrCreateUserChecksEveryDeletedIdentityHMACRotationKey(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 6)})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	keys := map[string][]byte{
		"z-new": []byte("new-key-0123456789abcdef01234567"),
		"a-old": []byte("old-key-0123456789abcdef01234567"),
	}
	store, err := NewWithDeletedIdentityHMACKeys(db, keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOrCreateUserByEmail(context.Background(), " PERSON@Example.COM ", "user-new", now); !errors.Is(err, coreauth.ErrUserDisabled) {
		t.Fatalf("GetOrCreateUserByEmail() error = %v, want ErrUserDisabled", err)
	}
	calls := script.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "deleted_identity_tombstones_v2") ||
		!strings.Contains(calls[0].Query, "(identity_key_id, identity_digest) IN (($5, $6), ($7, $8))") {
		t.Fatalf("rotation query = %#v", calls)
	}
	if calls[0].Args[4].Value != "a-old" || calls[0].Args[6].Value != "z-new" {
		t.Fatalf("key order = %#v", calls[0].Args)
	}
	for index, id := range []string{"a-old", "z-new"} {
		want, ok := identity.EmailHMAC("person@example.com", keys[id])
		if !ok || !bytes.Equal(calls[0].Args[5+index*2].Value.([]byte), want[:]) {
			t.Fatalf("digest for %s = %#v", id, calls[0].Args[5+index*2].Value)
		}
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

func TestUseSessionReturnsClaimedSession(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("session"))
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "user", "hash", "created", "expires", "revoked", "seen", "ip", "agent"},
		Rows: [][]driver.Value{{
			"cf0e620a-9568-4771-a0c9-54d8833f0950", "user-1", digest[:], now.Add(-time.Hour),
			now.Add(time.Hour), nil, now, "127.0.0.1", "browser",
		}},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	session, err := store.UseSession(context.Background(), digest, now)
	if err != nil {
		t.Fatalf("UseSession() error = %v", err)
	}
	if session.LastSeenAt == nil || !session.LastSeenAt.Equal(now) || session.IPAddress != "127.0.0.1" {
		t.Fatalf("UseSession() = %#v", session)
	}
	if query := script.Calls()[0].Query; !strings.Contains(query, "revoked_at IS NULL AND expires_at > $2") ||
		!strings.Contains(query, "users.status = 'active'") {
		t.Fatalf("session claim query = %s", query)
	}
}

func TestSessionAuthenticationJoinsOnlyAnAlreadyActiveLazyTransaction(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("session boundary"))
	row := fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "user", "hash", "created", "expires", "revoked", "seen", "ip", "agent"},
		Rows: [][]driver.Value{{
			"cf0e620a-9568-4771-a0c9-54d8833f0950", "user-1", digest[:], now.Add(-time.Hour),
			now.Add(time.Hour), nil, now, "", "",
		}},
	}
	t.Run("inactive does not begin", func(t *testing.T) {
		script := fakedb.New(row)
		db := script.Open()
		t.Cleanup(func() { _ = db.Close() })
		store, _ := New(db)
		ctx, lazy := appdb.WithLazyTransaction(context.Background(), db)
		if _, err := store.UseSession(ctx, digest, now); err != nil {
			t.Fatal(err)
		}
		if _, active := lazy.Transaction(); active || len(script.Calls()) != 1 || script.Calls()[0].Operation != fakedb.Query {
			t.Fatalf("inactive lazy transaction calls=%#v active=%t", script.Calls(), active)
		}
	})
	t.Run("active is reused", func(t *testing.T) {
		script := fakedb.New(fakedb.Step{Operation: fakedb.Begin}, row, fakedb.Step{Operation: fakedb.Rollback})
		db := script.Open()
		t.Cleanup(func() { _ = db.Close() })
		store, _ := New(db)
		ctx, lazy := appdb.WithLazyTransaction(context.Background(), db)
		if _, err := appdb.MutationExecutor(ctx, db); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UseSession(ctx, digest, now); err != nil {
			t.Fatal(err)
		}
		if err := lazy.Rollback(); err != nil {
			t.Fatal(err)
		}
		calls := script.Calls()
		if len(calls) != 3 || calls[0].Operation != fakedb.Begin || calls[1].Operation != fakedb.Query || calls[2].Operation != fakedb.Rollback {
			t.Fatalf("active lazy transaction calls=%#v", calls)
		}
	})
}

func TestSaveSessionRequiresActiveUserInInsertStatement(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("session"))
	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 0})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	err := store.SaveSession(context.Background(), coreauth.Session{
		ID: "cf0e620a-9568-4771-a0c9-54d8833f0950", UserID: "user-1", TokenHash: digest,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if !errors.Is(err, coreauth.ErrUserDisabled) {
		t.Fatalf("SaveSession() error = %v, want ErrUserDisabled", err)
	}
	if query := script.Calls()[0].Query; !strings.Contains(query, "FROM users") || !strings.Contains(query, "status = 'active'") {
		t.Fatalf("session insert query = %s", query)
	}
}

func TestGetOrCreateUserPreservesDeletionPendingStatus(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "email", "status", "verified", "created", "updated"},
		Rows: [][]driver.Value{{
			"user-1", "person@example.com", "deletion_pending", now, now, now,
		}},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	user, err := store.GetOrCreateUserByEmail(context.Background(), "person@example.com", "new-user", now)
	if err != nil {
		t.Fatal(err)
	}
	if user.Status != coreauth.UserStatusDeletionPending {
		t.Fatalf("status = %q, want deletion_pending", user.Status)
	}
	call := script.Calls()[0]
	if query := call.Query; strings.Contains(query, "status = excluded.status") || !strings.Contains(query, "ON CONFLICT (primary_email)") ||
		!strings.Contains(query, "deleted_identity_tombstones") || !strings.Contains(query, "expires_at > $3") {
		t.Fatalf("unsafe get-or-create query = %s", query)
	}
	wantHash := sha256.Sum256([]byte("person@example.com"))
	if got, ok := call.Args[3].Value.([]byte); !ok || string(got) != string(wantHash[:]) {
		t.Fatalf("email tombstone digest = %x, want %x", got, wantHash)
	}
}

func TestGetOrCreateUserRejectsRetainedDeletionTombstone(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 6)})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	_, err := store.GetOrCreateUserByEmail(context.Background(), "Deleted@Example.com", "new-user", now)
	if !errors.Is(err, coreauth.ErrUserDisabled) {
		t.Fatalf("GetOrCreateUserByEmail() error = %v, want ErrUserDisabled", err)
	}
	call := script.Calls()[0]
	wantHash := sha256.Sum256([]byte("deleted@example.com"))
	if got, ok := call.Args[3].Value.([]byte); !ok || string(got) != string(wantHash[:]) {
		t.Fatalf("canonical email digest = %x, want %x", got, wantHash)
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
