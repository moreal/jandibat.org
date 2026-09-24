package apihttp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
)

func TestSensitiveAndErrorResponsesArePrivateByDefault(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "custom ingest", method: http.MethodPost, path: "/v1/custom-providers/provider-1/activities:ingest"},
		{name: "oauth callback error", method: http.MethodGet, path: "/v1/integrations/github/callback?state=secret&code=secret"},
		{name: "magic link consume error", method: http.MethodPost, path: "/v1/auth/magic-link/consume"},
		{name: "svg validation error", method: http.MethodGet, path: "/v1/render/alice.svg?from=not-a-date"},
	}

	router := apihttp.NewRouter()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code == http.StatusNotFound || recorder.Code == http.StatusMethodNotAllowed {
				t.Fatalf("test did not reach the intended operation: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q, want private, no-store", got)
			}
			for _, name := range []string{"Cookie", "Authorization"} {
				if !responseVaryContains(recorder.Header(), name) {
					t.Errorf("Vary does not contain %s: %q", name, recorder.Header().Values("Vary"))
				}
			}
		})
	}
}

func responseVaryContains(header http.Header, wanted string) bool {
	for _, value := range header.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(name), wanted) {
				return true
			}
		}
	}
	return false
}
