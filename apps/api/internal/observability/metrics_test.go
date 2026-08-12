package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistryExposesRequiredFamiliesWithResourceLabels(t *testing.T) {
	registry := NewRegistry(Resource{BuildSHA: "abc123", Environment: "staging", Region: "icn"})
	registry.ObserveHTTP("/v1/subjects/{subject}", "GET", http.StatusOK, 75*time.Millisecond)
	registry.ObserveProvider("github", "2xx", 125*time.Millisecond)
	registry.ObserveSyncJob("tenant-provider-id", "succeeded", "manual")
	registry.ObserveSyncJobStartDelay(3 * time.Second)
	registry.ObserveCustomIngest("accepted", 3)
	registry.ObserveCustomIngest("duplicate", 2)
	registry.ObserveCustomIngest("rejected", 1)
	registry.ObserveAudit("subject.update", "succeeded", 20*time.Millisecond)
	registry.ObserveRetention("sessions", "succeeded", 7)
	registry.observeCredentialDecrypt("old-key", "succeeded")
	registry.ObserveDBPoolWait(75 * time.Millisecond)
	registry.RegisterQueueAgeProbe(func(context.Context) (float64, error) { return 42.5, nil })
	registry.RegisterDeletionAgeProbe(func(context.Context) (float64, error) { return 86_400, nil })
	registry.RegisterActivityFreshnessProbe(func(context.Context) ([]FreshnessSample, error) {
		return []FreshnessSample{
			{Provider: "github", Visibility: "public", Seconds: 120},
			{Provider: "github", Visibility: "public", Seconds: 1900},
		}, nil
	})
	registry.RegisterRevocationDLQProbe(func(context.Context) (float64, float64, error) { return 2, 900, nil })

	body := scrape(t, registry)
	for _, fragment := range []string{
		`http_server_requests_total{route="/v1/subjects/{subject}",method="GET",status_class="2xx",build_sha="abc123",environment="staging",region="icn"} 1`,
		`http_server_request_duration_seconds_bucket{route="/v1/subjects/{subject}",method="GET",le="0.1",build_sha="abc123",environment="staging",region="icn"} 1`,
		`provider_requests_total{provider="github",outcome="2xx",build_sha="abc123",environment="staging",region="icn"} 1`,
		`sync_jobs_total{provider="custom",status="succeeded",trigger="manual",build_sha="abc123",environment="staging",region="icn"} 1`,
		`sync_job_start_delay_seconds_bucket{le="5",build_sha="abc123",environment="staging",region="icn"} 1`,
		`custom_ingest_events_total{outcome="accepted",build_sha="abc123",environment="staging",region="icn"} 3`,
		`custom_ingest_events_total{outcome="duplicate",build_sha="abc123",environment="staging",region="icn"} 2`,
		`custom_ingest_events_total{outcome="rejected",build_sha="abc123",environment="staging",region="icn"} 1`,
		`audit_events_total{action="subject.update",outcome="succeeded",build_sha="abc123",environment="staging",region="icn"} 1`,
		`retention_rows_total{table="sessions",outcome="succeeded",build_sha="abc123",environment="staging",region="icn"} 7`,
		`credential_decrypt_operations_total{key_id="old-key",outcome="succeeded",build_sha="abc123",environment="staging",region="icn"} 1`,
		`sync_queue_oldest_ready_seconds{build_sha="abc123",environment="staging",region="icn"} 42.5`,
		`deletion_request_age_seconds{build_sha="abc123",environment="staging",region="icn"} 86400`,
		`activity_freshness_seconds{provider="github",visibility="public",build_sha="abc123",environment="staging",region="icn"} 1900`,
		`activity_freshness_subjects{provider="github",visibility="public",threshold="le_30m",build_sha="abc123",environment="staging",region="icn"} 1`,
		`activity_freshness_subjects{provider="github",visibility="public",threshold="le_2h",build_sha="abc123",environment="staging",region="icn"} 2`,
		`activity_freshness_subjects{provider="github",visibility="public",threshold="total",build_sha="abc123",environment="staging",region="icn"} 2`,
		`db_pool_wait_seconds{build_sha="abc123",environment="staging",region="icn"} 0.075`,
		`db_pool_wait_duration_seconds_bucket{le="0.1",build_sha="abc123",environment="staging",region="icn"} 1`,
		`db_pool_wait_duration_seconds_count{build_sha="abc123",environment="staging",region="icn"} 1`,
		`observability_capability{capability="db_pool_wait_p99",state="implemented",build_sha="abc123",environment="staging",region="icn"} 1`,
		`provider_token_revocation_dead_jobs{build_sha="abc123",environment="staging",region="icn"} 2`,
		`provider_token_revocation_oldest_dead_seconds{build_sha="abc123",environment="staging",region="icn"} 900`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("metrics missing %q\n%s", fragment, body)
		}
	}
}

func TestRegistryBoundsAttackerControlledDimensions(t *testing.T) {
	registry := NewRegistry(Resource{})
	registry.ObserveHTTP("", "ATTACK-"+strings.Repeat("x", 100), 799, time.Millisecond)
	registry.ObserveProvider("provider-"+strings.Repeat("x", 100), "unexpected", time.Millisecond)
	registry.ObserveSyncJob("provider-user-id", "invented", "invented")

	body := scrape(t, registry)
	for _, fragment := range []string{
		`route="unmatched",method="OTHER",status_class="unknown"`,
		`provider="custom",outcome="dependency_error"`,
		`provider="custom",status="unknown",trigger="unknown"`,
		`build_sha="unknown",environment="unknown",region="unknown"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("bounded metrics missing %q\n%s", fragment, body)
		}
	}
	if strings.Contains(body, "ATTACK-") || strings.Contains(body, "provider-user-id") {
		t.Fatalf("unbounded label reached exposition:\n%s", body)
	}
}

func TestQueueProbeFailureIsVisibleWithoutReportingFalseZeroAge(t *testing.T) {
	registry := NewRegistry(Resource{Environment: "test"})
	registry.RegisterQueueAgeProbe(func(context.Context) (float64, error) { return 0, errors.New("offline") })

	body := scrape(t, registry)
	if !strings.Contains(body, `sync_queue_oldest_ready_seconds{build_sha="unknown",environment="test",region="unknown"} NaN`) {
		t.Fatalf("failed queue probe must expose NaN:\n%s", body)
	}
	if !strings.Contains(body, `observability_collection_errors_total{collector="sync_queue",build_sha="unknown",environment="test",region="unknown"} 1`) {
		t.Fatalf("failed queue probe must increment collection error:\n%s", body)
	}
}

func TestInstrumentRoundTripperRecordsBoundedProviderOutcome(t *testing.T) {
	registry := NewRegistry(Resource{Environment: "test"})
	transport := InstrumentRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody, Header: make(http.Header)}, nil
	}), registry)
	request := httptest.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	response, err := transport.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("RoundTrip() = %#v, %v", response, err)
	}
	body := scrape(t, registry)
	if !strings.Contains(body, `provider_requests_total{provider="github",outcome="4xx"`) {
		t.Fatalf("provider request metric missing:\n%s", body)
	}
}

func TestSQLQueueAgeQueryMatchesDurableSyncJobSchema(t *testing.T) {
	for _, fragment := range []string{"FROM provider_sync_jobs", "status = 'queued'", "available_at <= current_timestamp"} {
		if !strings.Contains(sqlQueueAgeQuery, fragment) {
			t.Errorf("queue age query missing %q: %s", fragment, sqlQueueAgeQuery)
		}
	}
	if strings.Contains(sqlQueueAgeQuery, "FROM sync_jobs") || strings.Contains(sqlQueueAgeQuery, "status = 'pending'") {
		t.Fatalf("queue age query references stale schema: %s", sqlQueueAgeQuery)
	}
}

func TestSQLDeletionAgeQueryIncludesAllUnfinishedRequests(t *testing.T) {
	for _, fragment := range []string{
		"FROM deletion_request_inbox",
		"status = 'requested'",
		"UNION ALL",
		"FROM deletion_requests",
		"MIN(requested_at)",
		"status <> 'completed'",
	} {
		if !strings.Contains(sqlDeletionAgeQuery, fragment) {
			t.Errorf("deletion age query missing %q: %s", fragment, sqlDeletionAgeQuery)
		}
	}
	if strings.Contains(sqlDeletionAgeQuery, "status = 'promoted'") {
		t.Fatalf("promoted inbox rows would double-count active workflow requests: %s", sqlDeletionAgeQuery)
	}
}

func TestSQLActivityFreshnessQueryUsesFactIngestionAndBoundedDimensions(t *testing.T) {
	for _, fragment := range []string{"FROM activity_facts", "MAX(facts.ingested_at)", "last_ingested_at", "GROUP BY facts.subject_id", "subjects.is_public", "environments.metadata->>'visibility'", "provider_id"} {
		if !strings.Contains(sqlActivityFreshnessQuery, fragment) {
			t.Errorf("activity freshness query missing %q: %s", fragment, sqlActivityFreshnessQuery)
		}
	}
}

func TestSyncJobStatusNormalizesDomainAndDurableStates(t *testing.T) {
	registry := NewRegistry(Resource{Environment: "test"})
	registry.ObserveSyncJob("github", "pending", "manual")
	registry.ObserveSyncJob("github", "queued", "manual")
	registry.ObserveSyncJob("github", "cancelled", "scheduled")

	body := scrape(t, registry)
	for _, fragment := range []string{
		`sync_jobs_total{provider="github",status="queued",trigger="manual",build_sha="unknown",environment="test",region="unknown"} 2`,
		`sync_jobs_total{provider="github",status="cancelled",trigger="scheduled",build_sha="unknown",environment="test",region="unknown"} 1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("sync state metric missing %q:\n%s", fragment, body)
		}
	}
	if strings.Contains(body, `status="pending"`) {
		t.Fatalf("domain pending must normalize to durable queued:\n%s", body)
	}
}

func TestSQLRevocationDLQQueryUsesDurableDeadState(t *testing.T) {
	for _, fragment := range []string{"FROM provider_token_revocation_jobs", "status = 'dead'", "MIN(updated_at)"} {
		if !strings.Contains(sqlRevocationDLQQuery, fragment) {
			t.Errorf("revocation DLQ query missing %q: %s", fragment, sqlRevocationDLQQuery)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func scrape(t *testing.T, registry *Registry) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("metrics content type = %q", contentType)
	}
	return recorder.Body.String()
}
