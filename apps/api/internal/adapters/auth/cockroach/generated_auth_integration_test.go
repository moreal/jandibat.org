//go:build integration

package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestGeneratedAuthRepositoryVerticalSlice(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	userID := "scythe-auth-user-" + suffix
	email := fmt.Sprintf("%s@example.invalid", userID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, primary_email, status, created_at, updated_at) VALUES ($1, $2, 'active', $3, $3)`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})
	got, err := store.GetUserByID(ctx, userID)
	if err != nil || got.ID != userID || got.PrimaryEmail != email || got.Status != coreauth.UserStatusActive {
		t.Fatalf("GetUserByID() = (%+v, %v)", got, err)
	}
	createdID := "scythe-auth-created-" + uuid.NewString()
	createdEmail := createdID + "@example.invalid"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, createdID)
	})
	created, err := store.GetOrCreateUserByEmail(ctx, createdEmail, createdID, now)
	if err != nil || created.ID != createdID || created.PrimaryEmail != createdEmail {
		t.Fatalf("GetOrCreateUserByEmail() = (%+v, %v)", created, err)
	}
	if _, err := store.GetUserByID(ctx, "missing-"+suffix); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("GetUserByID(missing) = %v", err)
	}
	credential := coreauth.PasskeyCredential{
		ID: uuid.NewString(), UserID: userID, CredentialID: []byte("scythe-credential-" + suffix),
		PublicKey: []byte("public-key"), SignCount: 0,
		VerifierCredential: []byte(`{"id":"scythe"}`), CreatedAt: now,
	}
	if err := store.SaveCredential(ctx, credential); err != nil {
		t.Fatalf("SaveCredential() = %v", err)
	}
	loadedCredential, err := store.GetCredentialByCredentialID(ctx, credential.CredentialID)
	if err != nil || loadedCredential.ID != credential.ID || loadedCredential.SignCount != 0 {
		t.Fatalf("GetCredentialByCredentialID() = (%+v, %v)", loadedCredential, err)
	}
	listedCredentials, err := store.ListCredentialsByUser(ctx, userID)
	if err != nil || len(listedCredentials) != 1 || listedCredentials[0].ID != credential.ID {
		t.Fatalf("ListCredentialsByUser() = (%+v, %v)", listedCredentials, err)
	}
	if err := store.UseCredential(ctx, credential.CredentialID, 0, 1, []byte(`{"id":"scythe","signCount":1}`), now.Add(time.Second)); err != nil {
		t.Fatalf("UseCredential() = %v", err)
	}
	loadedCredential, err = store.GetCredentialByCredentialID(ctx, credential.CredentialID)
	if err != nil || loadedCredential.SignCount != 1 || loadedCredential.LastUsedAt == nil {
		t.Fatalf("GetCredentialByCredentialID(updated) = (%+v, %v)", loadedCredential, err)
	}
	session := coreauth.Session{ID: uuid.NewString(), UserID: userID, TokenHash: coreauth.Digest(sha256.Sum256([]byte("scythe-session-" + suffix))), CreatedAt: now, ExpiresAt: now.Add(time.Hour), IPAddress: "203.0.113.7", UserAgent: "scythe-test"}
	if err := store.SaveSession(ctx, session); err != nil {
		t.Fatalf("SaveSession() = %v", err)
	}
	used, err := store.UseSession(ctx, session.TokenHash, now.Add(time.Second))
	if err != nil || used.ID != session.ID || used.LastSeenAt == nil {
		t.Fatalf("UseSession() = (%+v, %v)", used, err)
	}
	gotSession, err := store.GetSession(ctx, session.TokenHash)
	if err != nil || gotSession.ID != session.ID {
		t.Fatalf("GetSession() = (%+v, %v)", gotSession, err)
	}
	listedSessions, err := store.ListSessionsByUser(ctx, userID)
	if err != nil || len(listedSessions) != 1 {
		t.Fatalf("ListSessionsByUser() = (%+v, %v)", listedSessions, err)
	}
	if err := store.RevokeSession(ctx, session.TokenHash, now.Add(2*time.Second)); err != nil {
		t.Fatalf("RevokeSession() = %v", err)
	}
	if _, err := store.UseSession(ctx, session.TokenHash, now.Add(3*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("UseSession(revoked) = %v", err)
	}
	ceremony := coreauth.PasskeyCeremony{ID: uuid.NewString(), Kind: coreauth.CeremonyRegistration, Challenge: "scythe-challenge-" + suffix, UserID: userID, VerifierSession: []byte(`{"challenge":"scythe"}`), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.SaveCeremony(ctx, ceremony); err != nil {
		t.Fatalf("SaveCeremony() = %v", err)
	}
	consumedCeremony, err := store.ConsumeCeremony(ctx, ceremony.ID, ceremony.Kind, now.Add(time.Second))
	if err != nil || consumedCeremony.ID != ceremony.ID || consumedCeremony.ConsumedAt == nil {
		t.Fatalf("ConsumeCeremony() = (%+v, %v)", consumedCeremony, err)
	}
	if _, err := store.ConsumeCeremony(ctx, ceremony.ID, ceremony.Kind, now.Add(2*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("ConsumeCeremony(replayed) = %v", err)
	}
	link := coreauth.MagicLink{ID: uuid.NewString(), Email: email, TokenHash: coreauth.Digest(sha256.Sum256([]byte("scythe-link-" + suffix))), Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.SaveMagicLink(ctx, link); err != nil {
		t.Fatalf("SaveMagicLink() = %v", err)
	}
	consumedLink, err := store.ConsumeMagicLink(ctx, link.TokenHash, link.Purpose, now.Add(time.Second))
	if err != nil || consumedLink.ID != link.ID || consumedLink.ConsumedAt == nil {
		t.Fatalf("ConsumeMagicLink() = (%+v, %v)", consumedLink, err)
	}
	if _, err := store.ConsumeMagicLink(ctx, link.TokenHash, link.Purpose, now.Add(2*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("ConsumeMagicLink(replayed) = %v", err)
	}
	delivery := coreauth.MagicLinkDelivery{ID: uuid.NewString(), RecipientEmail: "scythe-delivery-" + suffix + "@example.invalid", RedirectURI: "https://example.invalid/auth", Purpose: coreauth.MagicLinkPurposeSignIn, Status: coreauth.MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM magic_link_mail_outbox WHERE id = $1::UUID`, delivery.ID)
	})
	if err := store.SaveMagicLinkDeliveryIntent(ctx, delivery); err != nil {
		t.Fatalf("SaveMagicLinkDeliveryIntent() = %v", err)
	}
	claimed, err := store.ClaimMagicLinkDeliveries(ctx, now.Add(time.Second), time.Minute, 1, 3)
	if err != nil || len(claimed) != 1 || claimed[0].ID != delivery.ID || claimed[0].ClaimToken == "" {
		t.Fatalf("ClaimMagicLinkDeliveries() = (%+v, %v)", claimed, err)
	}
	deliveryLink := coreauth.MagicLink{ID: delivery.ID, Email: delivery.RecipientEmail, TokenHash: coreauth.Digest(sha256.Sum256([]byte("scythe-delivery-link-" + suffix))), Purpose: delivery.Purpose, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.ActivateMagicLinkDelivery(ctx, delivery.ID, claimed[0].ClaimToken, deliveryLink); err != nil {
		t.Fatalf("ActivateMagicLinkDelivery() = %v", err)
	}
	if err := store.CompleteMagicLinkDelivery(ctx, delivery.ID, claimed[0].ClaimToken, now.Add(2*time.Second)); err != nil {
		t.Fatalf("CompleteMagicLinkDelivery() = %v", err)
	}
	consumedDeliveryLink, err := store.ConsumeMagicLink(ctx, deliveryLink.TokenHash, deliveryLink.Purpose, now.Add(3*time.Second))
	if err != nil || consumedDeliveryLink.ID != delivery.ID {
		t.Fatalf("ConsumeMagicLink(delivery) = (%+v, %v)", consumedDeliveryLink, err)
	}
	if err := store.CheckMagicLinkDeliverySchema(ctx); err != nil {
		t.Fatalf("CheckMagicLinkDeliverySchema() = %v", err)
	}
	retryDelivery := delivery
	retryDelivery.ID = uuid.NewString()
	retryDelivery.RecipientEmail = "scythe-retry-" + suffix + "@example.invalid"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM magic_link_mail_outbox WHERE id = $1::UUID`, retryDelivery.ID)
	})
	if err := store.SaveMagicLinkDeliveryIntent(ctx, retryDelivery); err != nil {
		t.Fatalf("SaveMagicLinkDeliveryIntent(retry) = %v", err)
	}
	retryClaimed, err := store.ClaimMagicLinkDeliveries(ctx, now.Add(4*time.Second), time.Minute, 1, 1)
	if err != nil || len(retryClaimed) != 1 || retryClaimed[0].ID != retryDelivery.ID {
		t.Fatalf("ClaimMagicLinkDeliveries(retry) = (%+v, %v)", retryClaimed, err)
	}
	status, err := store.RetryMagicLinkDelivery(ctx, retryDelivery.ID, retryClaimed[0].ClaimToken, now.Add(5*time.Second), now.Add(6*time.Second), 1)
	if err != nil || status != coreauth.MagicLinkDeliveryDead {
		t.Fatalf("RetryMagicLinkDelivery() = (%s, %v)", status, err)
	}
}
