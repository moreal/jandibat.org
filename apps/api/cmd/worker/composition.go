package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	authstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
)

type workerApplication struct {
	runner    processruntime.Runner
	readiness *operations.ReadinessChecker
	client    *http.Client
	metrics   *observability.Registry
	close     func() error
}

func (app *workerApplication) Close() error {
	if app == nil {
		return nil
	}
	if app.client != nil {
		app.client.CloseIdleConnections()
	}
	if app.close != nil {
		return app.close()
	}
	return nil
}

func buildWorker(ctx context.Context, settings config.Config) (*workerApplication, error) {
	databaseURL, err := processruntime.DatabaseURL(settings, config.ProcessWorker)
	if err != nil {
		return nil, err
	}
	database, err := processruntime.OpenDatabase(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	db := database.DB
	success := false
	defer func() {
		if !success {
			_ = database.Close()
		}
	}()
	activity, err := activitystore.New(db)
	if err != nil {
		return nil, fmt.Errorf("worker: construct activity store: %w", err)
	}
	integration, err := integrationstore.New(db)
	if err != nil {
		return nil, fmt.Errorf("worker: construct integration store: %w", err)
	}
	subjects, err := subjectstore.New(db)
	if err != nil {
		return nil, fmt.Errorf("worker: construct subject store: %w", err)
	}
	authRepository, err := authstore.New(db)
	if err != nil {
		return nil, fmt.Errorf("worker: construct auth store: %w", err)
	}
	operationStore, err := operationsstore.New(db)
	if err != nil {
		return nil, fmt.Errorf("worker: construct operations store: %w", err)
	}
	keyring, err := processruntime.BuildCredentialKeyring(settings)
	if err != nil {
		return nil, err
	}
	metrics := observability.Default()
	metrics.SetResource(observability.ResourceFromEnvironment(settings.Environment))
	metrics.RegisterDBPool(db)
	metrics.RegisterQueueAgeProbe(observability.SQLQueueAgeProbe(db))
	metrics.RegisterActivityFreshnessProbe(observability.SQLActivityFreshnessProbe(db))
	metrics.RegisterRevocationDLQProbe(observability.SQLRevocationDLQProbe(db))
	keyring = observability.InstrumentCredentialCipher(keyring, metrics)
	client := processruntime.NewProviderHTTPClient()
	client.Transport = observability.InstrumentRoundTripper(client.Transport, metrics)
	registry, err := processruntime.BuildSyncRegistry(client)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	syncService, err := integrations.NewSyncService(integration, integration, activity, keyring, registry, nil, nil)
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("worker: construct sync service: %w", err)
	}
	scheduler, err := integrations.NewScheduler(integration, syncService, nil, integrations.SchedulerConfig{
		SyncInterval:  defaultDuration(settings.SchedulerInterval, 15*time.Minute),
		PollInterval:  defaultDuration(settings.SchedulerInterval, 15*time.Minute),
		MaxConcurrent: 4, SubjectSettings: workerSubjectSettings{repository: subjects},
	})
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("worker: construct scheduler: %w", err)
	}
	revoker, err := buildWorkerOAuthRevoker(settings, client)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	revocationWorker, err := integrations.NewOAuthTokenRevocationWorker(integration, keyring, revoker, nil, integrations.OAuthTokenRevocationWorkerConfig{
		PollInterval: time.Minute, Lease: 2 * time.Minute, CallTimeout: 10 * time.Second, BatchSize: 25,
	})
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("worker: construct OAuth token revocation worker: %w", err)
	}
	mailer, err := processruntime.BuildMagicLinkMailer(settings)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	mailWorker, err := auth.NewMagicLinkDeliveryWorker(authRepository, mailer, metrics, auth.MagicLinkDeliveryWorkerConfig{
		PollInterval: 5 * time.Second, Lease: time.Minute, CallTimeout: 15 * time.Second,
		TokenTTL: 15 * time.Minute, BatchSize: 25, MaxAttempts: 5,
	})
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("worker: construct Magic Link delivery worker: %w", err)
	}
	auditDispatcher, err := operations.NewMutationAuditDispatcher(operationStore, workerClock{}, operations.MutationAuditDispatcherConfig{
		PollInterval: 5 * time.Second, Lease: time.Minute, BatchSize: 25, MaxAttempts: 5,
	})
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("worker: construct mutation audit dispatcher: %w", err)
	}
	runner, err := composeWorkerRunners(&observedScheduler{
		scheduler: scheduler, connections: integration, metrics: metrics,
		pollInterval: defaultDuration(settings.SchedulerInterval, 15*time.Minute),
	}, revocationWorker, mailWorker, auditDispatcher)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	readiness, err := operations.NewReadinessChecker(2*time.Second,
		operations.ReadinessDependency{Name: "worker-database", Probe: operations.DependencyProbeFunc(db.PingContext)},
		operations.ReadinessDependency{Name: "provider-revocation-schema", Probe: operations.DependencyProbeFunc(integration.CheckRevocationSchema)},
		operations.ReadinessDependency{Name: "magic-link-delivery-schema", Probe: operations.DependencyProbeFunc(authRepository.CheckMagicLinkDeliverySchema)},
		operations.ReadinessDependency{Name: "mutation-audit-outbox-schema", Probe: operations.DependencyProbeFunc(operationStore.CheckMutationAuditOutboxSchema)},
	)
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("worker: construct readiness: %w", err)
	}
	app := &workerApplication{runner: runner, readiness: readiness, client: client, metrics: metrics, close: database.Close}
	success = true
	return app, nil
}

type workerRunnerGroup []processruntime.NamedRunner

func composeWorkerRunners(syncRunner, revocationRunner, mailRunner, auditRunner processruntime.Runner) (workerRunnerGroup, error) {
	if syncRunner == nil || revocationRunner == nil || mailRunner == nil || auditRunner == nil {
		return nil, errors.New("worker: sync, revocation, Magic Link mail, and mutation audit runners are required")
	}
	return workerRunnerGroup{
		{Name: "provider synchronization", Runner: syncRunner},
		{Name: "OAuth token revocation", Runner: revocationRunner},
		{Name: "Magic Link mail delivery", Runner: mailRunner},
		{Name: "mutation audit delivery", Runner: auditRunner},
	}, nil
}

type workerClock struct{}

func (workerClock) Now() time.Time { return time.Now().UTC() }

func (group workerRunnerGroup) Run(ctx context.Context) error {
	return processruntime.RunGroup(ctx, group...)
}

type schedulerSweep interface {
	RunOnce(context.Context) ([]integrations.SyncJob, error)
}

type connectionLister interface {
	ListConnections(context.Context, string) ([]integrations.ConnectionRecord, error)
}

type observedScheduler struct {
	scheduler    schedulerSweep
	connections  connectionLister
	metrics      *observability.Registry
	pollInterval time.Duration
}

func (runner *observedScheduler) Run(ctx context.Context) error {
	if runner == nil || runner.scheduler == nil || runner.connections == nil || runner.metrics == nil || runner.pollInterval <= 0 {
		return errors.New("worker: invalid observed scheduler")
	}
	var runErrors []error
	run := func() {
		jobs, err := runner.scheduler.RunOnce(ctx)
		runner.observe(ctx, jobs)
		if err != nil {
			runErrors = append(runErrors, err)
		}
	}
	run()
	ticker := time.NewTicker(runner.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				runErrors = append(runErrors, ctx.Err())
			}
			return errors.Join(runErrors...)
		case <-ticker.C:
			run()
		}
	}
}

func (runner *observedScheduler) observe(ctx context.Context, jobs []integrations.SyncJob) {
	if len(jobs) == 0 {
		return
	}
	records, err := runner.connections.ListConnections(ctx, "")
	if err != nil {
		for _, job := range jobs {
			runner.observeJob("unknown", job)
		}
		return
	}
	providers := make(map[string]string, len(records))
	for _, record := range records {
		providers[record.Connection.ID] = record.Connection.ProviderID
	}
	for _, job := range jobs {
		runner.observeJob(providers[job.ConnectionID], job)
	}
}

func (runner *observedScheduler) observeJob(provider string, job integrations.SyncJob) {
	runner.metrics.ObserveSyncJob(provider, string(job.Status), string(job.Trigger))
	if job.StartedAt != nil && !job.CreatedAt.IsZero() {
		runner.metrics.ObserveSyncJobStartDelay(job.StartedAt.Sub(job.CreatedAt))
	}
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
