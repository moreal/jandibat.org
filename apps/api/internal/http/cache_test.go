package apihttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCachePrivateByDefault(t *testing.T) {
	tests := []struct {
		name         string
		cacheControl string
		vary         string
		wantCache    string
		wantOrigin   bool
		wantCookie   bool
		wantAuth     bool
	}{
		{name: "implicit policy", wantCache: privateNoStore, wantCookie: true, wantAuth: true},
		{name: "explicit private", cacheControl: "private, no-store", vary: "Origin, Cookie", wantCache: "private, no-store", wantOrigin: true, wantCookie: true, wantAuth: true},
		{name: "explicit public", cacheControl: "public, max-age=300", vary: "Origin, Cookie, Authorization", wantCache: "public, max-age=300", wantOrigin: true},
		{name: "contradictory public remains private", cacheControl: "public, no-store", wantCache: "public, no-store", wantCookie: true, wantAuth: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := cachePrivateByDefault(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.cacheControl != "" {
					w.Header().Set("Cache-Control", test.cacheControl)
				}
				if test.vary != "" {
					w.Header().Set("Vary", test.vary)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

			if got := recorder.Header().Get("Cache-Control"); got != test.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", got, test.wantCache)
			}
			for name, want := range map[string]bool{
				"Origin": test.wantOrigin, "Cookie": test.wantCookie, "Authorization": test.wantAuth,
			} {
				if got := varyContains(recorder.Header(), name); got != want {
					t.Errorf("Vary contains %s = %v, want %v; values=%q", name, got, want, recorder.Header().Values("Vary"))
				}
			}
		})
	}
}

func TestCachePrivateByDefaultCoversEmptyResponses(t *testing.T) {
	recorder := httptest.NewRecorder()
	cachePrivateByDefault(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if got := recorder.Header().Get("Cache-Control"); got != privateNoStore {
		t.Fatalf("Cache-Control = %q, want %q", got, privateNoStore)
	}
}
