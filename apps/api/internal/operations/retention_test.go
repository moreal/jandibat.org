package operations

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRetentionWorkerBatchesAndUsesSingleCutoff(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	store := &retentionStoreStub{responses: map[RetentionDataset][]purgeResponse{
		RetentionAuditEvents:                  {{deleted: 2}, {deleted: 1}},
		RetentionActivityFacts:                nil,
		RetentionCustomActivities:             nil,
		RetentionRevokedProviderMetadata:      nil,
		RetentionOrphanedProviderEnvironments: nil,
		RetentionSuccessfulSyncJobs:           nil,
		RetentionFailedSyncJobs:               nil,
		RetentionSessions:                     {{deleted: 0}},
		RetentionMagicLinks:                   nil,
		RetentionMagicLinkMailOutbox:          nil,
		RetentionMutationAuditOutbox:          nil,
		RetentionAuthChallenges:               nil,
		RetentionIdempotencyKeys:              nil,
		RetentionTimelineCache:                nil,
		RetentionActivityRefresh:              nil,
		RetentionRateLimitBuckets:             nil,
		RetentionDeletedIdentityTombstones:    nil,
		RetentionDeletedIdentityTombstonesV2:  nil,
		RetentionDeletionRequests:             nil,
		RetentionDeletionRequestInbox:         nil,
	}}
	worker, err := NewRetentionWorker(store, retentionClock{now}, RetentionConfig{
		Rules: []RetentionRule{
			{Dataset: RetentionAuditEvents, RetainFor: 30 * 24 * time.Hour},
			{Dataset: RetentionSessions, RetainFor: 24 * time.Hour},
		},
		BatchSize: 2, MaxBatchesPerDataset: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted[RetentionAuditEvents] != 3 || result.Deleted[RetentionSessions] != 0 {
		t.Fatalf("deleted = %#v", result.Deleted)
	}
	wantAuditCutoff := now.UTC().Add(-30 * 24 * time.Hour)
	if len(store.requests) != 3 || !store.requests[0].Before.Equal(wantAuditCutoff) || !store.requests[1].Before.Equal(wantAuditCutoff) {
		t.Fatalf("requests = %#v", store.requests)
	}
}

func TestRetentionWorkerIsolatesDatasetFailureAndBoundsWork(t *testing.T) {
	store := &retentionStoreStub{responses: map[RetentionDataset][]purgeResponse{
		RetentionAuditEvents:                  {{err: errors.New("audit unavailable")}},
		RetentionActivityFacts:                nil,
		RetentionCustomActivities:             nil,
		RetentionRevokedProviderMetadata:      nil,
		RetentionOrphanedProviderEnvironments: nil,
		RetentionSuccessfulSyncJobs:           nil,
		RetentionFailedSyncJobs:               nil,
		RetentionSessions:                     {{deleted: 2}, {deleted: 2}},
		RetentionMagicLinks:                   {{deleted: 1}},
		RetentionMagicLinkMailOutbox:          nil,
		RetentionMutationAuditOutbox:          nil,
		RetentionAuthChallenges:               nil,
		RetentionIdempotencyKeys:              nil,
		RetentionTimelineCache:                nil,
		RetentionActivityRefresh:              nil,
		RetentionRateLimitBuckets:             nil,
		RetentionDeletedIdentityTombstones:    nil,
		RetentionDeletedIdentityTombstonesV2:  nil,
		RetentionDeletionRequests:             nil,
		RetentionDeletionRequestInbox:         nil,
	}}
	worker, _ := NewRetentionWorker(store, retentionClock{time.Now()}, RetentionConfig{
		Rules: []RetentionRule{
			{Dataset: RetentionAuditEvents}, {Dataset: RetentionSessions}, {Dataset: RetentionMagicLinks},
		}, BatchSize: 2, MaxBatchesPerDataset: 2,
	})
	result, err := worker.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil")
	}
	if !reflect.DeepEqual(result.Failed, []RetentionDataset{RetentionAuditEvents}) ||
		!reflect.DeepEqual(result.Truncated, []RetentionDataset{RetentionSessions}) ||
		result.Deleted[RetentionMagicLinks] != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRetentionWorkerRejectsUnsafeConfig(t *testing.T) {
	store := &retentionStoreStub{}
	tests := []RetentionConfig{
		{},
		{Rules: []RetentionRule{{Dataset: "unknown"}}, BatchSize: 1, MaxBatchesPerDataset: 1},
		{Rules: []RetentionRule{{Dataset: RetentionAuditEvents, RetainFor: -1}}, BatchSize: 1, MaxBatchesPerDataset: 1},
		{Rules: []RetentionRule{{Dataset: RetentionAuditEvents}, {Dataset: RetentionAuditEvents}}, BatchSize: 1, MaxBatchesPerDataset: 1},
	}
	for _, config := range tests {
		if _, err := NewRetentionWorker(store, retentionClock{time.Now()}, config); !errors.Is(err, ErrInvalidRetentionConfig) {
			t.Fatalf("NewRetentionWorker(%#v) error = %v", config, err)
		}
	}
}

func TestRetentionOperatorDryRunDoesNotDeleteOrCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store := &retentionStoreStub{counts: map[RetentionDataset]int64{
		RetentionAuditEvents:                  7,
		RetentionActivityFacts:                0,
		RetentionCustomActivities:             0,
		RetentionRevokedProviderMetadata:      0,
		RetentionOrphanedProviderEnvironments: 0,
		RetentionSuccessfulSyncJobs:           0,
		RetentionFailedSyncJobs:               0,
		RetentionSessions:                     0,
		RetentionMagicLinks:                   0,
		RetentionMagicLinkMailOutbox:          0,
		RetentionMutationAuditOutbox:          0,
		RetentionAuthChallenges:               0,
		RetentionIdempotencyKeys:              0,
		RetentionTimelineCache:                0,
		RetentionActivityRefresh:              0,
		RetentionRateLimitBuckets:             0,
		RetentionDeletedIdentityTombstones:    0,
		RetentionDeletedIdentityTombstonesV2:  0,
		RetentionDeletionRequests:             0,
		RetentionDeletionRequestInbox:         0,
	}}
	worker, _ := NewRetentionWorker(store, retentionClock{now}, RetentionConfig{
		Rules: []RetentionRule{{Dataset: RetentionAuditEvents, RetainFor: 400 * 24 * time.Hour}}, BatchSize: 2, MaxBatchesPerDataset: 2,
	})
	checkpoints := newMemoryCheckpointStore()
	result, err := worker.RunOperator(context.Background(), checkpoints, RetentionOperatorOptions{AsOf: now, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched[RetentionAuditEvents] != 7 || result.Deleted[RetentionAuditEvents] != 0 || len(store.requests) != 0 || len(checkpoints.values) != 0 {
		t.Fatalf("result=%#v requests=%#v checkpoints=%#v", result, store.requests, checkpoints.values)
	}
	if len(store.countRequests) != 1 || !store.countRequests[0].AsOf.Equal(now) ||
		!store.countRequests[0].Before.Equal(now.Add(-400*24*time.Hour)) {
		t.Fatalf("count requests = %#v", store.countRequests)
	}
}

func TestRetentionOperatorResumesFromDurableRuleCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store := &retentionStoreStub{responses: map[RetentionDataset][]purgeResponse{
		RetentionAuditEvents:                  {{deleted: 2}, {err: errors.New("stopped")}, {deleted: 1}},
		RetentionActivityFacts:                nil,
		RetentionCustomActivities:             nil,
		RetentionRevokedProviderMetadata:      nil,
		RetentionOrphanedProviderEnvironments: nil,
		RetentionSuccessfulSyncJobs:           nil,
		RetentionFailedSyncJobs:               nil,
		RetentionSessions:                     {{deleted: 0}},
		RetentionMagicLinks:                   nil,
		RetentionMagicLinkMailOutbox:          nil,
		RetentionMutationAuditOutbox:          nil,
		RetentionAuthChallenges:               nil,
		RetentionIdempotencyKeys:              nil,
		RetentionTimelineCache:                nil,
		RetentionActivityRefresh:              nil,
		RetentionRateLimitBuckets:             nil,
		RetentionDeletedIdentityTombstones:    nil,
		RetentionDeletedIdentityTombstonesV2:  nil,
		RetentionDeletionRequests:             nil,
		RetentionDeletionRequestInbox:         nil,
	}}
	worker, _ := NewRetentionWorker(store, retentionClock{now}, RetentionConfig{
		Rules: []RetentionRule{{Dataset: RetentionAuditEvents}, {Dataset: RetentionSessions}}, BatchSize: 2, MaxBatchesPerDataset: 4,
	})
	checkpoints := newMemoryCheckpointStore()
	result, err := worker.RunOperator(context.Background(), checkpoints, RetentionOperatorOptions{AsOf: now, Scope: "release-1"})
	if err == nil || result.Deleted[RetentionAuditEvents] != 2 {
		t.Fatalf("first RunOperator() = %#v, %v", result, err)
	}
	if len(checkpoints.values) != 1 {
		t.Fatalf("checkpoint not persisted: %#v", checkpoints.values)
	}
	result, err = worker.RunOperator(context.Background(), checkpoints, RetentionOperatorOptions{AsOf: now, Scope: "release-1", Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted[RetentionAuditEvents] != 3 || result.Deleted[RetentionSessions] != 0 || len(checkpoints.values) != 0 {
		t.Fatalf("resumed result=%#v checkpoints=%#v", result, checkpoints.values)
	}
}

type retentionClock struct{ now time.Time }

func (clock retentionClock) Now() time.Time { return clock.now }

type purgeResponse struct {
	deleted int64
	err     error
}

type retentionStoreStub struct {
	responses     map[RetentionDataset][]purgeResponse
	requests      []RetentionPurgeRequest
	counts        map[RetentionDataset]int64
	countRequests []RetentionPurgeRequest
}

func (store *retentionStoreStub) PurgeExpired(_ context.Context, request RetentionPurgeRequest) (int64, error) {
	store.requests = append(store.requests, request)
	responses := store.responses[request.Dataset]
	if len(responses) == 0 {
		return 0, nil
	}
	response := responses[0]
	store.responses[request.Dataset] = responses[1:]
	return response.deleted, response.err
}

func (store *retentionStoreStub) CountExpired(_ context.Context, request RetentionPurgeRequest) (int64, error) {
	store.countRequests = append(store.countRequests, request)
	return store.counts[request.Dataset], nil
}
