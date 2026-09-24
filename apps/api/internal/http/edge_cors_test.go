package apihttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSPreflightRejectsRemovedRESTOperations(t *testing.T) {
	router := NewRouter(Dependencies{AllowedOrigins: []string{"https://app.example"}})
	for _, test := range []struct{ path, method string }{
		{"/v1/subjects", http.MethodPost},
		{"/v1/auth/magic-link/request", http.MethodPost},
		{"/v1/render/alice.svg", http.MethodPost},
		{"/graphql", http.MethodPost},
	} {
		request := httptest.NewRequest(http.MethodOptions, test.path, nil)
		request.Header.Set("Origin", "https://app.example")
		request.Header.Set("Access-Control-Request-Method", test.method)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code == http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("removed operation %s %s received CORS grant: status=%d headers=%v", test.method, test.path, response.Code, response.Header())
		}
	}
}

func TestCORSPreflightPreservesMountedEdgeOperations(t *testing.T) {
	router := NewRouter(Dependencies{AllowedOrigins: []string{"https://app.example"}})
	for _, test := range []struct{ path, method string }{
		{"/healthz", http.MethodGet},
		{"/v1/render/alice.svg", http.MethodGet},
		{"/v1/auth/magic-link/consume", http.MethodPost},
		{"/v1/integrations/github/callback", http.MethodGet},
		{"/v1/custom-providers/provider-1/activities:ingest", http.MethodPost},
	} {
		request := httptest.NewRequest(http.MethodOptions, test.path, nil)
		request.Header.Set("Origin", "https://app.example")
		request.Header.Set("Access-Control-Request-Method", test.method)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://app.example" || response.Header().Get("Access-Control-Allow-Methods") != test.method {
			t.Errorf("edge preflight %s %s = status=%d headers=%v", test.method, test.path, response.Code, response.Header())
		}
	}
}
