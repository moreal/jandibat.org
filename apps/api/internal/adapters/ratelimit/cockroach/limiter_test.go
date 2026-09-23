package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
)

type rateLimitCall struct {
	query string
	args  []any
}

type rateLimitExecutor struct {
	count *int64
	calls []rateLimitCall
}

func (*rateLimitExecutor) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (*rateLimitExecutor) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (*rateLimitExecutor) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected Begin")
}

func (executor *rateLimitExecutor) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	executor.calls = append(executor.calls, rateLimitCall{query: query, args: append([]any(nil), args...)})
	return rateLimitRow{count: executor.count}
}

type rateLimitRow struct{ count *int64 }

func (row rateLimitRow) Scan(dest ...any) error {
	if row.count == nil {
		return pgx.ErrNoRows
	}
	*dest[0].(*int64) = *row.count
	return nil
}

func TestLimiterAtomicallyHashesAndIncrementsBucket(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 34, 56, 0, time.UTC)
	count := int64(1)
	executor := &rateLimitExecutor{count: &count}
	limiter, err := New(executor, map[string]handlers.RateLimitPolicy{"sync": {Limit: 10, Window: time.Minute}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	allowed, retryAfter, err := limiter.Allow(context.Background(), "sync", "192.0.2.1")
	if err != nil || !allowed || retryAfter != 0 {
		t.Fatalf("Allow() = %t, %s, %v", allowed, retryAfter, err)
	}
	calls := executor.calls
	if len(calls) != 1 || len(calls[0].args) != 5 {
		t.Fatalf("calls = %#v", calls)
	}
	wantHash := sha256.Sum256([]byte("192.0.2.1"))
	if !reflect.DeepEqual(calls[0].args[1], wantHash[:]) || calls[0].args[1] == "192.0.2.1" {
		t.Fatalf("persisted key = %#v", calls[0].args[1])
	}
	if got := calls[0].args[2]; got != now.Truncate(time.Minute) {
		t.Fatalf("window start = %v", got)
	}
	for _, fragment := range []string{"ON CONFLICT", "count = api_rate_limit_buckets.count + 1", "count < $5", "RETURNING count"} {
		if !strings.Contains(calls[0].query, fragment) {
			t.Errorf("atomic query missing %q: %s", fragment, calls[0].query)
		}
	}
}

func TestLimiterReturnsWindowExpiryWhenLimitReached(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 34, 30, 0, time.UTC)
	executor := &rateLimitExecutor{}
	limiter, _ := New(executor, map[string]handlers.RateLimitPolicy{"sync": {Limit: 1, Window: time.Minute}}, func() time.Time { return now })
	allowed, retryAfter, err := limiter.Allow(context.Background(), "sync", "same-key")
	if err != nil || allowed || retryAfter != 30*time.Second {
		t.Fatalf("Allow() = %t, %s, %v", allowed, retryAfter, err)
	}
}

func TestLimiterUnknownScopeBypassesDatabaseAndRejectsBadConfig(t *testing.T) {
	executor := &rateLimitExecutor{}
	limiter, _ := New(executor, map[string]handlers.RateLimitPolicy{"known": {Limit: 1, Window: time.Minute}}, nil)
	allowed, _, err := limiter.Allow(context.Background(), "unknown", "key")
	if err != nil || !allowed || len(executor.calls) != 0 {
		t.Fatalf("unknown Allow() = %t, %v, calls=%#v", allowed, err, executor.calls)
	}
	if _, err := New(nil, map[string]handlers.RateLimitPolicy{"known": {Limit: 1, Window: time.Minute}}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New(nil) error = %v", err)
	}
	var nilPool *pgxpool.Pool
	if _, err := New(nilPool, map[string]handlers.RateLimitPolicy{"known": {Limit: 1, Window: time.Minute}}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New(typed nil) error = %v", err)
	}
}
