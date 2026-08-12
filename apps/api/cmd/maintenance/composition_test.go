package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
)

func TestMaintenanceProductionRequiresDistinctRoleDatabase(t *testing.T) {
	settings := config.Config{Environment: config.EnvironmentProduction}
	if _, err := processruntime.DatabaseURL(settings, config.ProcessMaintenance); !errors.Is(err, processruntime.ErrDatabaseURLRequired) {
		t.Fatalf("missing maintenance DB error = %v", err)
	}
	settings.MaintenanceDatabaseURL = "postgresql://jandibat_worker@db/jandibat"
	if _, err := processruntime.DatabaseURL(settings, config.ProcessMaintenance); !errors.Is(err, processruntime.ErrDatabaseRoleMismatch) {
		t.Fatalf("wrong maintenance DB role error = %v", err)
	}
}

func TestMaintenanceProcessHandlerExposesMetricsAndHealth(t *testing.T) {
	health := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	metrics := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("metric 1\n")) })
	handler := newMaintenanceProcessHandler(health, metrics)
	for _, path := range []string{"/livez", "/readyz", "/healthz", "/metrics"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusNotFound {
			t.Fatalf("%s not registered", path)
		}
	}
}

func TestMaintenanceOperatorCommandValidationAndSecretFreeSummary(t *testing.T) {
	if !isMaintenanceCommand([]string{"retention"}) || isMaintenanceCommand([]string{"serve"}) {
		t.Fatal("operator command detection failed")
	}
	app := &maintenanceApplication{clock: maintenanceClock{}}
	if err := runMaintenanceCommand(context.Background(), app,
		[]string{"retention", "--as-of", "not-a-time", "--dry-run"}, io.Discard); !errors.Is(err, errMaintenanceCommandUsage) {
		t.Fatalf("invalid retention command error = %v", err)
	}
	var output bytes.Buffer
	request := operations.DeletionRequest{
		RequestID: "request-1", TargetType: operations.DeletionTargetAccount, TargetID: "sensitive-target",
		Status: operations.DeletionRequested,
	}
	if err := writeOperatorJSON(&output, deletionSummary(request)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), request.TargetID) || !strings.Contains(output.String(), request.RequestID) {
		t.Fatalf("unsafe deletion summary = %s", output.String())
	}
}

func TestMaintenanceRetentionPolicyIsCompleteAndBounded(t *testing.T) {
	rules := maintenanceRetentionRules()
	seen := make(map[operations.RetentionDataset]time.Duration, len(rules))
	for _, rule := range rules {
		if _, duplicate := seen[rule.Dataset]; duplicate {
			t.Fatalf("duplicate dataset %q", rule.Dataset)
		}
		if !operations.ValidRetentionDataset(rule.Dataset) || rule.RetainFor < 0 {
			t.Fatalf("invalid rule %#v", rule)
		}
		seen[rule.Dataset] = rule.RetainFor
	}
	day := 24 * time.Hour
	if retainFor, ok := seen[operations.RetentionOrphanedProviderEnvironments]; !ok || retainFor != 0 {
		t.Fatalf("orphan environment retention policy = %s, present=%t", retainFor, ok)
	}
	if seen[operations.RetentionRevokedProviderMetadata] != 30*day ||
		seen[operations.RetentionSuccessfulSyncJobs] != 30*day ||
		seen[operations.RetentionFailedSyncJobs] != 90*day ||
		seen[operations.RetentionMagicLinkMailOutbox] != day ||
		seen[operations.RetentionMutationAuditOutbox] != 400*day {
		t.Fatalf("retention policy = %#v", seen)
	}
}

func TestRecordMaintenanceResultIsSecretFreeAndPreservesFailure(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	runErr := errors.New("database password=must-not-be-recorded")
	if err := recordMaintenanceResult(context.Background(), recorder, "retention.purge", runErr, map[string]any{"deleted": map[string]any{"sessions": 4}}); err != nil {
		t.Fatal(err)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	event := events[0]
	if event.Action != "retention.purge" || event.Outcome != operations.AuditFailed || event.Actor.Type != operations.AuditActorSystem || event.RequestID == "" {
		t.Fatalf("event = %#v", event)
	}
	if _, leaked := event.Metadata["error"]; leaked {
		t.Fatalf("maintenance event contains an error body: %#v", event.Metadata)
	}
}

type failingAuditSink struct{ err error }

func (sink failingAuditSink) WriteAuditEvent(context.Context, operations.AuditEvent) error {
	return sink.err
}

func TestRecordMaintenanceResultPropagatesAuditFailure(t *testing.T) {
	want := errors.New("audit unavailable")
	recorder, err := operations.NewAuditRecorder(failingAuditSink{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if err := recordMaintenanceResult(context.Background(), recorder, "credential.reencrypt", nil, map[string]any{"rotated": 1}); !errors.Is(err, want) {
		t.Fatalf("recordMaintenanceResult() error = %v, want %v", err, want)
	}
}
