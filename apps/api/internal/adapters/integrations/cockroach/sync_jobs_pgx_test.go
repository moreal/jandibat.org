package cockroach

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestSyncJobOperationsRequirePGXPool(t *testing.T) {
	store := &Store{}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	job := integrations.SyncJob{
		ID:           "018f0000-0000-7000-8000-000000000003",
		ConnectionID: "018f0000-0000-7000-8000-000000000001",
		Status:       integrations.SyncJobPending, CreatedAt: now, UpdatedAt: now,
	}
	check := func(name string, got error) {
		t.Helper()
		if !errors.Is(got, ErrNilDB) {
			t.Errorf("%s without pgx pool error = %v, want %v", name, got, ErrNilDB)
		}
	}
	check("SaveSyncJob", store.SaveSyncJob(context.Background(), job))
	_, err := store.GetSyncJob(context.Background(), job.ID)
	check("GetSyncJob", err)
	_, err = store.ListSyncJobs(context.Background(), job.ConnectionID)
	check("ListSyncJobs", err)
	_, err = store.ListSyncJobsPage(context.Background(), job.ConnectionID, nil, 2)
	check("ListSyncJobsPage", err)
	_, _, err = store.GetSyncJobByIdempotencyKey(context.Background(), job.ConnectionID, []byte("key"), now)
	check("GetSyncJobByIdempotencyKey", err)
	_, _, err = store.ClaimSyncJob(context.Background(), job.ID, now, now.Add(time.Minute))
	check("ClaimSyncJob", err)
	_, err = store.ListClaimableSyncJobs(context.Background(), now, 1)
	check("ListClaimableSyncJobs", err)
	check("CompleteClaimedSyncJob", store.CompleteClaimedSyncJob(context.Background(), job, "11111111-1111-4111-8111-111111111111"))
}
