package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
	"go.uber.org/zap"
)

const (
	developmentCredentialKeyID = "development-only"
)

type operationalRuntime struct {
	audit      *operations.AuditRecorder
	background []namedBackgroundRunner
}

type backgroundRunner interface {
	Run(context.Context) error
}

type namedBackgroundRunner struct {
	name   string
	runner backgroundRunner
}

// RunBackground owns the lifetime of every recurring process task. An
// unexpected runner exit cancels its siblings and fails the process; normal
// context cancellation waits for every runner to finish.
func (app *application) RunBackground(ctx context.Context) error {
	if app == nil || len(app.background) == 0 {
		<-ctx.Done()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(app.background))
	for _, item := range app.background {
		item := item
		go func() { results <- result{name: item.name, err: item.runner.Run(runCtx)} }()
	}

	first := <-results
	unexpected := ctx.Err() == nil
	cancel()
	all := []result{first}
	for len(all) < len(app.background) {
		all = append(all, <-results)
	}
	var failures []error
	for _, item := range all {
		if item.err != nil && !errors.Is(item.err, context.Canceled) && !errors.Is(item.err, context.DeadlineExceeded) {
			failures = append(failures, fmt.Errorf("%s: %w", item.name, item.err))
		} else if unexpected && item.name == first.name {
			failures = append(failures, fmt.Errorf("%s stopped unexpectedly", item.name))
		}
	}
	return errors.Join(failures...)
}

type periodicWorker struct {
	name     string
	interval time.Duration
	timeout  time.Duration
	execute  func(context.Context) error
	logger   *zap.Logger
}

func (worker *periodicWorker) Run(ctx context.Context) error {
	if worker == nil || worker.interval <= 0 || worker.timeout <= 0 || worker.execute == nil {
		return errors.New("runtime: invalid periodic worker")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			runCtx, cancel := context.WithTimeout(ctx, worker.timeout)
			err := worker.execute(runCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				observability.Log(worker.logger, "maintenance.periodic_failed",
					zap.String("worker", worker.name), observability.SafeError(err))
			}
			timer.Reset(worker.interval)
		}
	}
}

func buildCredentialKeyring(settings config.Config) (operations.RotatingSecretCipher, error) {
	return processruntime.BuildCredentialKeyring(settings)
}

func buildOperationalRuntime(settings config.Config, stores databaseStores, keyring operations.RotatingSecretCipher, logger *zap.Logger) (operationalRuntime, error) {
	metrics := observability.Default()
	metrics.SetResource(observability.ResourceFromEnvironment(settings.Environment))
	metrics.RegisterPGXPool(stores.pool)
	metrics.RegisterQueueAgeProbe(observability.PGXQueueAgeProbe(stores.pool))
	metrics.RegisterActivityFreshnessProbe(observability.PGXActivityFreshnessProbe(stores.pool))

	var sink operations.AuditEventSink = operations.NewMemoryAuditSink()
	if stores.operations != nil {
		sink = stores.operations
	}
	sink = observedAuditSink{next: sink, metrics: metrics}
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		return operationalRuntime{}, fmt.Errorf("runtime: construct audit recorder: %w", err)
	}
	runtime := operationalRuntime{audit: recorder}
	if stores.operations == nil || !settings.DevelopmentAllInOne {
		return runtime, nil
	}

	batchSize := defaultPositive(settings.MaintenanceBatchSize, 500)
	maxBatches := defaultPositive(settings.RetentionMaxBatches, 30)
	timeout := defaultDuration(settings.MaintenanceTimeout, 15*time.Minute)
	retention, err := operations.NewRetentionWorker(stores.operations, runtimeClock{}, operations.RetentionConfig{
		Rules: defaultRetentionRules(), BatchSize: batchSize, MaxBatchesPerDataset: maxBatches,
	})
	if err != nil {
		return operationalRuntime{}, fmt.Errorf("runtime: construct retention worker: %w", err)
	}
	reencryption, err := operations.NewReencryptionWorker(stores.operations, keyring, batchSize)
	if err != nil {
		return operationalRuntime{}, fmt.Errorf("runtime: construct re-encryption worker: %w", err)
	}
	runtime.background = []namedBackgroundRunner{
		{name: "credential re-encryption", runner: &periodicWorker{
			name: "credential re-encryption", interval: defaultDuration(settings.ReencryptionInterval, time.Hour), timeout: timeout,
			logger: logger,
			execute: func(ctx context.Context) error {
				result, runErr := reencryption.Run(ctx)
				auditErr := recordMaintenance(ctx, recorder, "credential.reencrypt", runErr, map[string]any{
					"active_key_id": keyring.ActiveKeyID(), "scanned": result.Scanned,
					"rotated": result.Rotated, "skipped": result.Skipped, "failed": result.Failed,
				})
				return errors.Join(runErr, auditErr)
			},
		}},
		{name: "data retention", runner: &periodicWorker{
			name: "data retention", interval: defaultDuration(settings.RetentionInterval, 24*time.Hour), timeout: timeout,
			logger: logger,
			execute: func(ctx context.Context) error {
				result, runErr := retention.Run(ctx)
				deleted := make(map[string]any, len(result.Deleted))
				for dataset, count := range result.Deleted {
					deleted[string(dataset)] = count
					if count > 0 {
						metrics.ObserveRetention(string(dataset), "succeeded", uint64(count))
					}
				}
				auditErr := recordMaintenance(ctx, recorder, "retention.purge", runErr, map[string]any{
					"deleted": deleted, "failed_datasets": retentionDatasetStrings(result.Failed),
					"truncated_datasets": retentionDatasetStrings(result.Truncated),
				})
				return errors.Join(runErr, auditErr)
			},
		}},
	}
	return runtime, nil
}

type observedAuditSink struct {
	next    operations.AuditEventSink
	metrics *observability.Registry
}

func (sink observedAuditSink) WriteAuditEvent(ctx context.Context, event operations.AuditEvent) error {
	err := sink.next.WriteAuditEvent(ctx, event)
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	sink.metrics.ObserveAudit(event.Action, outcome, time.Since(event.OccurredAt))
	return err
}

func recordMaintenance(ctx context.Context, recorder *operations.AuditRecorder, action string, runErr error, metadata map[string]any) error {
	id, err := operations.NewAuditEventID()
	if err != nil {
		return err
	}
	outcome := operations.AuditSucceeded
	if runErr != nil {
		outcome = operations.AuditFailed
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return recorder.Record(auditCtx, operations.AuditEvent{
		ID: id, OccurredAt: time.Now().UTC(), Actor: operations.AuditActor{Type: operations.AuditActorSystem},
		Action: action, Target: operations.AuditTarget{Type: "maintenance"}, Outcome: outcome,
		RequestID: "maintenance:" + id, Metadata: metadata,
	})
}

func retentionDatasetStrings(values []operations.RetentionDataset) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func defaultRetentionRules() []operations.RetentionRule {
	day := 24 * time.Hour
	return []operations.RetentionRule{
		{Dataset: operations.RetentionAuditEvents, RetainFor: 400 * day},
		{Dataset: operations.RetentionActivityFacts, RetainFor: 400 * day},
		{Dataset: operations.RetentionCustomActivities, RetainFor: 400 * day},
		{Dataset: operations.RetentionRevokedProviderMetadata, RetainFor: 30 * day},
		{Dataset: operations.RetentionOrphanedProviderEnvironments, RetainFor: 0},
		{Dataset: operations.RetentionSuccessfulSyncJobs, RetainFor: 30 * day},
		{Dataset: operations.RetentionFailedSyncJobs, RetainFor: 90 * day},
		{Dataset: operations.RetentionSessions, RetainFor: 30 * day},
		{Dataset: operations.RetentionMagicLinks, RetainFor: day},
		{Dataset: operations.RetentionAuthChallenges, RetainFor: day},
		{Dataset: operations.RetentionIdempotencyKeys, RetainFor: 0},
		{Dataset: operations.RetentionTimelineCache, RetainFor: 7 * day},
		{Dataset: operations.RetentionActivityRefresh, RetainFor: 7 * day},
		{Dataset: operations.RetentionRateLimitBuckets, RetainFor: 0},
	}
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func defaultPositive(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

type runtimeClock struct{}

func (runtimeClock) Now() time.Time { return time.Now() }
