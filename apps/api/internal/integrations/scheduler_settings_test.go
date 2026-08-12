package integrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type syncSettingsReader struct {
	settings map[string]SubjectSyncSettings
	errors   map[string]error
	calls    map[string]int
}

func (reader *syncSettingsReader) LoadSubjectSyncSettings(_ context.Context, subjectID string) (SubjectSyncSettings, bool, error) {
	if reader.calls == nil {
		reader.calls = make(map[string]int)
	}
	reader.calls[subjectID]++
	if err := reader.errors[subjectID]; err != nil {
		return SubjectSyncSettings{}, false, err
	}
	settings, found := reader.settings[subjectID]
	return settings, found, nil
}

func TestSchedulerHonorsSubjectEnablementAndCadenceWithExplicitFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	clock := &fixedClock{now: now}
	ids := &sequentialIDs{}
	connections := mustConnectionService(store, clock, ids)
	connect := func(subject string) ProviderConnection {
		t.Helper()
		connection, err := connections.ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
			SubjectID: subject, ProviderID: "github", EnvironmentID: "github-env",
		}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
		if err != nil {
			t.Fatal(err)
		}
		return connection
	}
	disabled := connect("disabled")
	fast := connect("fast")
	slow := connect("slow")
	unmanaged := connect("unmanaged")
	for _, item := range []struct {
		connection ProviderConnection
		last       time.Time
	}{
		{disabled, now.Add(-24 * time.Hour)},
		{fast, now.Add(-31 * time.Minute)},
		{slow, now.Add(-61 * time.Minute)},
		{unmanaged, now.Add(-61 * time.Minute)},
	} {
		record, err := store.GetConnection(ctx, item.connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		record.Connection.LastSyncedAt = &item.last
		if err := store.SaveConnection(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	reader := &syncSettingsReader{settings: map[string]SubjectSyncSettings{
		"disabled": {Timezone: "UTC", Enabled: false, Interval: 15 * time.Minute, FailurePolicy: activity.FetchFailureKeepStale},
		"fast":     {Timezone: "Asia/Seoul", Enabled: true, Interval: 30 * time.Minute, FailurePolicy: activity.FetchFailurePurge},
		"slow":     {Timezone: "UTC", Enabled: true, Interval: 2 * time.Hour, FailurePolicy: activity.FetchFailureKeepStale},
	}}
	syncer := &fakeSyncer{}
	registry, _ := NewSyncRegistry(syncer)
	syncService, err := NewSyncService(store, store, &recordingActivitySink{}, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewScheduler(store, syncService, clock, SchedulerConfig{
		SyncInterval: time.Hour, PollInterval: time.Minute, MaxConcurrent: 1, SubjectSettings: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := scheduler.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("scheduled jobs = %#v, want fast plus unmanaged fallback", jobs)
	}
	jobsByConnection := make(map[string]SyncJob, len(jobs))
	for _, job := range jobs {
		jobsByConnection[job.ConnectionID] = job
	}
	if _, ok := jobsByConnection[disabled.ID]; ok {
		t.Fatal("disabled subject was scheduled")
	}
	if _, ok := jobsByConnection[slow.ID]; ok {
		t.Fatal("subject was scheduled before its configured cadence")
	}
	fastJob, ok := jobsByConnection[fast.ID]
	if !ok || fastJob.Timezone != "Asia/Seoul" || fastJob.FailurePolicy != activity.FetchFailurePurge {
		t.Fatalf("fast subject job = %#v", fastJob)
	}
	fallbackJob, ok := jobsByConnection[unmanaged.ID]
	if !ok || fallbackJob.Timezone != "" || fallbackJob.FailurePolicy != activity.FetchFailureKeepStale {
		t.Fatalf("unmanaged fallback job = %#v", fallbackJob)
	}
	for _, subject := range []string{"disabled", "fast", "slow", "unmanaged"} {
		if reader.calls[subject] != 1 {
			t.Fatalf("settings calls for %q = %d, want 1", subject, reader.calls[subject])
		}
	}
}

func TestScheduledSyncPropagatesTimezoneAndFailurePolicyToSyncer(t *testing.T) {
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
	sink := &recordingActivitySink{}
	service, err := NewSyncService(store, store, sink, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	settings := SubjectSyncSettings{
		Timezone: "America/New_York", Enabled: true, Interval: time.Hour, FailurePolicy: activity.FetchFailurePurge,
	}
	job, err := service.ScheduledSyncWithSettings(ctx, connection.ID, settings)
	if err != nil {
		t.Fatal(err)
	}
	if job.Timezone != settings.Timezone || job.FailurePolicy != settings.FailurePolicy {
		t.Fatalf("scheduled job settings = %#v", job)
	}
	if syncer.lastRequest.Timezone != settings.Timezone || syncer.lastRequest.FailurePolicy != settings.FailurePolicy {
		t.Fatalf("provider request settings = %#v", syncer.lastRequest)
	}
	syncer.err = errors.New("provider unavailable")
	failed, err := service.ScheduledSyncWithSettings(ctx, connection.ID, settings)
	if err == nil || failed.Status != SyncJobFailed {
		t.Fatalf("scheduled purge failure = %#v, error = %v", failed, err)
	}
	if len(sink.replacements) != 1 || sink.replacements[0].Subject != activity.SubjectID(connection.SubjectID) {
		t.Fatalf("scheduled purge replacements = %#v", sink.replacements)
	}
	if _, err := service.ScheduledSyncWithSettings(ctx, connection.ID, SubjectSyncSettings{
		Timezone: "Mars/Olympus", FailurePolicy: activity.FetchFailureKeepStale,
	}); !errors.Is(err, ErrInvalidSyncTimezone) {
		t.Fatalf("invalid timezone error = %v", err)
	}
}

func TestSchedulerSettingsFailureDoesNotBlockOtherSubjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	ids := &sequentialIDs{}
	connections := mustConnectionService(store, clock, ids)
	broken, err := connections.ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "broken", ProviderID: "github", EnvironmentID: "github-env",
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := connections.ConnectToken(ctx, ConnectTokenInput{ConnectInput: ConnectInput{
		SubjectID: "healthy", ProviderID: "github", EnvironmentID: "github-env",
	}, Credentials: TokenCredentials{AccessToken: "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("settings unavailable")
	reader := &syncSettingsReader{
		errors: map[string]error{"broken": sentinel},
		settings: map[string]SubjectSyncSettings{"healthy": {
			Timezone: "UTC", Enabled: true, Interval: time.Hour, FailurePolicy: activity.FetchFailureKeepStale,
		}},
	}
	syncer := &fakeSyncer{}
	registry, _ := NewSyncRegistry(syncer)
	service, err := NewSyncService(store, store, &recordingActivitySink{}, mustTestCipher(), registry, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewScheduler(store, service, clock, SchedulerConfig{
		SyncInterval: time.Hour, PollInterval: time.Minute, MaxConcurrent: 1, SubjectSettings: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := scheduler.RunOnce(ctx)
	if !errors.Is(err, sentinel) {
		t.Fatalf("scheduler error = %v, want %v", err, sentinel)
	}
	if len(jobs) != 1 || jobs[0].ConnectionID != healthy.ID || jobs[0].ConnectionID == broken.ID {
		t.Fatalf("jobs after one settings failure = %#v", jobs)
	}
}
