package apihttp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
)

func TestHealthz(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	apihttp.NewRouter().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	contentType := recorder.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}

	var payload struct {
		Status       string            `json:"status"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}

	if payload.Status != "ok" || payload.Dependencies == nil {
		t.Fatalf("unexpected health payload: %#v", payload)
	}
}

func TestListProviders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/providers", nil)
	recorder := httptest.NewRecorder()

	apihttp.NewRouter().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	var payload struct {
		Providers []map[string]any `json:"providers"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}

	if len(payload.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(payload.Providers))
	}
	for _, provider := range payload.Providers {
		methods, ok := provider["authMethods"].([]any)
		if !ok {
			t.Fatalf("authMethods = %#v", provider["authMethods"])
		}
		hasNone := false
		for _, method := range methods {
			hasNone = hasNone || method == "none"
		}
		if !hasNone {
			t.Errorf("provider %v does not advertise none: %#v", provider["id"], methods)
		}
		if (provider["id"] == "github" || provider["id"] == "codeberg") && provider["supportsPrivateData"] != false {
			t.Errorf("provider %v advertises unsupported private data", provider["id"])
		}
	}
}

func TestGetActivities(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/activities/moreal", nil)
	recorder := httptest.NewRecorder()

	apihttp.NewRouter().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("expected problem response, got %q", recorder.Header().Get("Content-Type"))
	}
}
