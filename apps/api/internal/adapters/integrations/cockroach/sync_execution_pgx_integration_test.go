package cockroach_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

func TestCockroachSyncExecutionUsesPGXAndRollsBackLease(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	suffix := integrationSuffix(t)
	userID := "it_execution_user_" + suffix
	subjectID := "it_execution_subject_" + suffix
	environmentID := "it_execution_environment_" + suffix
	connectionID := "4d9ebd00-c2c0-43b7-a341-" + suffix
	if _, err := admin.Exec(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM provider_connections WHERE id=$1`, connectionID)
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM environments WHERE id=$1`, environmentID)
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM subjects WHERE id=$1`, subjectID)
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, userID)
	})
	if _, err := admin.Exec(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public) VALUES ($1, $2, $3, 'UTC', true)`, subjectID, userID, "it-execution-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $1, 'Execution integration', 'subject', $2)`, environmentID, subjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status) VALUES ($1, $2, $3, 'token', 'active')`, connectionID, subjectID, environmentID); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.User("jandibat_api")
	apiDSN := parsed.String()
	pool, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	legacy, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	store, err := integrationstore.NewWithPGXPool(legacy, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	rollback := errors.New("rollback sync execution lease")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		claim, acquired, err := store.TryAcquireSyncExecution(txctx, connectionID)
		if err != nil || !acquired || claim == "" {
			return fmt.Errorf("transactional claim = (%q, %t, %v)", claim, acquired, err)
		}
		if err := store.ReleaseSyncExecution(txctx, connectionID, "wrong-token"); err != nil {
			return err
		}
		if _, acquired, err := store.TryAcquireSyncExecution(txctx, connectionID); err != nil || acquired {
			return fmt.Errorf("fenced duplicate = (%t, %v)", acquired, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback transaction = %v", err)
	}
	claim, acquired, err := store.TryAcquireSyncExecution(ctx, connectionID)
	if err != nil || !acquired || claim == "" {
		t.Fatalf("claim after rollback = (%q, %t, %v)", claim, acquired, err)
	}
	if err := store.ReleaseSyncExecution(ctx, connectionID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE provider_connections SET sync_cursor=jsonb_build_object('connection_status', 'revoked') WHERE id=$1`, connectionID); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := store.TryAcquireSyncExecution(ctx, connectionID); err != nil || acquired {
		t.Fatalf("revoked connection claim = (%t, %v)", acquired, err)
	}
}
