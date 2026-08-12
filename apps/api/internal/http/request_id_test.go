package apihttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouterReplacesHostileRequestIDsBeforeCSRFProblem(t *testing.T) {
	for name, inbound := range map[string]string{
		"quote":     `attacker"break`,
		"backslash": `attacker\break`,
		"oversize":  strings.Repeat("a", maxRequestIDLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/subjects", nil)
			request.Header.Set("X-Request-ID", inbound)
			request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "session"})
			response := httptest.NewRecorder()
			NewRouter().ServeHTTP(response, request)

			if response.Code != http.StatusForbidden || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("response = %d %#v", response.Code, response.Header())
			}
			var problem struct {
				Status    int    `json:"status"`
				Code      string `json:"code"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatalf("CSRF problem is not valid JSON: %v; body=%q", err, response.Body.String())
			}
			got := response.Header().Get("X-Request-ID")
			if problem.Status != http.StatusForbidden || problem.Code != "csrf" || got == "" || got != problem.RequestID ||
				got == inbound || !validRequestID(got) {
				t.Fatalf("problem/header request ID = %#v / %q, inbound %q", problem, got, inbound)
			}
		})
	}
}

func TestRouterPreservesValidBoundedRequestID(t *testing.T) {
	const inbound = "request-correlation_2026.08/12:abc"
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", inbound)
	response := httptest.NewRecorder()
	NewRouter().ServeHTTP(response, request)
	if got := response.Header().Get("X-Request-ID"); got != inbound {
		t.Fatalf("X-Request-ID = %q, want %q", got, inbound)
	}
}
