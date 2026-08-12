package cockroach

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

// Authentication can complete just before account deletion. ProvisionUser
// must treat that auth.User as stale and must never recreate the auth-owned
// users row after deletion commits.
func TestCockroachProvisionUserCannotResurrectDeletedAuthUser(t *testing.T) {
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

	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	userID := "it_stale_subject_user_" + suffix
	email := userID + "@example.invalid"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $3, $3)`, userID, email, now); err != nil {
		t.Fatalf("insert auth user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	stale := subjects.User{
		ID: userID, PrimaryEmail: email, Status: subjects.UserStatusActive,
		EmailVerifiedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("commit simulated account deletion: %v", err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := subjects.NewService(store, subjects.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProvisionUser(ctx, stale); !errors.Is(err, subjects.ErrForbidden) {
		t.Fatalf("ProvisionUser(stale auth result) error = %v, want ErrForbidden", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = $1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale auth user was resurrected: count=%d", count)
	}
}
