package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
)

func TestLimiterAtomicallyHashesAndIncrementsBucket(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 34, 56, 0, time.UTC)
	script := fakedb.New(fakedb.Step{Operation: fakedb.Query, Columns: []string{"count"}, Rows: [][]driver.Value{{int64(1)}}})
	db := script.Open()
	defer db.Close()
	limiter, err := New(db, map[string]handlers.RateLimitPolicy{"sync": {Limit: 10, Window: time.Minute}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	allowed, retryAfter, err := limiter.Allow(context.Background(), "sync", "192.0.2.1")
	if err != nil || !allowed || retryAfter != 0 {
		t.Fatalf("Allow() = %t, %s, %v", allowed, retryAfter, err)
	}
	calls := script.Calls()
	if len(calls) != 1 || len(calls[0].Args) != 5 {
		t.Fatalf("calls = %#v", calls)
	}
	wantHash := sha256.Sum256([]byte("192.0.2.1"))
	if !reflect.DeepEqual(calls[0].Args[1].Value, wantHash[:]) || calls[0].Args[1].Value == "192.0.2.1" {
		t.Fatalf("persisted key = %#v", calls[0].Args[1].Value)
	}
	if got := calls[0].Args[2].Value; got != now.Truncate(time.Minute) {
		t.Fatalf("window start = %v", got)
	}
	for _, fragment := range []string{"ON CONFLICT", "count = api_rate_limit_buckets.count + 1", "count < $5", "RETURNING count"} {
		if !strings.Contains(calls[0].Query, fragment) {
			t.Errorf("atomic query missing %q: %s", fragment, calls[0].Query)
		}
	}
}

func TestLimiterReturnsWindowExpiryWhenLimitReached(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 34, 30, 0, time.UTC)
	script := fakedb.New(fakedb.Step{Operation: fakedb.Query, Columns: []string{"count"}})
	db := script.Open()
	defer db.Close()
	limiter, _ := New(db, map[string]handlers.RateLimitPolicy{"sync": {Limit: 1, Window: time.Minute}}, func() time.Time { return now })
	allowed, retryAfter, err := limiter.Allow(context.Background(), "sync", "same-key")
	if err != nil || allowed || retryAfter != 30*time.Second {
		t.Fatalf("Allow() = %t, %s, %v", allowed, retryAfter, err)
	}
}

func TestLimiterUnknownScopeBypassesDatabaseAndRejectsBadConfig(t *testing.T) {
	script := fakedb.New()
	db := script.Open()
	defer db.Close()
	limiter, _ := New(db, map[string]handlers.RateLimitPolicy{"known": {Limit: 1, Window: time.Minute}}, nil)
	allowed, _, err := limiter.Allow(context.Background(), "unknown", "key")
	if err != nil || !allowed || len(script.Calls()) != 0 {
		t.Fatalf("unknown Allow() = %t, %v, calls=%#v", allowed, err, script.Calls())
	}
	if _, err := New(nil, map[string]handlers.RateLimitPolicy{"known": {Limit: 1, Window: time.Minute}}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New(nil) error = %v", err)
	}
}
