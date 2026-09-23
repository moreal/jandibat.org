//go:build integration

package cockroach

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func TestCustomIngestFactSinkRequiresMatchingActiveTransaction(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated isolated CockroachDB")
	}
	ctx, done := context.WithTimeout(context.Background(), 20*time.Second)
	defer done()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	otherPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	transactional, ok := any(store).(interface {
		SaveFactsInCurrentTransaction(context.Context, activity.SaveFactsInput) error
	})
	if !ok {
		t.Fatal("Cockroach activity store lacks a transaction-bound custom ingest fact writer")
	}
	environmentID := "it-tx-facts-env-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO environments (id, key, name, scope) VALUES ($1, $1, 'Transaction-bound facts', 'global')`, environmentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM environments WHERE id = $1`, environmentID)
	})
	subjectID := activity.SubjectID("it-tx-facts-" + uuid.NewString())
	input := activity.SaveFactsInput{Subject: subjectID, Facts: []activity.Fact{{
		Subject: subjectID, EnvironmentID: activity.EnvironmentID(environmentID),
		Date:   activity.Date(time.Now().UTC().Format(time.DateOnly)),
		Action: activity.ActionCustom, Metric: activity.Metric{Name: activity.MetricCount, Value: 1},
		Metadata: map[string]string{"source": "transaction-test"},
	}}}
	if err := transactional.SaveFactsInCurrentTransaction(ctx, input); err == nil {
		t.Fatal("fact sink accepted a missing active transaction")
	}
	rollbackProbe := errors.New("rollback transaction-bound fact probe")
	err = appdb.InTx(ctx, otherPool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := transactional.SaveFactsInCurrentTransaction(txctx, input); err == nil {
			return errors.New("fact sink accepted another pool's active transaction")
		}
		return rollbackProbe
	})
	if !errors.Is(err, rollbackProbe) {
		t.Fatalf("different-pool probe = %v", err)
	}
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := transactional.SaveFactsInCurrentTransaction(txctx, input); err != nil {
			return err
		}
		var visible int
		if err := tx.QueryRow(txctx, `SELECT count(*) FROM activity_facts WHERE subject_id = $1`, string(subjectID)).Scan(&visible); err != nil || visible != 1 {
			return fmt.Errorf("fact inside matching transaction = (%d, %v)", visible, err)
		}
		return rollbackProbe
	})
	if !errors.Is(err, rollbackProbe) {
		t.Fatalf("same-pool rollback probe = %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_facts WHERE subject_id = $1`, string(subjectID)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fact escaped rollback = (%d, %v)", count, err)
	}
}
