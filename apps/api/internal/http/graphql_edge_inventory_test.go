package apihttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestGraphQLProductionRouterExposesOnlyDomainAndEdgeOperations(t *testing.T) {
	authService := &auth.Service{}
	subjectService := &subjects.Service{}
	connections := &integrations.ConnectionService{}
	customProviders := &integrations.CustomProviderService{}
	sync := &integrations.SyncService{}
	audit, err := operations.NewAuditRecorder(operations.NewMemoryAuditSink())
	if err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{Auth: authService, Sessions: authService, Audit: audit, RateLimiter: handlers.DefaultRateLimiter()}
	graphDeps := GraphQLDependencies{
		NodeServices: graph.NodeServices{
			ViewerUsers: subjectService, SessionPages: authService, Subjects: subjectService,
			Connections: connections, CustomProviders: customProviders, SyncJobs: sync,
			Sessions: authService, Activity: &appactivity.GetTimeline{},
		},
		SubjectQueries: graph.SubjectQueryServices{
			Pages: subjectService, UserSettings: subjectService, SubjectSettings: subjectService,
		},
		SubjectMutations:        graph.SubjectMutationServices{Subjects: subjectService, Deletions: &operations.DeletionRequester{}},
		IntegrationQueries:      graph.IntegrationQueryServices{Connections: connections, CustomProviders: customProviders},
		ConnectionMutations:     graph.ConnectionMutationServices{Connections: connections, Sync: sync},
		CustomProviderMutations: graph.CustomProviderMutationServices{Providers: customProviders},
		SyncJobs:                sync, AuthAccounts: authService, Passkeys: authService,
		AuthFailureReporter: graphQLAuthFailureStub{}, Development: true,
	}
	router, err := NewRouterWithGraphQL(deps, graphDeps)
	if err != nil {
		t.Fatalf("complete GraphQL dependencies rejected: %v", err)
	}
	type operation struct{ method, path string }
	want := map[operation]bool{
		{http.MethodGet, "/healthz"}:                                                   true,
		{http.MethodGet, "/metrics"}:                                                   true,
		{http.MethodPost, "/graphql"}:                                                  true,
		{http.MethodGet, "/v1/render/{subject}.svg"}:                                   true,
		{http.MethodPost, "/v1/auth/magic-link/consume"}:                               true,
		{http.MethodGet, "/v1/integrations/{provider}/callback"}:                       true,
		{http.MethodPost, "/v1/custom-providers/{customProviderId}/activities:ingest"}: true,
	}
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatal("GraphQL production router does not expose chi routes")
	}
	if err := chi.Walk(routes, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := operation{method, path}
		if !want[key] {
			t.Errorf("unexpected HTTP operation: %s %s", method, path)
		}
		delete(want, key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for missing := range want {
		t.Errorf("missing HTTP operation: %s %s", missing.method, missing.path)
	}

	request := httptest.NewRequest(http.MethodOptions, "/graphql", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	deps.AllowedOrigins = []string{"https://app.example"}
	router, err = NewRouterWithGraphQL(deps, graphDeps)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Methods") != http.MethodPost {
		t.Errorf("GraphQL preflight = status=%d headers=%v", response.Code, response.Header())
	}
}
