package cockroach

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// A SQL-only Store must not silently run maintenance deletion or preview
// outside the pgx transaction/role boundary.
func TestRetentionRequiresPGXPoolWithoutUsingSQLFallback(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"count"}, Rows: [][]driver.Value{{int64(1)}}},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	request := operations.RetentionPurgeRequest{
		Dataset: operations.RetentionAuditEvents, Before: asOf.Add(-time.Hour), AsOf: asOf, Limit: 1,
	}
	if deleted, err := store.PurgeExpired(context.Background(), request); deleted != 0 || err == nil {
		t.Fatalf("PurgeExpired() = %d, %v, want 0 and pgx pool required", deleted, err)
	}
	if count, err := store.CountExpired(context.Background(), request); count != 0 || err == nil {
		t.Fatalf("CountExpired() = %d, %v, want 0 and pgx pool required", count, err)
	}
	if calls := script.Calls(); len(calls) != 0 {
		t.Fatalf("retention used SQL fallback: %#v", calls)
	}
}
