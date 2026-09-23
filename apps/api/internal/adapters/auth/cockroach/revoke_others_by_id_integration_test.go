//go:build integration

package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestCockroachRevokeOtherSessionsExceptIDAtomicOwnerCheck(t *testing.T) {
	adminDSN, apiDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL"), os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set isolated JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { api.Close(); admin.Close() })
	store, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	owner, foreign := "revoke-by-id-"+uuid.NewString(), "revoke-foreign-"+uuid.NewString()
	for _, id := range []string{owner, foreign} {
		if _, err := admin.Exec(ctx, `INSERT INTO users (id,primary_email,status) VALUES ($1,$2,'active')`, id, id+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, owner, foreign)
	})
	now := time.Now().UTC().Truncate(time.Microsecond)
	current := coreauth.Session{ID: uuid.NewString(), UserID: owner, TokenHash: coreauth.Digest(sha256.Sum256([]byte(owner + "current"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	other := coreauth.Session{ID: uuid.NewString(), UserID: owner, TokenHash: coreauth.Digest(sha256.Sum256([]byte(owner + "other"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	foreignSession := coreauth.Session{ID: uuid.NewString(), UserID: foreign, TokenHash: coreauth.Digest(sha256.Sum256([]byte(foreign))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	for _, session := range []coreauth.Session{current, other, foreignSession} {
		if err := store.SaveSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ userID, sessionID string }{{owner, uuid.NewString()}, {foreign, current.ID}} {
		if err := store.RevokeOtherSessionsExceptID(ctx, tc.userID, tc.sessionID, now); !errors.Is(err, coreauth.ErrNotFound) {
			t.Fatalf("untrusted current error=%v, want ErrNotFound", err)
		}
	}
	assertRevoked := func(session coreauth.Session, want bool) {
		t.Helper()
		got, err := store.GetSessionByID(ctx, session.UserID, session.ID)
		if err != nil || (got.RevokedAt != nil) != want || got.TokenHash != (coreauth.Digest{}) {
			t.Fatalf("session %s revoked=%t got=%+v err=%v", session.ID, want, got, err)
		}
	}
	assertRevoked(current, false)
	assertRevoked(other, false)
	assertRevoked(foreignSession, false)
	if err := store.RevokeOtherSessionsExceptID(ctx, owner, current.ID, now); err != nil {
		t.Fatal(err)
	}
	assertRevoked(current, false)
	assertRevoked(other, true)
	assertRevoked(foreignSession, false)
}

func TestCockroachRevokeOtherSessionsExceptIDRacesCurrentRevocation(t *testing.T) {
	adminDSN, apiDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL"), os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set isolated JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { api.Close(); admin.Close() })
	store, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	owner := "revoke-race-" + uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO users (id,primary_email,status) VALUES ($1,$2,'active')`, owner, owner+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	current := coreauth.Session{ID: uuid.NewString(), UserID: owner, TokenHash: coreauth.Digest(sha256.Sum256([]byte(owner + "current"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	other := coreauth.Session{ID: uuid.NewString(), UserID: owner, TokenHash: coreauth.Digest(sha256.Sum256([]byte(owner + "other"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	for _, session := range []coreauth.Session{current, other} {
		if err := store.SaveSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at=$2::TIMESTAMPTZ WHERE id=$1::UUID`, current.ID, now); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- store.RevokeOtherSessionsExceptID(ctx, owner, current.ID, now)
	}()
	<-started
	// Wait for the revocation transaction to acquire its own connection
	// while the admin transaction still holds the current-session row lock.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for api.Stat().AcquiredConns() == 0 {
		select {
		case err := <-result:
			t.Fatalf("revoke finished before current-row lock release: %v", err)
		case <-ticker.C:
		case <-deadline:
			t.Fatal("revoke transaction did not acquire a connection")
		}
	}
	select {
	case err := <-result:
		t.Fatalf("revoke finished before current-row lock release: %v", err)
	default:
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("concurrent revoked current error=%v, want ErrNotFound", err)
	}
	got, err := store.GetSessionByID(ctx, owner, other.ID)
	if err != nil || got.RevokedAt != nil {
		t.Fatalf("failed concurrent request revoked other session: %+v, %v", got, err)
	}
}
