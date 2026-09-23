//go:build integration

package cockroach

import (
	"context"
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
	if dsn == "" { t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB") }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil { t.Fatal(err) }
	defer pool.Close()
	store, err := New(pool)
	if err != nil { t.Fatal(err) }

	suffix := uuid.NewString()
	userID := "scythe-auth-user-" + suffix
	email := fmt.Sprintf("%s@example.invalid", userID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, primary_email, status, created_at, updated_at) VALUES ($1, $2, 'active', $3, $3)`, userID, email, now); err != nil { t.Fatal(err) }
	t.Cleanup(func() { cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second); defer cleanupCancel(); _, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID) })
	got, err := store.GetUserByID(ctx, userID)
	if err != nil || got.ID != userID || got.PrimaryEmail != email || got.Status != coreauth.UserStatusActive { t.Fatalf("GetUserByID() = (%+v, %v)", got, err) }
}
