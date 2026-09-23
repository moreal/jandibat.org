//go:build integration

package observability

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGXMetricsAndProbesAgainstCockroach(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to an isolated migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry(Resource{Environment: "test"})
	registry.RegisterPGXPool(pool)
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if body := scrape(t, registry); !strings.Contains(body, `db_pool_in_use{build_sha="unknown",environment="test",region="unknown"} 1`) {
		t.Fatalf("acquired pgx connection must raise the existing gauge:\n%s", body)
	}
	connection.Release()
	if body := scrape(t, registry); !strings.Contains(body, `db_pool_in_use{build_sha="unknown",environment="test",region="unknown"} 0`) {
		t.Fatalf("released pgx connection must lower the existing gauge:\n%s", body)
	}

	queueAge, err := PGXQueueAgeProbe(pool)(ctx)
	if err != nil || queueAge < 0 {
		t.Fatalf("queue age = %v, %v", queueAge, err)
	}
	deletionAge, err := PGXDeletionAgeProbe(pool)(ctx)
	if err != nil || deletionAge < 0 {
		t.Fatalf("deletion age = %v, %v", deletionAge, err)
	}
	samples, err := PGXActivityFreshnessProbe(pool)(ctx)
	if err != nil {
		t.Fatalf("activity freshness: %v", err)
	}
	for _, sample := range samples {
		if sample.Provider == "" || sample.Visibility == "" || sample.Seconds < 0 {
			t.Fatalf("invalid bounded freshness sample: %+v", sample)
		}
	}
	count, oldestAge, err := PGXRevocationDLQProbe(pool)(ctx)
	if err != nil || count < 0 || oldestAge < 0 {
		t.Fatalf("revocation DLQ = %v, %v, %v", count, oldestAge, err)
	}
}
