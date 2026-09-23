package cockroach

import (
	"context"
	"strings"
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

func TestRetentionFactPurgeReturnsOnlyDeletedSnapshotKeys(t *testing.T) {
	query, err := buildRetentionPurgeQuery(operations.RetentionActivityFacts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "RETURNING subject_id, environment_id, activity_date") {
		t.Fatalf("fact purge cannot identify actual deleted snapshot keys: %s", query)
	}
	other, err := buildRetentionPurgeQuery(operations.RetentionSessions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, "RETURNING") {
		t.Fatalf("session purge unexpectedly requests snapshot keys: %s", other)
	}
}
