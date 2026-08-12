package integrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type fakeSyncer struct {
	err         error
	calls       int
	token       string
	providerID  string
	lastRequest ProviderSyncRequest
}

type claimedMemoryConnections struct {
	*MemoryStore
	updateClaims []string
}

func (*claimedMemoryConnections) TryAcquireSyncExecution(context.Context, string) (string, bool, error) {
	return "opaque-execution-claim", true, nil
}
func (*claimedMemoryConnections) ReleaseSyncExecution(context.Context, string, string) error {
	return nil
}
func (store *claimedMemoryConnections) UpdateConnectionAfterSync(ctx context.Context, record ConnectionRecord, claimToken string) error {
	store.updateClaims = append(store.updateClaims, claimToken)
	return store.MemoryStore.SaveConnection(ctx, record)
}

type fencedRecordingActivitySink struct {
	called       bool
	connectionID string
	claimToken   string
	facts        activity.SaveFactsInput
}

func (*fencedRecordingActivitySink) SaveEnvironments(context.Context, activity.SaveEnvironmentsInput) error {
	return errors.New("legacy environment write must not be used")
}
func (*fencedRecordingActivitySink) SaveFacts(context.Context, activity.SaveFactsInput) error {
	return errors.New("legacy fact write must not be used")
}
func (sink *fencedRecordingActivitySink) SaveConnectionActivity(_ context.Context, connectionID, claimToken string, _ activity.SaveEnvironmentsInput, facts activity.SaveFactsInput) error {
	sink.called = true
	sink.connectionID = connectionID
	sink.claimToken = claimToken
	sink.facts = facts
	return nil
}

func (syncer *fakeSyncer) ProviderID() string {
	if syncer.providerID != "" {
		return syncer.providerID
	}
	return "github"
}
func (syncer *fakeSyncer) Sync(ctx context.Context, request ProviderSyncRequest) (ProviderSyncResult, error) {
	syncer.calls++
	syncer.lastRequest = request
	token, err := request.Credentials.AccessToken(ctx)
	if err != nil {
		return ProviderSyncResult{}, err
	}
	syncer.token = string(token)
	clearBytes(token)
	if syncer.err != nil {
		return ProviderSyncResult{}, syncer.err
	}
	return ProviderSyncResult{
		Environments: []activity.Environment{{ID: "github-env", Key: "github", Name: "GitHub", Scope: activity.EnvironmentScopeGlobal}},
		Facts:        []activity.Fact{{Subject: activity.SubjectID(request.Connection.SubjectID), Date: "2026-08-12", EnvironmentID: "github-env", Action: activity.ActionCommit, Metric: activity.Metric{Name: activity.MetricCount, Value: 3}}},
	}, nil
}

func TestManualSyncWithOptionsValidatesPropagatesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	ids := &sequentialIDs{}
	connection, err := mustConnectionService(store, clock, ids).ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github-env",
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSyncer{}
	registry, _ := NewSyncRegistry(syncer)
	service, err := NewSyncService(store, store, &recordingActivitySink{}, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	from, to := activity.Date("2026-08-01"), activity.Date("2026-08-12")
	input := ManualSyncInput{
		ConnectionID: connection.ID, IdempotencyKey: "request-key-1", From: &from, To: &to,
		Force: true, FailurePolicy: activity.FetchFailureKeepStale,
	}
	first, err := service.ManualSyncWithOptions(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ManualSyncWithOptions(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || syncer.calls != 1 {
		t.Fatalf("idempotent jobs = %q/%q, calls = %d", first.ID, second.ID, syncer.calls)
	}
	request := syncer.lastRequest
	if request.From == nil || *request.From != from || request.To == nil || *request.To != to || !request.Force || request.FailurePolicy != activity.FetchFailureKeepStale {
		t.Fatalf("provider request = %#v", request)
	}

	conflicting := input
	conflicting.Force = false
	if _, err := service.ManualSyncWithOptions(ctx, conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting idempotency error = %v", err)
	}
	invalid := input
	invalid.IdempotencyKey = "short"
	if _, err := service.ManualSyncWithOptions(ctx, invalid); !errors.Is(err, ErrInvalidIdempotencyKey) {
		t.Fatalf("short idempotency key error = %v", err)
	}
	badTo := activity.Date("2026-07-31")
	invalid = input
	invalid.To = &badTo
	if _, err := service.ManualSyncWithOptions(ctx, invalid); !errors.Is(err, ErrInvalidSyncDateRange) {
		t.Fatalf("invalid date range error = %v", err)
	}
	clock.set(clock.Now().Add(syncIdempotencyRetention + time.Second))
	afterExpiry, err := service.ManualSyncWithOptions(ctx, input)
	if err != nil || afterExpiry.ID == first.ID || syncer.calls != 2 {
		t.Fatalf("expired idempotency = %#v, error = %v, calls = %d", afterExpiry, err, syncer.calls)
	}
}

func TestSyncPublishesThroughConnectionClaimFence(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	connections := &claimedMemoryConnections{MemoryStore: store}
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	ids := &sequentialIDs{}
	connection, err := mustConnectionService(store, clock, ids).ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github-env",
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSyncer{}
	registry, _ := NewSyncRegistry(syncer)
	sink := &fencedRecordingActivitySink{}
	service, err := NewSyncService(connections, store, sink, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ManualSync(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	if !sink.called || sink.connectionID != connection.ID || sink.claimToken != "opaque-execution-claim" ||
		sink.facts.Subject != "subject-1" || len(sink.facts.Facts) != 1 {
		t.Fatalf("fenced publication = %#v", sink)
	}
	if len(connections.updateClaims) != 2 || connections.updateClaims[0] != "opaque-execution-claim" || connections.updateClaims[1] != "opaque-execution-claim" {
		t.Fatalf("update claims = %#v", connections.updateClaims)
	}
}

func TestEnqueueManualSyncIsDurableAndSchedulerExecutesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	ids := &sequentialIDs{}
	connection, err := mustConnectionService(store, clock, ids).ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github-env",
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSyncer{}
	registry, _ := NewSyncRegistry(syncer)
	service, err := NewSyncService(store, store, &recordingActivitySink{}, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	input := ManualSyncInput{ConnectionID: connection.ID, IdempotencyKey: "enqueue-key-1"}
	queued, err := service.EnqueueManualSync(ctx, input)
	if err != nil || queued.Status != SyncJobPending || syncer.calls != 0 {
		t.Fatalf("EnqueueManualSync() = %#v, error = %v, calls = %d", queued, err, syncer.calls)
	}
	again, err := service.EnqueueManualSync(ctx, input)
	if err != nil || again.ID != queued.ID {
		t.Fatalf("idempotent enqueue = %#v, error = %v", again, err)
	}
	scheduler, err := NewScheduler(store, service, clock, SchedulerConfig{
		SyncInterval: time.Hour, PollInterval: time.Minute, MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := scheduler.RunOnce(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != queued.ID || jobs[0].Status != SyncJobSucceeded || syncer.calls != 1 {
		t.Fatalf("RunOnce() = %#v, error = %v, calls = %d", jobs, err, syncer.calls)
	}
}

func TestManualSyncPurgeFailurePolicyReplacesOnlyRequestedRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	ids := &sequentialIDs{}
	connection, err := mustConnectionService(store, clock, ids).ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github-env",
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSyncer{err: errors.New("provider unavailable")}
	registry, _ := NewSyncRegistry(syncer)
	sink := &recordingActivitySink{}
	service, err := NewSyncService(store, store, sink, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	from, to := activity.Date("2026-08-01"), activity.Date("2026-08-12")
	job, err := service.ManualSyncWithOptions(ctx, ManualSyncInput{
		ConnectionID: connection.ID, IdempotencyKey: "request-key-2", From: &from, To: &to,
		FailurePolicy: activity.FetchFailurePurge,
	})
	if err == nil || job.Status != SyncJobFailed {
		t.Fatalf("ManualSyncWithOptions() = %#v, error = %v", job, err)
	}
	if len(sink.replacements) != 1 || sink.replacements[0].From == nil || *sink.replacements[0].From != from ||
		sink.replacements[0].To == nil || *sink.replacements[0].To != to {
		t.Fatalf("purged ranges = %#v", sink.replacements)
	}
}

func TestManualSyncAndSchedulerRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	ids := &sequentialIDs{}
	connectionService := mustConnectionService(store, clock, ids)
	connection, err := connectionService.ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab-env", IncludePrivate: true,
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSyncer{providerID: "gitlab"}
	registry, _ := NewSyncRegistry(syncer)
	sink := &recordingActivitySink{}
	syncService, err := NewSyncService(store, store, sink, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	job, err := syncService.ManualSync(ctx, connection.ID)
	if err != nil || job.Status != SyncJobSucceeded || job.FactsWritten != 1 || syncer.token != "secret-token" {
		t.Fatalf("ManualSync() = %#v, error = %v, token = %q", job, err, syncer.token)
	}
	record, _ := store.GetConnection(ctx, connection.ID)
	if record.Connection.LastSyncedAt == nil || len(sink.facts) != 1 {
		t.Fatalf("connection/sink not updated: %#v, %#v", record.Connection, sink.facts)
	}

	syncer.err = errors.New("provider unavailable")
	clock.set(clock.Now().Add(2 * time.Hour))
	scheduler, err := NewScheduler(store, syncService, clock, SchedulerConfig{SyncInterval: time.Hour, PollInterval: time.Minute, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := scheduler.RunOnce(ctx)
	if err == nil || len(jobs) != 1 || jobs[0].Status != SyncJobFailed {
		t.Fatalf("RunOnce() = %#v, error = %v", jobs, err)
	}
	record, _ = store.GetConnection(ctx, connection.ID)
	if record.Connection.Status != ConnectionError || record.Connection.LastError == "" {
		t.Fatalf("failed connection = %#v", record.Connection)
	}

	syncer.err = nil
	if jobs, err = scheduler.RunOnce(ctx); err != nil || len(jobs) != 0 {
		t.Fatalf("early retry RunOnce() = %#v, error = %v", jobs, err)
	}
	clock.set(*record.Connection.NextSyncAttemptAt)
	jobs, err = scheduler.RunOnce(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Status != SyncJobSucceeded {
		t.Fatalf("retry RunOnce() = %#v, error = %v", jobs, err)
	}
}
