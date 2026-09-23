package cockroach

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/identity"
)

// TestCockroachMagicLinkPurposeBindingAndReplay proves the database claim is
// scoped by purpose: a token from another flow is neither accepted nor burned.
func TestCockroachMagicLinkPurposeBindingAndReplay(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}

	suffix := authIntegrationSuffix(t)
	linkID := "d7aeb5c0-61f6-4c1c-91a8-" + suffix
	digest := sha256.Sum256([]byte("verify-email-token-" + suffix))
	now := time.Now().UTC().Truncate(time.Microsecond)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM magic_link_tokens WHERE id = $1`, linkID)
	})

	store, err := New(authIntegrationPool(t, ctx, dsn))
	if err != nil {
		t.Fatal(err)
	}
	link := coreauth.MagicLink{
		ID: linkID, Email: "purpose-" + suffix + "@example.invalid", TokenHash: digest,
		Purpose: coreauth.MagicLinkPurposeVerifyEmail, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if err := store.SaveMagicLink(ctx, link); err != nil {
		t.Fatalf("save verify-email magic link: %v", err)
	}
	if _, err := store.ConsumeMagicLink(ctx, digest, coreauth.MagicLinkPurposeSignIn, now.Add(time.Second)); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("cross-purpose consume error = %v, want ErrNotFound", err)
	}
	claimed, err := store.ConsumeMagicLink(ctx, digest, coreauth.MagicLinkPurposeVerifyEmail, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("purpose-correct consume: %v", err)
	}
	if claimed.ID != link.ID || claimed.Purpose != coreauth.MagicLinkPurposeVerifyEmail || claimed.ConsumedAt == nil {
		t.Fatalf("claimed magic link = %#v", claimed)
	}
	if _, err := store.ConsumeMagicLink(ctx, digest, coreauth.MagicLinkPurposeVerifyEmail, now.Add(3*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("replayed magic-link consume error = %v, want ErrConsumed", err)
	}
}

func TestCockroachMagicLinkIntentOutboxRolesAndCrashRecovery(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	roleDB := func(env, role string) *sql.DB {
		roleDSN := os.Getenv(env)
		if roleDSN == "" {
			parsed, parseErr := url.Parse(dsn)
			if parseErr != nil {
				t.Fatalf("parse integration DSN: %v", parseErr)
			}
			parsed.User = url.User(role)
			roleDSN = parsed.String()
		}
		db, openErr := sql.Open("pgx", roleDSN)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() { _ = db.Close() })
		if pingErr := db.PingContext(ctx); pingErr != nil {
			t.Fatalf("ping as %s: %v", role, pingErr)
		}
		return db
	}
	roleDB("JANDIBAT_TEST_API_DATABASE_URL", "jandibat_api")
	workerDB := roleDB("JANDIBAT_TEST_WORKER_DATABASE_URL", "jandibat_worker")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if apiDSN == "" {
		parsed, parseErr := url.Parse(dsn)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		parsed.User = url.User("jandibat_api")
		apiDSN = parsed.String()
	}
	workerDSN := os.Getenv("JANDIBAT_TEST_WORKER_DATABASE_URL")
	if workerDSN == "" {
		parsed, parseErr := url.Parse(dsn)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		parsed.User = url.User("jandibat_worker")
		workerDSN = parsed.String()
	}
	apiStore, err := New(authIntegrationPool(t, ctx, apiDSN))
	if err != nil {
		t.Fatal(err)
	}
	workerStore, err := New(authIntegrationPool(t, ctx, workerDSN))
	if err != nil {
		t.Fatal(err)
	}
	suffix := authIntegrationSuffix(t)
	email := "mail-outbox-" + suffix + "@example.invalid"
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := func(prefix string) string { return prefix + suffix }
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.ExecContext(cleanupCtx, `DELETE FROM magic_link_mail_outbox WHERE recipient_email = $1`, email)
	}
	cleanup()
	t.Cleanup(cleanup)

	var forbiddenColumns int
	if err := adminDB.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'magic_link_mail_outbox'
  AND column_name IN ('token_ciphertext', 'token_key_id', 'raw_token')`).Scan(&forbiddenColumns); err != nil || forbiddenColumns != 0 {
		t.Fatalf("forbidden bearer-storage columns = %d, error = %v", forbiddenColumns, err)
	}
	intent := func(value string, at time.Time) coreauth.MagicLinkDelivery {
		return coreauth.MagicLinkDelivery{
			ID: value, RecipientEmail: email, RedirectURI: "https://app.example/#auth", Purpose: coreauth.MagicLinkPurposeSignIn,
			Status: coreauth.MagicLinkDeliveryPending, AvailableAt: at, CreatedAt: at, UpdatedAt: at,
		}
	}
	if err := apiStore.SaveMagicLinkDeliveryIntent(ctx, intent(id("10bb2480-3560-4e93-b5ef-"), now)); err != nil {
		t.Fatalf("API-role save intent: %v", err)
	}
	claimed, err := workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(time.Second), time.Minute, 1, 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("worker-role claim = %#v, %v", claimed, err)
	}
	firstToken := "first-outbox-token-" + suffix + "-long-enough"
	firstDigest := sha256.Sum256([]byte(firstToken))
	firstLink := coreauth.MagicLink{
		ID: id("20bb2480-3560-4e93-b5ef-"), Email: email, TokenHash: firstDigest, Purpose: coreauth.MagicLinkPurposeSignIn,
		CreatedAt: now.Add(2 * time.Second), ExpiresAt: now.Add(17 * time.Minute),
	}
	if err := workerStore.ActivateMagicLinkDelivery(ctx, claimed[0].ID, "00000000-0000-4000-8000-000000000000", firstLink); !errors.Is(err, coreauth.ErrConflict) {
		t.Fatalf("stale claim activation error = %v, want ErrConflict", err)
	}
	var staleDigestCount int
	if err := adminDB.QueryRowContext(ctx, `SELECT count(*) FROM magic_link_mail_outbox WHERE id = $1 AND token_hash IS NOT NULL`, claimed[0].ID).Scan(&staleDigestCount); err != nil || staleDigestCount != 0 {
		t.Fatalf("stale claim wrote digest: count=%d, error=%v", staleDigestCount, err)
	}
	if err := workerStore.ActivateMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, firstLink); err != nil {
		t.Fatalf("worker-role activate: %v", err)
	}
	status, err := workerStore.RetryMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, now.Add(3*time.Second), now.Add(2*time.Minute), 5)
	if err != nil || status != coreauth.MagicLinkDeliveryPending {
		t.Fatalf("worker-role retry = %q, %v", status, err)
	}
	var digestCount int
	if err := adminDB.QueryRowContext(ctx, `SELECT count(*) FROM magic_link_mail_outbox WHERE token_hash = $1`, firstDigest[:]).Scan(&digestCount); err != nil || digestCount != 0 {
		t.Fatalf("failed attempt digest remains: count=%d, %v", digestCount, err)
	}

	claimed, err = workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(3*time.Minute), time.Minute, 1, 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("second claim = %#v, %v", claimed, err)
	}
	secondToken := "second-outbox-token-" + suffix + "-long-enough"
	secondDigest := sha256.Sum256([]byte(secondToken))
	secondLink := coreauth.MagicLink{
		ID: id("30bb2480-3560-4e93-b5ef-"), Email: email, TokenHash: secondDigest, Purpose: coreauth.MagicLinkPurposeSignIn,
		CreatedAt: now.Add(3*time.Minute + time.Second), ExpiresAt: now.Add(18 * time.Minute),
	}
	if err := workerStore.ActivateMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, secondLink); err != nil {
		t.Fatalf("second activate: %v", err)
	}
	consumed, err := apiStore.ConsumeMagicLink(ctx, secondDigest, coreauth.MagicLinkPurposeSignIn, now.Add(3*time.Minute+2*time.Second))
	if err != nil || consumed.Email != email || consumed.ConsumedAt == nil {
		t.Fatalf("API-role consume processing delivery = %#v, %v", consumed, err)
	}
	claimed, err = workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(5*time.Minute), time.Minute, 1, 5)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("consumed-before-complete reclaim = %#v, %v", claimed, err)
	}
	var outboxStatus string
	if err := adminDB.QueryRowContext(ctx, `SELECT status FROM magic_link_mail_outbox WHERE id = $1`, id("10bb2480-3560-4e93-b5ef-")).Scan(&outboxStatus); err != nil || outboxStatus != "sent" {
		t.Fatalf("consumed delivery status = %q, %v", outboxStatus, err)
	}
	if _, err := apiStore.ConsumeMagicLink(ctx, secondDigest, coreauth.MagicLinkPurposeSignIn, now.Add(5*time.Minute+time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("outbox replay error = %v, want ErrConsumed", err)
	}
	if _, err := apiStore.ConsumeMagicLink(ctx, secondDigest, coreauth.MagicLinkPurposeVerifyEmail, now.Add(5*time.Minute+time.Second)); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("outbox wrong-purpose error = %v, want ErrNotFound", err)
	}

	// An unconsumed send-before-complete crash is reclaimed. Activation under
	// the new claim invalidates the previous digest and installs a fresh one.
	thirdIntent := intent(id("40bb2480-3560-4e93-b5ef-"), now.Add(6*time.Minute))
	if err := apiStore.SaveMagicLinkDeliveryIntent(ctx, thirdIntent); err != nil {
		t.Fatalf("third intent: %v", err)
	}
	claimed, err = workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(6*time.Minute+time.Second), time.Minute, 1, 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("third claim = %#v, %v", claimed, err)
	}
	thirdDigest := sha256.Sum256([]byte("third-outbox-token-" + suffix + "-long-enough"))
	thirdLink := coreauth.MagicLink{
		ID: id("50bb2480-3560-4e93-b5ef-"), Email: email, TokenHash: thirdDigest, Purpose: coreauth.MagicLinkPurposeSignIn,
		CreatedAt: now.Add(6*time.Minute + 2*time.Second), ExpiresAt: now.Add(21 * time.Minute),
	}
	if err := workerStore.ActivateMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, thirdLink); err != nil {
		t.Fatal(err)
	}
	claimed, err = workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(8*time.Minute), time.Minute, 1, 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("unconsumed reclaim = %#v, %v", claimed, err)
	}
	fourthDigest := sha256.Sum256([]byte("fourth-outbox-token-" + suffix + "-long-enough"))
	fourthLink := coreauth.MagicLink{
		ID: id("60bb2480-3560-4e93-b5ef-"), Email: email, TokenHash: fourthDigest, Purpose: coreauth.MagicLinkPurposeSignIn,
		CreatedAt: now.Add(8*time.Minute + time.Second), ExpiresAt: now.Add(23 * time.Minute),
	}
	if err := workerStore.ActivateMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, fourthLink); err != nil {
		t.Fatalf("fresh activation after crash: %v", err)
	}
	if err := adminDB.QueryRowContext(ctx, `SELECT count(*) FROM magic_link_mail_outbox WHERE token_hash = $1`, thirdDigest[:]).Scan(&digestCount); err != nil || digestCount != 0 {
		t.Fatalf("crashed attempt digest remains: count=%d, %v", digestCount, err)
	}
	if err := workerStore.CompleteMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, now.Add(8*time.Minute+2*time.Second)); err != nil {
		t.Fatalf("complete fourth delivery: %v", err)
	}
	consumed, err = apiStore.ConsumeMagicLink(ctx, fourthDigest, coreauth.MagicLinkPurposeSignIn, now.Add(8*time.Minute+3*time.Second))
	if err != nil || consumed.Email != email {
		t.Fatalf("API-role consume sent delivery = %#v, %v", consumed, err)
	}

	// Superseding an activated claim clears its usable digest atomically.
	fifthIntent := intent(id("70bb2480-3560-4e93-b5ef-"), now.Add(9*time.Minute))
	if err := apiStore.SaveMagicLinkDeliveryIntent(ctx, fifthIntent); err != nil {
		t.Fatalf("fifth intent: %v", err)
	}
	claimed, err = workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(9*time.Minute+time.Second), time.Minute, 1, 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("fifth claim = %#v, %v", claimed, err)
	}
	fifthDigest := sha256.Sum256([]byte("fifth-outbox-token-" + suffix + "-long-enough"))
	if err := workerStore.ActivateMagicLinkDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken, coreauth.MagicLink{
		ID: id("80bb2480-3560-4e93-b5ef-"), Email: email, TokenHash: fifthDigest, Purpose: coreauth.MagicLinkPurposeSignIn,
		CreatedAt: now.Add(9*time.Minute + 2*time.Second), ExpiresAt: now.Add(24 * time.Minute),
	}); err != nil {
		t.Fatalf("fifth activate: %v", err)
	}
	if err := apiStore.SaveMagicLinkDeliveryIntent(ctx, intent(id("90bb2480-3560-4e93-b5ef-"), now.Add(10*time.Minute))); err != nil {
		t.Fatalf("API supersede: %v", err)
	}
	if err := adminDB.QueryRowContext(ctx, `SELECT status FROM magic_link_mail_outbox WHERE id = $1`, fifthIntent.ID).Scan(&outboxStatus); err != nil || outboxStatus != "superseded" {
		t.Fatalf("superseded status = %q, %v", outboxStatus, err)
	}
	if err := adminDB.QueryRowContext(ctx, `SELECT count(*) FROM magic_link_mail_outbox WHERE token_hash = $1`, fifthDigest[:]).Scan(&digestCount); err != nil || digestCount != 0 {
		t.Fatalf("superseded digest remains: count=%d, %v", digestCount, err)
	}
	if _, err := workerDB.ExecContext(ctx, `SELECT primary_email FROM users LIMIT 1`); err == nil {
		t.Fatal("worker unexpectedly read users PII")
	}
	if _, err := workerDB.ExecContext(ctx, `SELECT token_hash FROM magic_link_tokens LIMIT 1`); err == nil {
		t.Fatal("worker unexpectedly read legacy magic-link tokens")
	}
}

// TestCockroachDeletedIdentityHMACRotationBlocksResurrection proves that an
// old-key-only v2 tombstone remains authoritative after the active key rotates.
// The auth process checks every configured read key in the same implicit
// serializable statement that would create a replacement user.
func TestCockroachDeletedIdentityHMACRotationBlocksResurrection(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}

	suffix := authIntegrationSuffix(t)
	email := "identity-hmac-" + suffix + "@example.invalid"
	requestID := "identity-hmac-request-" + suffix
	requestUUID := "a73c0625-6081-4e17-8457-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldKey := []byte("old-key-0123456789abcdef01234567")
	newKey := []byte("new-key-0123456789abcdef01234567")
	digest, ok := identity.EmailHMAC(email, oldKey)
	if !ok {
		t.Fatal("derive old identity digest")
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO deletion_requests (id, request_id, target_type, target_id, status, requested_at, updated_at)
VALUES ($1, $2, 'account', $3, 'deleting_primary', $4, $4)`, requestUUID, requestID, "deleted-user-"+suffix, now); err != nil {
		t.Fatalf("insert deletion request: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO deleted_identity_tombstones_v2
  (identity_key_id, identity_digest, deletion_request_id, created_at, expires_at)
VALUES ('old', $1, $2, $3, $4)`, digest[:], requestUUID, now, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("insert v2 tombstone: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE primary_email = $1`, email)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deleted_identity_tombstones_v2 WHERE deletion_request_id = $1`, requestUUID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deletion_requests WHERE id = $1`, requestUUID)
	})

	store, err := NewWithDeletedIdentityHMACKeys(authIntegrationPool(t, ctx, dsn), map[string][]byte{"new": newKey, "old": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOrCreateUserByEmail(ctx, email, "replacement-"+suffix, now.Add(time.Minute)); !errors.Is(err, coreauth.ErrUserDisabled) {
		t.Fatalf("rotated-key resurrection error = %v, want ErrUserDisabled", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE primary_email = $1`, email).Scan(&count); err != nil || count != 0 {
		t.Fatalf("replacement users = %d, error = %v", count, err)
	}
}

// TestCockroachConsumedMagicLinkCannotResurrectDeletedIdentity covers the
// critical interleaving where a request already owns a consumed link when
// account deletion commits. The digest tombstone and user creation are read
// and written by one implicit serializable statement, so no active replacement
// user or session can be issued afterward.
func TestCockroachConsumedMagicLinkCannotResurrectDeletedIdentity(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}

	suffix := authIntegrationSuffix(t)
	userID := "it_deleted_auth_user_" + suffix
	email := "deleted-auth-" + suffix + "@example.invalid"
	linkID := "06e6c640-747e-462a-845f-" + suffix
	requestID := "deleted-auth-request-" + suffix
	requestUUID := "93d0d16c-7cff-40fb-b137-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	digest := sha256.Sum256([]byte("consumed-before-delete-" + suffix))
	if _, err := db.ExecContext(ctx, `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $3, $3)`, userID, email, now); err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deleted_identity_tombstones WHERE deletion_request_id = $1`, requestUUID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deletion_requests WHERE id = $1`, requestUUID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM magic_link_tokens WHERE id = $1`, linkID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE primary_email = $1`, email)
	})

	store, err := New(authIntegrationPool(t, ctx, dsn))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMagicLink(ctx, coreauth.MagicLink{
		ID: linkID, Email: email, TokenHash: digest,
		Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}); err != nil {
		t.Fatalf("save sign-in link: %v", err)
	}
	claimed, err := store.ConsumeMagicLink(ctx, digest, coreauth.MagicLinkPurposeSignIn, now.Add(time.Second))
	if err != nil || claimed.Email != email {
		t.Fatalf("consume before deletion = %#v, %v", claimed, err)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	deletionCommitted := false
	defer func() {
		if !deletionCommitted {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO deletion_requests (id, request_id, target_type, target_id, status, requested_at, updated_at)
VALUES ($1, $2, 'account', $3, 'deleting_primary', $4, $4)`, requestUUID, requestID, userID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("insert deletion request: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		t.Fatalf("lock deleted identity: %v", err)
	}
	emailHash := sha256.Sum256([]byte(email))
	if _, err := tx.ExecContext(ctx, `
INSERT INTO deleted_identity_tombstones (email_hash, deletion_request_id, created_at, expires_at)
VALUES ($1, $2, $3, $4)`, emailHash[:], requestUUID, now.Add(2*time.Second), now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("insert identity tombstone: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("delete identity: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, createErr := store.GetOrCreateUserByEmail(ctx, claimed.Email, "replacement-"+suffix, now.Add(3*time.Second))
		result <- createErr
	}()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit account deletion: %v", err)
	}
	deletionCommitted = true
	if err := <-result; !errors.Is(err, coreauth.ErrUserDisabled) {
		t.Fatalf("post-consume identity recreation error = %v, want ErrUserDisabled", err)
	}
	var users, sessions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE primary_email = $1`, email).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM user_sessions WHERE user_id = $1 OR user_id = $2`, userID, "replacement-"+suffix).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if users != 0 || sessions != 0 {
		t.Fatalf("deleted identity resurrected: users=%d sessions=%d", users, sessions)
	}
}

// TestCockroachPasskeyCeremonyReplayAndCredentialCounterCAS is opt-in because
// it requires a migrated CockroachDB. Unlike the scripted SQL unit tests, this
// test proves that the schema and Cockroach's concurrent statement semantics
// admit exactly one ceremony consumer and exactly one signature-counter writer.
func TestCockroachPasskeyCeremonyReplayAndCredentialCounterCAS(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}

	suffix := authIntegrationSuffix(t)
	userID := "it_auth_user_" + suffix
	ceremonyID := "8da3b5d8-303f-4fb6-bbe4-" + suffix
	passkeyID := "e6ba61c0-5ae7-44f1-ab4c-" + suffix
	credentialID := []byte("it-credential-" + suffix)
	if _, err := db.ExecContext(ctx, `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
VALUES ($1, $2, now(), 'active', now(), now())`, userID, userID+"@example.invalid"); err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	store, err := New(authIntegrationPool(t, ctx, dsn))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ceremony := coreauth.PasskeyCeremony{
		ID:              ceremonyID,
		Kind:            coreauth.CeremonyAuthentication,
		Challenge:       "integration-challenge-" + suffix,
		UserID:          userID,
		VerifierSession: json.RawMessage(`{"challenge":"server-bound"}`),
		CreatedAt:       now,
		ExpiresAt:       now.Add(5 * time.Minute),
	}
	if err := store.SaveCeremony(ctx, ceremony); err != nil {
		t.Fatalf("save ceremony: %v", err)
	}

	startConsume := make(chan struct{})
	consumeResults := make(chan error, 2)
	for range 2 {
		go func() {
			<-startConsume
			_, err := store.ConsumeCeremony(ctx, ceremony.ID, ceremony.Kind, now.Add(time.Second))
			consumeResults <- err
		}()
	}
	close(startConsume)
	assertOneSuccessOneConflict(t, consumeResults, coreauth.ErrConsumed)
	if _, err := store.ConsumeCeremony(ctx, ceremony.ID, ceremony.Kind, now.Add(2*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("sequential ceremony replay error = %v, want ErrConsumed", err)
	}

	credential := coreauth.PasskeyCredential{
		ID:                 passkeyID,
		UserID:             userID,
		CredentialID:       credentialID,
		PublicKey:          []byte("integration-public-key"),
		SignCount:          4,
		Transports:         []string{"internal", "hybrid"},
		VerifierCredential: json.RawMessage(`{"version":1,"signCount":4}`),
		CreatedAt:          now,
	}
	if err := store.SaveCredential(ctx, credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	startCounter := make(chan struct{})
	counterResults := make(chan error, 2)
	for range 2 {
		go func() {
			<-startCounter
			counterResults <- store.UseCredential(
				ctx,
				credential.CredentialID,
				credential.SignCount,
				credential.SignCount+1,
				json.RawMessage(`{"version":1,"signCount":5}`),
				now.Add(3*time.Second),
			)
		}()
	}
	close(startCounter)
	assertOneSuccessOneConflict(t, counterResults, coreauth.ErrConflict)

	stored, err := store.GetCredentialByCredentialID(ctx, credential.CredentialID)
	if err != nil {
		t.Fatalf("load credential after CAS: %v", err)
	}
	var verifierRecord struct {
		Version   int    `json:"version"`
		SignCount uint32 `json:"signCount"`
	}
	if err := json.Unmarshal(stored.VerifierCredential, &verifierRecord); err != nil {
		t.Fatalf("decode credential verifier record: %v", err)
	}
	if stored.SignCount != 5 || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(now.Add(3*time.Second)) ||
		verifierRecord.Version != 1 || verifierRecord.SignCount != 5 || len(stored.Transports) != 2 ||
		stored.Transports[0] != "internal" || stored.Transports[1] != "hybrid" {
		t.Fatalf("stored credential after CAS = %#v", stored)
	}
}

func assertOneSuccessOneConflict(t *testing.T, results <-chan error, conflict error) {
	t.Helper()
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, conflict):
			conflicts++
		default:
			t.Fatalf("concurrent operation returned unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: successes=%d conflicts=%d, want one each", successes, conflicts)
	}
}

func authIntegrationSuffix(t *testing.T) string {
	t.Helper()
	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatalf("generate integration suffix: %v", err)
	}
	return hex.EncodeToString(value[:])
}

func authIntegrationPool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}
	return pool
}
