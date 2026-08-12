package processruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestHealthHandlerSeparatesLivenessAndReadiness(t *testing.T) {
	shutdown := make(chan struct{})
	checker, err := operations.NewReadinessChecker(time.Second, operations.ReadinessDependency{
		Name: "database", Probe: operations.DependencyProbeFunc(func(context.Context) error { return context.DeadlineExceeded }),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHealthHandler(operations.NewLiveness(shutdown), checker)

	assertHealthStatus(t, handler, "/livez", http.StatusOK, `"live":true`)
	assertHealthStatus(t, handler, "/readyz", http.StatusServiceUnavailable, `"Ready":false`)
	assertHealthStatus(t, handler, "/healthz", http.StatusServiceUnavailable, `"Name":"database"`)
	close(shutdown)
	assertHealthStatus(t, handler, "/livez", http.StatusServiceUnavailable, `"live":false`)
}

func assertHealthStatus(t *testing.T, handler http.Handler, path string, wantStatus int, bodyFragment string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != wantStatus || !strings.Contains(recorder.Body.String(), bodyFragment) {
		t.Fatalf("GET %s = %d %s", path, recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q", path, recorder.Header().Get("Cache-Control"))
	}
}
