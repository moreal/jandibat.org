package integrations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSyncJobsPageKeysetTraversalSurvivesDeletedAnchor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service := &SyncService{jobs: store}
	connectionID := "44d8a234-ef20-4301-b859-c9844c6902d5"
	otherConnectionID := "799d9899-4ac6-4ea2-b6ab-19a657241e67"
	base := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	for i, id := range ids {
		createdAt := base
		if i == 3 {
			createdAt = base.Add(time.Second)
		}
		if err := store.SaveSyncJob(ctx, SyncJob{ID: id, ConnectionID: connectionID, CreatedAt: createdAt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSyncJob(ctx, SyncJob{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ConnectionID: otherConnectionID, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	first, err := service.ListJobsPage(ctx, connectionID, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 2 || first.Jobs[0].ID != ids[0] || first.Jobs[1].ID != ids[1] || !first.HasNextPage {
		t.Fatalf("first page = %+v", first)
	}
	// A keyset cursor does not need to fetch its anchor row again.
	store.mu.Lock()
	delete(store.syncJobs, ids[1])
	store.mu.Unlock()
	second, err := service.ListJobsPage(ctx, connectionID, &SyncJobCursor{CreatedAt: base, ID: ids[1]}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 2 || second.Jobs[0].ID != ids[2] || second.Jobs[1].ID != ids[3] || second.HasNextPage {
		t.Fatalf("second page after deleted anchor = %+v", second)
	}
	end, err := service.ListJobsPage(ctx, connectionID, &SyncJobCursor{CreatedAt: base.Add(time.Second), ID: ids[3]}, 2)
	if err != nil || len(end.Jobs) != 0 || end.HasNextPage {
		t.Fatalf("empty terminal page = (%+v, %v)", end, err)
	}
}

func TestSyncJobsPageRejectsMalformedTupleAndLimits(t *testing.T) {
	t.Parallel()
	service := &SyncService{jobs: NewMemoryStore()}
	ctx := context.Background()
	connectionID := "44d8a234-ef20-4301-b859-c9844c6902d5"
	for _, limit := range []int{-1, 0, 101} {
		if _, err := service.ListJobsPage(ctx, connectionID, nil, limit); !errors.Is(err, ErrInvalidSyncPageSize) {
			t.Errorf("limit %d error = %v", limit, err)
		}
	}
	for _, tc := range []struct {
		name  string
		id    string
		after *SyncJobCursor
	}{
		{name: "bad parent", id: "invalid"},
		{name: "empty timestamp", id: connectionID, after: &SyncJobCursor{ID: "11111111-1111-4111-8111-111111111111"}},
		{name: "bad cursor ID", id: connectionID, after: &SyncJobCursor{CreatedAt: time.Now(), ID: "invalid"}},
		{name: "noncanonical cursor ID", id: connectionID, after: &SyncJobCursor{CreatedAt: time.Now(), ID: "11111111-1111-4111-8111-AAAAAAAAAAAA"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.ListJobsPage(ctx, tc.id, tc.after, 1); !errors.Is(err, ErrInvalidIdentifier) {
				t.Fatalf("malformed page tuple error = %v", err)
			}
		})
	}
	page, err := service.ListJobsPage(ctx, connectionID, nil, 100)
	if err != nil || len(page.Jobs) != 0 || page.HasNextPage {
		t.Fatalf("maximum limit empty page = (%+v, %v)", page, err)
	}
}

func TestMemorySyncJobsPageUsesStoreBoundaryValidation(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	ctx := context.Background()
	connectionID := "44d8a234-ef20-4301-b859-c9844c6902d5"
	for _, limit := range []int{0, 102} {
		if _, err := store.ListSyncJobsPage(ctx, connectionID, nil, limit); !errors.Is(err, ErrInvalidSyncPageSize) {
			t.Errorf("store limit %d error = %v, want invalid page size", limit, err)
		}
	}
	for _, tc := range []struct {
		name  string
		id    string
		after *SyncJobCursor
	}{
		{name: "noncanonical parent", id: "44D8A234-EF20-4301-B859-C9844C6902D5"},
		{name: "bad parent", id: "invalid"},
		{name: "zero cursor timestamp", id: connectionID, after: &SyncJobCursor{ID: "11111111-1111-4111-8111-111111111111"}},
		{name: "noncanonical cursor", id: connectionID, after: &SyncJobCursor{CreatedAt: time.Now(), ID: "11111111-1111-4111-8111-AAAAAAAAAAAA"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.ListSyncJobsPage(ctx, tc.id, tc.after, 1); !errors.Is(err, ErrInvalidIdentifier) {
				t.Fatalf("store tuple error = %v, want invalid identifier", err)
			}
		})
	}
}
