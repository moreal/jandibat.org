package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
	"go.uber.org/zap"
)

type maintenanceApplication struct {
	logger               *zap.Logger
	runner               processruntime.Runner
	readiness            *operations.ReadinessChecker
	metrics              *observability.Registry
	store                *operationsstore.Store
	retention            *operations.RetentionWorker
	reencrypt            *operations.ReencryptionWorker
	deletions            *operations.DeletionExecutor
	audit                *operations.AuditRecorder
	clock                operations.Clock
	deletionPseudonymKey []byte
	close                func() error
}

func (app *maintenanceApplication) Close() error {
	if app == nil {
		return nil
	}
	clear(app.deletionPseudonymKey)
	if app.store != nil {
		app.store.ClearDeletedIdentityHMAC()
	}
	if app.close == nil {
		return nil
	}
	return app.close()
}

type maintenanceRunnerGroup []processruntime.NamedRunner

func (group maintenanceRunnerGroup) Run(ctx context.Context) error {
	return processruntime.RunGroup(ctx, group...)
}

type maintenanceClock struct{}

func (maintenanceClock) Now() time.Time { return time.Now() }

func buildMaintenance(ctx context.Context, settings config.Config, logger *zap.Logger) (*maintenanceApplication, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	databaseURL, err := processruntime.DatabaseURL(settings, config.ProcessMaintenance)
	if err != nil {
		return nil, err
	}
	database, err := processruntime.OpenDatabase(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = database.Close()
		}
	}()

	store, err := operationsstore.NewWithPGXPool(nil, database.Pool)
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct operations store: %w", err)
	}
	if settings.DeletedIdentityHMACActiveKeyID != "" || len(settings.DeletedIdentityHMACKeys) != 0 {
		if err := store.ConfigureDeletedIdentityHMAC(
			settings.DeletedIdentityHMACActiveKeyID,
			settings.DeletedIdentityHMACKeys[settings.DeletedIdentityHMACActiveKeyID],
		); err != nil {
			return nil, fmt.Errorf("maintenance: configure deleted identity HMAC: %w", err)
		}
	}
	keyring, err := processruntime.BuildCredentialKeyring(settings)
	if err != nil {
		return nil, err
	}
	audit, err := operations.NewAuditRecorder(store)
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct audit recorder: %w", err)
	}
	metrics := observability.Default()
	metrics.SetResource(observability.ResourceFromEnvironment(settings.Environment))
	metrics.RegisterPGXPool(database.Pool)
	metrics.RegisterDeletionAgeProbe(observability.PGXDeletionAgeProbe(database.Pool))
	keyring = observability.InstrumentCredentialCipher(keyring, metrics)
	batchSize := positiveOr(settings.MaintenanceBatchSize, 500)
	retention, err := operations.NewRetentionWorker(store, maintenanceClock{}, operations.RetentionConfig{
		Rules: maintenanceRetentionRules(), BatchSize: batchSize,
		MaxBatchesPerDataset: positiveOr(settings.RetentionMaxBatches, 30),
	})
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct retention worker: %w", err)
	}
	reencryption, err := operations.NewReencryptionWorker(store, keyring, batchSize)
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct re-encryption worker: %w", err)
	}
	timeout := durationOr(settings.MaintenanceTimeout, 15*time.Minute)
	deletionWorkflow, err := operations.NewDeletionWorkflow(store, maintenanceClock{}, audit, settings.DeletionPseudonymKey)
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct deletion workflow: %w", err)
	}
	deletions, err := operations.NewDeletionExecutor(store, deletionWorkflow, maintenanceClock{}, batchSize, timeout+time.Minute)
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct deletion executor: %w", err)
	}
	runners := maintenanceRunnerGroup{
		{Name: "account and subject deletion", Runner: &processruntime.PeriodicRunner{
			Name: "account and subject deletion", Interval: time.Minute, Timeout: timeout,
			Logger: logger,
			Execute: func(runCtx context.Context) error {
				result, runErr := deletions.Run(runCtx)
				auditStarted := time.Now()
				auditErr := recordMaintenanceResult(runCtx, audit, "deletion.execute", runErr, map[string]any{
					"claimed": result.Claimed, "completed": result.Completed, "failed": result.Failed,
				})
				metrics.ObserveAudit("deletion.execute", auditMetricOutcome(auditErr), time.Since(auditStarted))
				return errors.Join(runErr, auditErr)
			},
		}},
		{Name: "credential re-encryption", Runner: &processruntime.PeriodicRunner{
			Name: "credential re-encryption", Interval: durationOr(settings.ReencryptionInterval, time.Hour), Timeout: timeout,
			Logger: logger,
			Execute: func(runCtx context.Context) error {
				result, runErr := reencryption.Run(runCtx)
				auditStarted := time.Now()
				auditErr := recordMaintenanceResult(runCtx, audit, "credential.reencrypt", runErr, map[string]any{
					"scanned": result.Scanned, "rotated": result.Rotated,
					"skipped": result.Skipped, "failed": result.Failed, "by_key": reencryptionKeyMetadata(result.ByKey),
				})
				metrics.ObserveAudit("credential.reencrypt", auditMetricOutcome(auditErr), time.Since(auditStarted))
				return errors.Join(runErr, auditErr)
			},
		}},
		{Name: "data retention", Runner: &processruntime.PeriodicRunner{
			Name: "data retention", Interval: durationOr(settings.RetentionInterval, 24*time.Hour), Timeout: timeout,
			Logger: logger,
			Execute: func(runCtx context.Context) error {
				result, runErr := retention.Run(runCtx)
				deleted := make(map[string]any, len(result.Deleted))
				for dataset, count := range result.Deleted {
					deleted[string(dataset)] = count
					outcome := "succeeded"
					if containsRetentionDataset(result.Failed, dataset) {
						outcome = "failed"
					}
					if count > 0 {
						metrics.ObserveRetention(string(dataset), outcome, uint64(count))
					}
				}
				auditStarted := time.Now()
				auditErr := recordMaintenanceResult(runCtx, audit, "retention.purge", runErr, map[string]any{
					"deleted": deleted, "failed_datasets": maintenanceDatasetNames(result.Failed),
					"truncated_datasets": maintenanceDatasetNames(result.Truncated),
				})
				metrics.ObserveAudit("retention.purge", auditMetricOutcome(auditErr), time.Since(auditStarted))
				return errors.Join(runErr, auditErr)
			},
		}},
	}
	readiness, err := operations.NewReadinessChecker(2*time.Second,
		operations.ReadinessDependency{
			Name: "maintenance-database-pool", Probe: operations.DependencyProbeFunc(database.Pool.Ping),
		},
		operations.ReadinessDependency{Name: "maintenance-schema", Probe: store},
	)
	if err != nil {
		return nil, fmt.Errorf("maintenance: construct readiness: %w", err)
	}

	app := &maintenanceApplication{
		logger: logger, runner: runners, readiness: readiness, metrics: metrics, store: store,
		retention: retention, reencrypt: reencryption, deletions: deletions, audit: audit, clock: maintenanceClock{}, close: database.Close,
		deletionPseudonymKey: append([]byte(nil), settings.DeletionPseudonymKey...),
	}
	success = true
	return app, nil
}

func containsRetentionDataset(datasets []operations.RetentionDataset, target operations.RetentionDataset) bool {
	for _, dataset := range datasets {
		if dataset == target {
			return true
		}
	}
	return false
}

func auditMetricOutcome(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func reencryptionKeyMetadata(results map[string]operations.ReencryptionKeyResult) map[string]any {
	metadata := make(map[string]any, len(results))
	for keyID, result := range results {
		metadata[keyID] = map[string]any{
			"scanned": result.Scanned, "rotated": result.Rotated, "pending": result.Pending,
			"skipped": result.Skipped, "failed": result.Failed,
		}
	}
	return metadata
}

func recordMaintenanceResult(ctx context.Context, recorder *operations.AuditRecorder, action string, runErr error, metadata map[string]any) error {
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

func maintenanceDatasetNames(datasets []operations.RetentionDataset) []string {
	result := make([]string, len(datasets))
	for index, dataset := range datasets {
		result[index] = string(dataset)
	}
	return result
}

func maintenanceRetentionRules() []operations.RetentionRule {
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
		{Dataset: operations.RetentionMagicLinkMailOutbox, RetainFor: day},
		{Dataset: operations.RetentionMutationAuditOutbox, RetainFor: 400 * day},
		{Dataset: operations.RetentionAuthChallenges, RetainFor: day},
		{Dataset: operations.RetentionIdempotencyKeys, RetainFor: 0},
		{Dataset: operations.RetentionTimelineCache, RetainFor: 7 * day},
		{Dataset: operations.RetentionActivityRefresh, RetainFor: 7 * day},
		{Dataset: operations.RetentionRateLimitBuckets, RetainFor: 0},
		{Dataset: operations.RetentionDeletedIdentityTombstones, RetainFor: 0},
		{Dataset: operations.RetentionDeletedIdentityTombstonesV2, RetainFor: 0},
		{Dataset: operations.RetentionDeletionRequests, RetainFor: 0},
		{Dataset: operations.RetentionDeletionRequestInbox, RetainFor: 35 * day},
	}
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func positiveOr(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func newHealthServer(address string, handler http.Handler, logger *zap.Logger) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler,
		ErrorLog:          observability.NewHTTPServerErrorLog(logger),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
}
