package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
)

func TestWorkerProductionRequiresDistinctRoleDatabase(t *testing.T) {
	settings := config.Config{Environment: config.EnvironmentProduction}
	if _, err := processruntime.DatabaseURL(settings, config.ProcessWorker); !errors.Is(err, processruntime.ErrDatabaseURLRequired) {
		t.Fatalf("missing worker DB error = %v", err)
	}
	settings.WorkerDatabaseURL = "postgresql://jandibat_api@db/jandibat"
	if _, err := processruntime.DatabaseURL(settings, config.ProcessWorker); !errors.Is(err, processruntime.ErrDatabaseRoleMismatch) {
		t.Fatalf("wrong worker DB role error = %v", err)
	}
}

func TestWorkerCompositionIncludesMailAndMutationAuditRunners(t *testing.T) {
	runner := workerTestRunner{}
	group, err := composeWorkerRunners(runner, runner, runner, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 4 || group[2].Name != "Magic Link mail delivery" || group[3].Name != "mutation audit delivery" || group[3].Runner == nil {
		t.Fatalf("worker runners = %#v", group)
	}
	if _, err := composeWorkerRunners(runner, runner, runner, nil); err == nil {
		t.Fatal("composition accepted missing mutation audit runner")
	}
}

type workerTestRunner struct{}

func (workerTestRunner) Run(context.Context) error { return nil }

func TestObservedSchedulerRecordsBoundedSyncJobMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := observability.NewRegistry(observability.Resource{Environment: "test"})
	runner := &observedScheduler{
		scheduler: schedulerSweepFake{run: func(context.Context) ([]integrations.SyncJob, error) {
			cancel()
			return []integrations.SyncJob{{
				ConnectionID: "connection-1", Status: integrations.SyncJobSucceeded, Trigger: integrations.SyncTriggerScheduled,
			}}, nil
		}},
		connections: connectionListerFake{records: []integrations.ConnectionRecord{{
			Connection: integrations.ProviderConnection{ID: "connection-1", ProviderID: "github"},
		}}},
		metrics: registry, pollInterval: time.Hour,
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), `sync_jobs_total{provider="github",status="succeeded",trigger="scheduled"`) {
		t.Fatalf("sync job metric missing:\n%s", recorder.Body.String())
	}
}

func TestObservedSchedulerFallsBackToUnknownProviderOnLookupFailure(t *testing.T) {
	registry := observability.NewRegistry(observability.Resource{Environment: "test"})
	runner := &observedScheduler{
		connections: connectionListerFake{err: errors.New("database unavailable")},
		metrics:     registry,
	}
	runner.observe(context.Background(), []integrations.SyncJob{{Status: integrations.SyncJobFailed, Trigger: integrations.SyncTriggerManual}})

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), `sync_jobs_total{provider="unknown",status="failed",trigger="manual"`) {
		t.Fatalf("fallback sync job metric missing:\n%s", recorder.Body.String())
	}
}

func TestWorkerInternalHandlerExposesHealthAliasesAndMetrics(t *testing.T) {
	registry := observability.NewRegistry(observability.Resource{Environment: "test"})
	health := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := newWorkerProcessHandler(health, registry.Handler())
	for _, path := range []string{"/livez", "/readyz", "/healthz", "/metrics"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code < 200 || recorder.Code >= 300 {
			t.Errorf("GET %s = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

type schedulerSweepFake struct {
	run func(context.Context) ([]integrations.SyncJob, error)
}

func (scheduler schedulerSweepFake) RunOnce(ctx context.Context) ([]integrations.SyncJob, error) {
	return scheduler.run(ctx)
}

type connectionListerFake struct {
	records []integrations.ConnectionRecord
	err     error
}

func (lister connectionListerFake) ListConnections(context.Context, string) ([]integrations.ConnectionRecord, error) {
	return lister.records, lister.err
}
