package apihttp_test

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
)

func TestHTTPRouterExposesOnlyEdgeOperations(t *testing.T) {
	type operation struct{ method, path string }
	want := map[operation]bool{
		{http.MethodGet, "/healthz"}:                                                   true,
		{http.MethodGet, "/metrics"}:                                                   true,
		{http.MethodGet, "/v1/render/{subject}.svg"}:                                   true,
		{http.MethodPost, "/v1/auth/magic-link/consume"}:                               true,
		{http.MethodGet, "/v1/integrations/{provider}/callback"}:                       true,
		{http.MethodPost, "/v1/custom-providers/{customProviderId}/activities:ingest"}: true,
	}
	routes, ok := apihttp.NewRouter().(chi.Routes)
	if !ok {
		t.Fatal("production router does not expose its route inventory")
	}
	err := chi.Walk(routes, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := operation{method, path}
		if !want[key] {
			t.Errorf("unexpected HTTP operation: %s %s", method, path)
		}
		delete(want, key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for missing := range want {
		t.Errorf("missing HTTP edge operation: %s %s", missing.method, missing.path)
	}
}
