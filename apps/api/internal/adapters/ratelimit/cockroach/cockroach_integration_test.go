package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// TestCockroachLimiterIsAtomicAndRetentionPurgesExpiredBuckets is opt-in and
// validates the 0004 schema against the real CockroachDB SQL dialect.
func TestCockroachLimiterIsAtomicAndRetentionPurgesExpiredBuckets(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}

	scope := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	rawKey := "192.0.2.44|account@example.invalid"
	keyHash := sha256.Sum256([]byte(rawKey))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM api_rate_limit_buckets WHERE scope = $1`, scope)
	})
	now := time.Now().UTC()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	limiter, err := New(pool, map[string]handlers.RateLimitPolicy{scope: {Limit: 2, Window: time.Minute}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		allowed, retryAfter, err := limiter.Allow(ctx, scope, rawKey)
		if err != nil || !allowed || retryAfter != 0 {
			t.Fatalf("allow attempt %d = %t, %s, %v", attempt, allowed, retryAfter, err)
		}
	}
	allowed, retryAfter, err := limiter.Allow(ctx, scope, rawKey)
	wantRetryAfter := maxDuration(now.Truncate(time.Minute).Add(time.Minute).Sub(now), time.Second)
	if err != nil || allowed || retryAfter != wantRetryAfter {
		t.Fatalf("denied attempt = %t, %s, %v", allowed, retryAfter, err)
	}
	var count int
	var persistedHash []byte
	var expiresAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT count, key_hash, expires_at
FROM api_rate_limit_buckets
WHERE scope = $1`, scope).Scan(&count, &persistedHash, &expiresAt); err != nil {
		t.Fatalf("load bucket: %v", err)
	}
	if count != 2 || string(persistedHash) != string(keyHash[:]) || string(persistedHash) == rawKey {
		t.Fatalf("persisted bucket count=%d hash=%x", count, persistedHash)
	}

	retention, err := operationsstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := retention.PurgeExpired(ctx, operations.RetentionPurgeRequest{
		Dataset: operations.RetentionRateLimitBuckets, Before: expiresAt.Add(time.Second), Limit: 10,
	})
	if err != nil || deleted != 1 {
		t.Fatalf("purge bucket = %d, %v", deleted, err)
	}
}
