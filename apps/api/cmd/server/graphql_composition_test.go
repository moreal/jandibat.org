package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
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

func TestDevelopmentHTTPDeletionPortIsTrulyAbsentWithoutDurableStore(t *testing.T) {
	app, err := buildApplication(context.Background(), developmentConfig(t), zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if app.dependencies.SubjectDeletions != nil {
		t.Fatal("development HTTP deletion port is a typed-nil interface")
	}
}

type graphQLTwoSessionAuth struct{ handlers.AuthService }

func (port graphQLTwoSessionAuth) AuthenticateSession(_ context.Context, token string) (auth.User, error) {
	if token != "session-a" && token != "session-b" {
		return auth.User{}, auth.ErrInvalidSession
	}
	return auth.User{ID: "same-user", Status: auth.UserStatusActive}, nil
}

type graphQLTwoSessionLookup struct{ handlers.SessionService }

func (port graphQLTwoSessionLookup) CurrentSession(_ context.Context, token string) (auth.Session, error) {
	switch token {
	case "session-a":
		return auth.Session{ID: "57e40368-8c92-438a-b273-05607dfcae0f", UserID: "same-user", ExpiresAt: time.Now().Add(time.Hour)}, nil
	case "session-b":
		return auth.Session{ID: "ee4dd1e9-b762-4fb6-8572-5bd064e317d1", UserID: "same-user", ExpiresAt: time.Now().Add(time.Hour)}, nil
	default:
		return auth.Session{}, auth.ErrInvalidSession
	}
}

type countingGraphQLMagicLinks struct {
	graph.AuthAccountService
	calls int
}

func (port *countingGraphQLMagicLinks) RequestMagicLink(context.Context, string, string) error {
	port.calls++
	return nil
}

func TestGraphQLFullRouterSharesMutationQuotaAcrossBearerSessions(t *testing.T) {
	app, err := buildApplication(context.Background(), developmentConfig(t), zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	app.dependencies.Auth = graphQLTwoSessionAuth{AuthService: app.dependencies.Auth}
	app.dependencies.Sessions = graphQLTwoSessionLookup{SessionService: app.dependencies.Sessions}
	policies := handlers.DefaultRateLimitPolicies()
	policies["mutation_intent_session"] = handlers.RateLimitPolicy{Limit: 1, Window: time.Hour}
	app.dependencies.RateLimiter = handlers.NewMemoryRateLimiter(policies, nil)
	mutations := &countingGraphQLMagicLinks{AuthAccountService: app.graphql.AuthAccounts}
	app.graphql.AuthAccounts = mutations
	handler, err := newProcessHandlerWithGraphQL(app, operations.NewLiveness(nil))
	if err != nil {
		t.Fatalf("construct GraphQL process handler: %v", err)
	}
	for i, token := range []string{"session-a", "session-b"} {
		request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Request { requestMagicLink(input: {email: \"person@example.test\"}) { accepted errors { code } } }","operationName":"Request"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusOK
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("session %d status=%d body=%s, want %d", i, response.Code, response.Body.String(), want)
		}
	}
	if mutations.calls != 1 {
		t.Fatalf("mutation service calls = %d, want only first call", mutations.calls)
	}
}

func TestGraphQLFullRouterSharesQueryQuotaAcrossBearerSessions(t *testing.T) {
	app, err := buildApplication(context.Background(), developmentConfig(t), zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	app.dependencies.Auth = graphQLTwoSessionAuth{AuthService: app.dependencies.Auth}
	app.dependencies.Sessions = graphQLTwoSessionLookup{SessionService: app.dependencies.Sessions}
	policies := handlers.DefaultRateLimitPolicies()
	policies["public_activity_account"] = handlers.RateLimitPolicy{Limit: 1, Window: time.Hour}
	app.dependencies.RateLimiter = handlers.NewMemoryRateLimiter(policies, nil)
	handler, err := newProcessHandlerWithGraphQL(app, operations.NewLiveness(nil))
	if err != nil {
		t.Fatalf("construct GraphQL process handler: %v", err)
	}
	for i, token := range []string{"session-a", "session-b"} {
		request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query Catalog { providerCatalog { id } }","operationName":"Catalog"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusOK
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("session %d status=%d body=%s, want %d", i, response.Code, response.Body.String(), want)
		}
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
