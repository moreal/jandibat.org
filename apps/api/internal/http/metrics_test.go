package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"go.uber.org/zap/zapcore"
)

func TestObserveHTTPUsesRoutePatternAndBoundsUnknownMethod(t *testing.T) {
	registry := observability.NewRegistry(observability.Resource{Environment: "test"})
	router := chi.NewRouter()
	router.Use(observeHTTP(registry, nil))
	router.Get("/v1/subjects/{subject}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/subjects/private-user-id", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("ATTACK-private-user-id", "/not-found/private-user-id", nil))

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, fragment := range []string{
		`http_server_requests_total{route="/v1/subjects/{subject}",method="GET",status_class="2xx"`,
		`http_server_requests_total{route="unmatched",method="OTHER",status_class="4xx"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("metrics missing %q\n%s", fragment, body)
		}
	}
	if strings.Contains(body, "private-user-id") || strings.Contains(body, "/not-found/") {
		t.Fatalf("raw request path or method leaked into labels:\n%s", body)
	}
}

func TestRouterLogsCompletedRequestsWithoutRequestVariables(t *testing.T) {
	for _, test := range []struct {
		name          string
		path          string
		requestID     string
		wantStatus    int
		wantOperation string
	}{
		{name: "success", path: "/healthz", requestID: "request-success", wantStatus: http.StatusOK, wantOperation: "GET /healthz"},
		{name: "failure", path: "/missing/private-id?access_token=query-secret", requestID: "request-failure", wantStatus: http.StatusNotFound, wantOperation: "GET unmatched"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger, _, err := observability.NewLogger(observability.Config{
				Service: "api", Resource: observability.Resource{Environment: "production"}, Output: zapcore.AddSync(&output),
			})
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-Request-ID", test.requestID)
			response := httptest.NewRecorder()

			NewRouter(Dependencies{Logger: logger}).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
				t.Fatalf("decode request log: %v: %q", err, output.String())
			}
			if record["event"] != "http.request_completed" || record["request_id"] != test.requestID ||
				record["method"] != http.MethodGet || record["operation"] != test.wantOperation ||
				record["status"] != float64(test.wantStatus) {
				t.Fatalf("request log = %#v", record)
			}
			duration, ok := record["duration"].(string)
			if !ok {
				t.Fatalf("duration = %#v, want encoded duration string", record["duration"])
			}
			if _, err := time.ParseDuration(duration); err != nil {
				t.Fatalf("duration = %q: %v", duration, err)
			}
			for _, forbidden := range []string{"private-id", "access_token", "query-secret"} {
				if strings.Contains(output.String(), forbidden) {
					t.Errorf("request log leaked %q: %s", forbidden, output.String())
				}
			}
		})
	}
}

func TestRouterExposesPrometheusMetrics(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	NewRouter().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "# TYPE http_server_requests_total counter") {
		t.Fatalf("Prometheus family missing:\n%s", recorder.Body.String())
	}
}

func TestRouterRejectsPublicMetricsScrape(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "203.0.113.42:12345"
	recorder := httptest.NewRecorder()
	NewRouter().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("public GET /metrics = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "build_sha") || strings.Contains(recorder.Body.String(), "db_pool") {
		t.Fatalf("public metrics response disclosed operational labels: %s", recorder.Body.String())
	}
}

func TestMetricsAllowsIPv4AndIPv6LoopbackWithoutForwarding(t *testing.T) {
	for _, remote := range []string{"127.0.0.1:12345", "[::1]:12345"} {
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		request.RemoteAddr = remote
		recorder := httptest.NewRecorder()
		NewRouter().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET /metrics from %s = %d, body = %s", remote, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMetricsRejectsForwardedLoopbackRequestAndIgnoresSpoofedXFF(t *testing.T) {
	for _, test := range []struct {
		remote string
		xff    string
	}{
		{remote: "127.0.0.1:12345", xff: "198.51.100.1"},
		{remote: "198.51.100.2:12345", xff: "127.0.0.1"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", test.xff)
		recorder := httptest.NewRecorder()
		NewRouter(Dependencies{TrustProxyHeaders: true}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("GET /metrics remote=%s XFF=%s = %d", test.remote, test.xff, recorder.Code)
		}
	}
}

func TestTrustedProxyHeadersRejectsDuplicatePhysicalFieldLines(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.2:12345"
	request.Header.Add("X-Real-IP", "127.0.0.1")
	request.Header.Add("X-Real-IP", "198.51.100.3")
	recorder := httptest.NewRecorder()
	var remoteAddress string

	trustedProxyHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteAddress = r.RemoteAddr
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)

	if remoteAddress != "198.51.100.2:12345" {
		t.Fatalf("duplicate X-Real-IP changed remote address to %q", remoteAddress)
	}
}

func TestObservedCustomIngestCountsEveryEventOutcome(t *testing.T) {
	registry := observability.NewRegistry(observability.Resource{Environment: "test"})
	service := observedCustomProviders{
		next:    customIngestFake{result: integrations.IngestCustomActivitiesResult{Accepted: 4, Duplicate: 2, Rejected: 1}},
		metrics: registry,
	}
	result, err := service.Ingest(context.Background(), integrations.IngestCustomActivitiesInput{})
	if err != nil || result.Accepted != 4 || result.Duplicate != 2 || result.Rejected != 1 {
		t.Fatalf("Ingest() = %#v, %v", result, err)
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, fragment := range []string{
		`custom_ingest_events_total{outcome="accepted",build_sha="unknown",environment="test",region="unknown"} 4`,
		`custom_ingest_events_total{outcome="duplicate",build_sha="unknown",environment="test",region="unknown"} 2`,
		`custom_ingest_events_total{outcome="rejected",build_sha="unknown",environment="test",region="unknown"} 1`,
	} {
		if !strings.Contains(recorder.Body.String(), fragment) {
			t.Errorf("custom ingest metric missing %q:\n%s", fragment, recorder.Body.String())
		}
	}
}

type customIngestFake struct {
	integrationsCustomProviderService
	result integrations.IngestCustomActivitiesResult
	err    error
}

func (service customIngestFake) Ingest(context.Context, integrations.IngestCustomActivitiesInput) (integrations.IngestCustomActivitiesResult, error) {
	return service.result, service.err
}
