package cockroach

import (
	"context"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// An unconfigured Store must not run maintenance deletion or preview.
func TestRetentionRequiresPGXPool(t *testing.T) {
	store := &Store{}
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
}
