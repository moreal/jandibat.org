package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestGraphQLProcessCompositionMountsOnlyCompleteRuntime(t *testing.T) {
	settings := developmentConfig(t)
	app, err := buildApplication(context.Background(), settings, zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	handler, err := newProcessHandlerWithGraphQL(app, operations.NewLiveness(nil))
	if err != nil {
		t.Fatalf("construct GraphQL process handler: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query Contract { _contract }"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"_contract":true`) {
		t.Fatalf("GraphQL status=%d body=%s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/healthz", "/v1/providers"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s with GraphQL mounted = %d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestGraphQLFailureReporterLogsFixedEventsWithoutSensitiveContext(t *testing.T) {
	core, observed := observer.New(zap.ErrorLevel)
	reporter := graphqlFailureReporter{logger: zap.New(core)}
	ctx := context.Background()
	reporter.ReportMagicLinkRequestFailure(ctx)
	reporter.ReportSessionCompensationFailure(ctx)
	reporter.ReportOAuthCompensationFailure(ctx)
	if observed.Len() != 3 {
		t.Fatalf("failure events = %d, want 3", observed.Len())
	}
	want := []string{
		"graphql.auth.magic_link_request_failed",
		"graphql.auth.session_compensation_failed",
		"graphql.integration.oauth_compensation_failed",
	}
	for i, entry := range observed.All() {
		if entry.Message != want[i] || len(entry.Context) != 0 {
			t.Fatalf("failure event %d = %#v; must contain only fixed event name", i, entry)
		}
	}
}

func TestGraphQLCompositionRequiresDurableDeletionRequesterInProduction(t *testing.T) {
	settings := productionConfig(t)
	if _, err := buildGraphQLDependencies(settings, zap.NewNop(), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("production GraphQL composed without durable deletion requester")
	}
}

func TestGraphQLDevelopmentDeletionPortFailsClosed(t *testing.T) {
	if _, err := (unavailableDeletionRequester{}).Request(context.Background(), "request", "subject", "subject-id"); err == nil {
		t.Fatal("development memory-mode deletion request appeared durable")
	}
}

func TestGraphQLProcessCompositionFailsClosedWhenRequiredPortMissing(t *testing.T) {
	settings := developmentConfig(t)
	app, err := buildApplication(context.Background(), settings, zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	app.graphql.AuthFailureReporter = nil
	if _, err := newProcessHandlerWithGraphQL(app, operations.NewLiveness(nil)); err == nil {
		t.Fatal("GraphQL route mounted without an auth failure reporter")
	}
}
