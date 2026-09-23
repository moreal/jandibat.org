//go:build integration

package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func magicLinkRolePool(t *testing.T, ctx context.Context, envName, role string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(envName)
	if dsn == "" {
		root := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
		if root == "" {
			t.Skip("set JANDIBAT_TEST_DATABASE_URL to an isolated migrated CockroachDB")
		}
		parsed, err := url.Parse(root)
		if err != nil {
			t.Fatal(err)
		}
		parsed.User = url.User(role)
		dsn = parsed.String()
	}
	pool := authIntegrationPool(t, ctx, dsn)
	return pool
}

func TestPGXMagicLinkExpiryAndSupersessionWithAdminFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := magicLinkRolePool(t, ctx, "JANDIBAT_TEST_DATABASE_URL", "root")
	store, err := New(admin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := "pgx-link-" + uuid.NewString() + "@example.invalid"
	first := coreauth.MagicLink{ID: uuid.NewString(), Email: email, TokenHash: coreauth.Digest(sha256.Sum256([]byte("first:" + email))), Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	second := coreauth.MagicLink{ID: uuid.NewString(), Email: email, TokenHash: coreauth.Digest(sha256.Sum256([]byte("second:" + email))), Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now.Add(time.Second), ExpiresAt: now.Add(time.Hour)}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM magic_link_tokens WHERE email = $1`, email)
	})
	if err := store.SaveMagicLink(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeMagicLink(ctx, first.TokenHash, first.Purpose, first.ExpiresAt); !errors.Is(err, coreauth.ErrExpired) {
		t.Fatalf("expired link error = %v, want ErrExpired", err)
	}
	if err := store.SaveMagicLink(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeMagicLink(ctx, first.TokenHash, first.Purpose, now.Add(2*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("superseded link error = %v, want ErrConsumed", err)
	}
	if err := store.InvalidateMagicLink(ctx, second.TokenHash, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeMagicLink(ctx, second.TokenHash, second.Purpose, now.Add(4*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("invalidated link error = %v, want ErrConsumed", err)
	}
}

func TestPGXWorkerFinalAttemptExpiresAndErasesBearer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := magicLinkRolePool(t, ctx, "JANDIBAT_TEST_DATABASE_URL", "root")
	api := magicLinkRolePool(t, ctx, "JANDIBAT_TEST_API_DATABASE_URL", "jandibat_api")
	worker := magicLinkRolePool(t, ctx, "JANDIBAT_TEST_WORKER_DATABASE_URL", "jandibat_worker")
	apiStore, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	workerStore, err := New(worker)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := uuid.NewString()
	email := "pgx-final-" + id + "@example.invalid"
	digest := coreauth.Digest(sha256.Sum256([]byte("final:" + id)))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM magic_link_mail_outbox WHERE id = $1::UUID`, id)
	})
	intent := coreauth.MagicLinkDelivery{ID: id, RecipientEmail: email, RedirectURI: "https://example.invalid/auth", Purpose: coreauth.MagicLinkPurposeSignIn, Status: coreauth.MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now}
	if err := apiStore.SaveMagicLinkDeliveryIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	claimed, err := workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(time.Second), time.Minute, 1, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim = (%+v, %v)", claimed, err)
	}
	link := coreauth.MagicLink{ID: id, Email: email, TokenHash: digest, Purpose: intent.Purpose, CreatedAt: now.Add(2 * time.Second), ExpiresAt: now.Add(time.Hour)}
	if err := workerStore.ActivateMagicLinkDelivery(ctx, id, claimed[0].ClaimToken, link); err != nil {
		t.Fatal(err)
	}
	claimed, err = workerStore.ClaimMagicLinkDeliveries(ctx, now.Add(2*time.Minute), time.Minute, 1, 1)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("expired final attempt claim = (%+v, %v), want none", claimed, err)
	}
	var status string
	var hasDigest bool
	if err := admin.QueryRow(ctx, `SELECT status, token_hash IS NOT NULL FROM magic_link_mail_outbox WHERE id = $1::UUID`, id).Scan(&status, &hasDigest); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || hasDigest {
		t.Fatalf("terminal delivery = (status=%s, hasDigest=%t), want (dead, false)", status, hasDigest)
	}
	if _, err := apiStore.ConsumeMagicLink(ctx, digest, link.Purpose, now.Add(2*time.Minute)); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("terminalized token error = %v, want ErrNotFound", err)
	}
}
