package apihttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRecoverProblemsUsesSanitizedRFC9457Response(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	const panicSecret = "provider password=do-not-leak"
	handler := middleware.RequestID(recoverProblems(zap.New(core))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "999")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("ETag", `"stale"`)
		panic(panicSecret)
	})))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	assertFrameworkProblem(t, recorder, http.StatusInternalServerError, "internal_error")
	if strings.Contains(recorder.Body.String(), panicSecret) {
		t.Fatalf("panic detail leaked in response: %s", recorder.Body.String())
	}
	entries := logs.All()
	if len(entries) != 1 || entries[0].Message != "http.panic_recovered" {
		t.Fatalf("panic log entries = %#v", entries)
	}
	for _, field := range entries[0].ContextMap() {
		value, _ := field.(string)
		if strings.Contains(value, panicSecret) || strings.Contains(value, "password") {
			t.Fatalf("panic detail leaked in logs: %#v", entries)
		}
	}
	for _, name := range []string{"Content-Length", "Content-Encoding", "ETag"} {
		if got := recorder.Header().Get(name); got != "" {
			t.Fatalf("stale %s survived panic response: %q", name, got)
		}
	}
}

func TestTimeoutProblemsUsesRFC9457Response(t *testing.T) {
	handler := middleware.RequestID(timeoutProblems(time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/slow", nil))

	assertFrameworkProblem(t, recorder, http.StatusServiceUnavailable, "service_unavailable")
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
}

func assertFrameworkProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Type      string `json:"type"`
		Status    int    `json:"status"`
		Code      string `json:"code"`
		Instance  string `json:"instance"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Status != status || body.Code != code || body.Type != "https://jandibat.org/problems/"+code || body.Instance == "" || body.RequestID == "" {
		t.Fatalf("problem = %+v", body)
	}
	if got := recorder.Header().Get("X-Request-ID"); got != body.RequestID {
		t.Fatalf("X-Request-ID = %q, body requestId = %q", got, body.RequestID)
	}
}
