package apihttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	adapteroauth "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Removing operation-aware classification makes a read-only GraphQL POST
// create a durable mutation intent, and lets an actual mutation bypass one.
func TestGraphQLAuditClassifiesOperationNotHTTPPost(t *testing.T) {
	for _, tc := range []struct {
		name       string
		operation  string
		query      string
		wantEvents int
	}{
		{"query", "Viewer", `query Viewer { viewer { id } }`, 0},
		{"mutation", "Change", `mutation Change { signOut { errors { code } } }`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := operations.NewMemoryAuditSink()
			recorder, err := operations.NewAuditRecorder(sink)
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			router.Use(middleware.RequestID)
			router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
			router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
			router.Post("/graphql", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":`+quotedGraphQL(tc.query)+`,"operationName":"`+tc.operation+`"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			events, err := sink.Events(context.Background())
			if err != nil || len(events) != tc.wantEvents {
				t.Fatalf("events=%#v err=%v; want count %d", events, err, tc.wantEvents)
			}
		})
	}
}

// A domain payload error with HTTP 200 must not commit a mutation transaction
// or produce a successful audit outcome.
func TestGraphQLTypedMutationErrorRollsBackAndAuditsFailure(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	transaction := &fakeMutationAuditTransaction{active: true}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	router.Post("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"signOut":{"errors":[{"code":"NOT_FOUND"}]}}}`))
	})
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Change { signOut { errors { code } } }","operationName":"Change"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || transaction.commits != 0 || transaction.rollbacks == 0 {
		t.Fatalf("status=%d commits=%d rollbacks=%d", response.Code, transaction.commits, transaction.rollbacks)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 || events[1].Outcome != operations.AuditFailed {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

// Selecting only `accepted` under an alias must not hide a typed domain
// failure from the transactional audit boundary.
func TestGraphQLAliasedMutationCannotHideTypedFailure(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	transaction := &fakeMutationAuditTransaction{active: true}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	router.Method(http.MethodPost, "/graphql", graph.NewHTTPHandler(&graph.Resolver{}, graph.HTTPOptions{}))
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Alias { concealed: requestMagicLink(input: {email: \"invalid\"}) { accepted } }","operationName":"Alias"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || transaction.commits != 0 || transaction.rollbacks == 0 {
		t.Fatalf("response=%d %s commits=%d rollbacks=%d", response.Code, response.Body.String(), transaction.commits, transaction.rollbacks)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 || events[1].Outcome != operations.AuditFailed {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

type graphQLAccountPortStub struct{ graph.AuthAccountService }

func (graphQLAccountPortStub) RequestMagicLink(context.Context, string, string) error { return nil }

type graphQLAuthFailureStub struct{}

func (graphQLAuthFailureStub) ReportMagicLinkRequestFailure(context.Context)    {}
func (graphQLAuthFailureStub) ReportSessionCompensationFailure(context.Context) {}

func TestGraphQLSuccessfulAliasedMutationCommitsTrustedOutcome(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	transaction := &fakeMutationAuditTransaction{active: true}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	graphDeps := GraphQLDependencies{AuthAccounts: graphQLAccountPortStub{}, AuthFailureReporter: graphQLAuthFailureStub{}}
	router.Method(http.MethodPost, "/graphql", graphQLTrustedContext(Dependencies{}, graphDeps, graph.NewHTTPHandler(&graph.Resolver{}, graph.HTTPOptions{})))
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Alias { concealed: requestMagicLink(input: {email: \"valid@example.test\"}) { accepted } }","operationName":"Alias"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accepted":true`) || transaction.commits != 1 {
		t.Fatalf("response=%d %s commits=%d", response.Code, response.Body.String(), transaction.commits)
	}
	if len(transaction.events) != 1 || transaction.events[0].Outcome != operations.AuditSucceeded {
		t.Fatalf("audit events=%#v", transaction.events)
	}
}

func quotedGraphQL(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `\"`) + `"`
}

// Missing trusted ports must stop startup rather than mount a partially
// functional GraphQL surface with anonymous fallbacks.
func TestGraphQLRouterRejectsMissingTrustedDependencies(t *testing.T) {
	if _, err := NewRouterWithGraphQL(Dependencies{}, GraphQLDependencies{}); err == nil {
		t.Fatal("GraphQL router accepted missing authentication, service and audit dependencies")
	}
}

// An interface containing a typed nil pointer is non-nil to Go. Treat it as
// missing at startup rather than mounting a route that panics on its first use.
func TestGraphQLRouterRejectsTypedNilRequiredPorts(t *testing.T) {
	var authPort *auth.Service
	var subjectPort *subjects.Service
	var connectionPort *integrations.ConnectionService
	var customPort *integrations.CustomProviderService
	var syncPort *integrations.SyncService
	var timelinePort *appactivity.GetTimeline
	var deletionPort *operations.DeletionRequester
	var auditPort *operations.AuditRecorder
	deps := Dependencies{Auth: authPort, Sessions: authPort, Audit: auditPort, RateLimiter: handlers.DefaultRateLimiter()}
	graphDeps := GraphQLDependencies{
		NodeServices: graph.NodeServices{
			ViewerUsers: subjectPort, SessionPages: authPort, Subjects: subjectPort,
			Connections: connectionPort, CustomProviders: customPort, SyncJobs: syncPort,
			Sessions: authPort, Activity: timelinePort,
		},
		SubjectQueries:          graph.SubjectQueryServices{Pages: subjectPort, UserSettings: subjectPort, SubjectSettings: subjectPort},
		SubjectMutations:        graph.SubjectMutationServices{Subjects: subjectPort, Deletions: deletionPort},
		IntegrationQueries:      graph.IntegrationQueryServices{Connections: connectionPort, CustomProviders: customPort},
		ConnectionMutations:     graph.ConnectionMutationServices{Connections: connectionPort, Sync: syncPort},
		CustomProviderMutations: graph.CustomProviderMutationServices{Providers: customPort},
		SyncJobs:                syncPort, AuthAccounts: authPort, Passkeys: authPort,
		AuthFailureReporter: graphQLAuthFailureStub{}, Development: true,
	}
	if _, err := NewRouterWithGraphQL(deps, graphDeps); err == nil {
		t.Fatal("GraphQL router accepted typed-nil required service ports")
	}
}

type graphQLAuthStub struct{ handlers.AuthService }

func (graphQLAuthStub) AuthenticateSession(_ context.Context, token string) (auth.User, error) {
	if token != "valid-token" {
		return auth.User{}, auth.ErrInvalidSession
	}
	return auth.User{ID: "user-1"}, nil
}

type graphQLSessionsStub struct{}

func (graphQLSessionsStub) CurrentSession(_ context.Context, token string) (auth.Session, error) {
	if token != "valid-token" {
		return auth.Session{}, auth.ErrInvalidSession
	}
	return auth.Session{ID: "00000000-0000-4000-8000-000000000001", UserID: "user-1"}, nil
}
func (graphQLSessionsStub) ListSessions(context.Context, string) ([]auth.Session, error) {
	return nil, nil
}
func (graphQLSessionsStub) RevokeOtherSessions(context.Context, string, string) error { return nil }
func (graphQLSessionsStub) RevokeSessionByID(context.Context, string, string) error   { return nil }

type graphQLSharedUserAuth struct{ handlers.AuthService }

func (graphQLSharedUserAuth) AuthenticateSession(_ context.Context, token string) (auth.User, error) {
	if token != "session-a" && token != "session-b" {
		return auth.User{}, auth.ErrInvalidSession
	}
	return auth.User{ID: "shared-user"}, nil
}

type graphQLSharedUserSessions struct{ handlers.SessionService }

func (graphQLSharedUserSessions) CurrentSession(_ context.Context, token string) (auth.Session, error) {
	if token == "session-a" {
		return auth.Session{ID: "00000000-0000-4000-8000-000000000001", UserID: "shared-user"}, nil
	}
	if token == "session-b" {
		return auth.Session{ID: "00000000-0000-4000-8000-000000000002", UserID: "shared-user"}, nil
	}
	return auth.Session{}, auth.ErrInvalidSession
}

type graphQLSharedAccountLimiter struct {
	accounts map[string]int
	failure  error
}

func (limiter *graphQLSharedAccountLimiter) Allow(_ context.Context, scope, key string) (bool, time.Duration, error) {
	if scope == "mutation_intent_session" {
		if limiter.failure != nil {
			return false, 0, limiter.failure
		}
		limiter.accounts[key]++
		return limiter.accounts[key] == 1, time.Minute, nil
	}
	return true, 0, nil
}

func TestGraphQLTrustedContextFailsClosedOnAccountLimiterError(t *testing.T) {
	limiter := &graphQLSharedAccountLimiter{accounts: make(map[string]int), failure: errors.New("limiter unavailable")}
	deps := Dependencies{Auth: graphQLSharedUserAuth{}, Sessions: graphQLSharedUserSessions{}, RateLimiter: limiter}
	executed := false
	handler := graph.PreflightHTTP(graphQLTrustedContext(deps, GraphQLDependencies{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		executed = true
		w.WriteHeader(http.StatusNoContent)
	})), graph.HTTPOptions{})
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Change { signOut { errors { code } } }","operationName":"Change"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer session-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || executed || strings.Contains(response.Body.String(), "limiter unavailable") {
		t.Fatalf("status=%d executed=%t body=%s", response.Code, executed, response.Body.String())
	}
}

// A new bearer session must not reset the verified user's mutation quota.
func TestGraphQLTrustedContextSharesMutationQuotaAcrossSessions(t *testing.T) {
	limiter := &graphQLSharedAccountLimiter{accounts: make(map[string]int)}
	deps := Dependencies{Auth: graphQLSharedUserAuth{}, Sessions: graphQLSharedUserSessions{}, RateLimiter: limiter}
	handler := graph.PreflightHTTP(graphQLTrustedContext(deps, GraphQLDependencies{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})), graph.HTTPOptions{})
	for index, token := range []string{"session-a", "session-b"} {
		request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Change { signOut { errors { code } } }","operationName":"Change"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if index == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("session %d status=%d body=%s, want %d", index, response.Code, response.Body.String(), want)
		}
	}
	if len(limiter.accounts) != 1 {
		t.Fatalf("two sessions used separate account buckets: %#v", limiter.accounts)
	}
}

type graphQLViewerUsersStub struct{}

func (graphQLViewerUsersStub) GetCurrentUser(_ context.Context, actor string) (subjects.User, error) {
	return subjects.User{ID: actor, Status: subjects.UserStatusActive}, nil
}

// A malformed bearer cannot fall back to a valid cookie or to anonymous
// public access. Verified user identity must come from the active session.
func TestGraphQLTrustedContextDoesNotDowngradeBadBearer(t *testing.T) {
	deps := Dependencies{Auth: graphQLAuthStub{}, Sessions: graphQLSessionsStub{}, RateLimiter: handlers.DefaultRateLimiter()}
	graphDeps := GraphQLDependencies{NodeServices: graph.NodeServices{ViewerUsers: graphQLViewerUsersStub{}}}
	handler := graph.PreflightHTTP(graphQLTrustedContext(deps, graphDeps, graph.NewHTTPHandler(&graph.Resolver{}, graph.HTTPOptions{})), graph.HTTPOptions{})
	for _, tc := range []struct{ name, bearer, want string }{
		{"valid", "Bearer valid-token", `"id":"user-1"`},
		{"invalid bearer despite valid cookie", "Bearer invalid-token", `"errors"`},
		{"malformed bearer despite valid cookie", "Basic invalid-token", `"errors"`},
		{"blank authorization despite valid cookie", "   ", `"errors"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query Viewer { viewer { user { id } } }","operationName":"Viewer"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", tc.bearer)
			request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "valid-token"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("response=%d %s, want %s", response.Code, response.Body.String(), tc.want)
			}
		})
	}
}

// The audit middleware sits outside authentication; its correlated outcome
// must still name the server-verified actor, never a GraphQL argument.
func TestGraphQLMutationAuditUsesVerifiedSessionActor(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{Auth: graphQLAuthStub{}, Sessions: graphQLSessionsStub{}, RateLimiter: handlers.DefaultRateLimiter()}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
	router.Method(http.MethodPost, "/graphql", graphQLTrustedContext(deps, GraphQLDependencies{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"signOut":{"errors":[]}}}`))
	})))
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation Change { signOut { errors { code } } }","operationName":"Change"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer valid-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 || events[1].Actor.ID != "user-1" || events[1].Actor.Type != operations.AuditActorUser {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

// An arbitrary Authorization header cannot disable origin checks for a
// browser cookie mutation on the GraphQL endpoint.
func TestGraphQLCookieMutationRejectsCrossOriginDespiteAuthorizationHeader(t *testing.T) {
	handler := csrf([]string{"https://app.example.test"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "valid-token"})
	request.Header.Set("Authorization", "Basic attacker")
	request.Header.Set("Origin", "https://evil.example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie mutation status=%d, want 403", response.Code)
	}
}

// A client controls operationName, so logs must record only a bounded,
// allowlisted label and never query text or variables.
func TestGraphQLOperationLogRedactsUnrecognizedNameAndVariables(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
	router.Use(func(next http.Handler) http.Handler {
		return graphQLOperationObserver(next, observability.NewRegistry(observability.Resource{Environment: "test"}), logger)
	})
	router.Method(http.MethodPost, "/graphql", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query email_secret { viewer { user { id } } }","operationName":"email_secret","variables":{"token":"private-token"}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	entries := observed.All()
	if len(entries) != 1 || entries[0].Message != "graphql.operation_completed" || entries[0].ContextMap()["operation_type"] != "query" || entries[0].ContextMap()["operation_name"] != "other" {
		t.Fatalf("operation logs=%#v", entries)
	}
	if strings.Contains(entries[0].ContextMap()["operation_name"].(string), "secret") || strings.Contains(entries[0].Message, "private-token") {
		t.Fatalf("operation log leaked request: %#v", entries[0])
	}
}

type graphQLOAuthConnectionStub struct {
	handlers.ConnectionService
	begin   int
	revoked string
}

func (port *graphQLOAuthConnectionStub) BeginOAuth(_ context.Context, input integrations.ConnectInput) (integrations.ProviderConnection, error) {
	port.begin++
	return integrations.ProviderConnection{ID: "pending-1", SubjectID: input.SubjectID, ProviderID: input.ProviderID, AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionPending}, nil
}
func (port *graphQLOAuthConnectionStub) Revoke(_ context.Context, id string) (integrations.ProviderConnection, error) {
	port.revoked = id
	return integrations.ProviderConnection{ID: id, Status: integrations.ConnectionRevoked}, nil
}

type graphQLOAuthFlowStub struct {
	adapteroauth.Flow
	request adapteroauth.AuthorizationRequest
	fail    bool
}

func (flow *graphQLOAuthFlowStub) ProviderID() string { return "github" }
func (flow *graphQLOAuthFlowStub) Begin(_ context.Context, request adapteroauth.AuthorizationRequest) (adapteroauth.AuthorizationResult, error) {
	flow.request = request
	if flow.fail {
		return adapteroauth.AuthorizationResult{}, errors.New("state unavailable")
	}
	return adapteroauth.AuthorizationResult{URL: "https://provider.example/authorize"}, nil
}

func TestGraphQLOAuthStarterUsesCookieBindingAndRevokesFailedState(t *testing.T) {
	connections := &graphQLOAuthConnectionStub{}
	flow := &graphQLOAuthFlowStub{fail: true}
	redirect := "https://app.example/return"
	starter := cookieBoundOAuthStarter{
		deps:  Dependencies{Auth: graphQLAuthStub{}, Sessions: graphQLSessionsStub{}, Connections: connections, AllowedRedirects: []string{redirect}},
		flows: map[string]adapteroauth.Flow{"github": flow}, token: "valid-token", userID: "user-1", sessionID: "00000000-0000-4000-8000-000000000001",
	}
	input := integrations.ConnectInput{SubjectID: "subject-1", ProviderID: "github"}
	if _, _, err := starter.Start(context.Background(), input, "https://evil.example/return"); err == nil || connections.begin != 0 {
		t.Fatalf("unlisted redirect reached pending creation: err=%v begin=%d", err, connections.begin)
	}
	if connection, url, err := starter.Start(context.Background(), input, redirect); err == nil || connection.ID != "" || url != "" || connections.revoked != "pending-1" {
		t.Fatalf("failed OAuth state was not compensated: connection=%#v url=%q err=%v revoke=%q", connection, url, err, connections.revoked)
	}
	if flow.request.SessionBinding != "valid-token" || flow.request.ClientRedirectURI != redirect {
		t.Fatalf("OAuth request did not bind exact cookie session and redirect: %#v", flow.request)
	}
}
