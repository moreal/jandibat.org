package apihttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestGraphQLMutationAuditTargetComesOnlyFromTrustedPublisher(t *testing.T) {
	const canonicalID = "8d28dfdb-a447-48fd-953d-c94d34719e29"
	const spoofedID = "20e077f8-ddad-4ae1-947d-646569a76d17"
	for _, test := range []struct {
		name       string
		publish    bool
		invalid    bool
		wantTarget operations.AuditTarget
	}{
		{"trusted target", true, false, operations.AuditTarget{Type: "subject", ID: canonicalID}},
		{"no publication", false, false, operations.AuditTarget{Type: "graphql"}},
		{"invalid direct target", false, true, operations.AuditTarget{Type: "graphql"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := operations.NewMemoryAuditSink()
			recorder, err := operations.NewAuditRecorder(sink)
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			router.Use(middleware.RequestID)
			router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
			router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
			router.Post("/graphql", graphQLTrustedContext(Dependencies{}, GraphQLDependencies{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.publish {
					graph.PublishMutationAuditTarget(r.Context(), "subject", canonicalID)
				}
				if test.invalid {
					publishGraphQLMutationAuditTarget(r.Context(), operations.AuditTarget{Type: "subject", ID: "not-a-durable-id?token=secret"})
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP)
			request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"operationName":"Change","query":"mutation Change { attackerAlias: _contract }","variables":{"targetID":"`+spoofedID+`","secret":"never-audit-me"}}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			events, err := sink.Events(context.Background())
			if err != nil || len(events) != 2 {
				t.Fatalf("events = %#v, %v", events, err)
			}
			if events[0].Target != (operations.AuditTarget{Type: "graphql"}) || events[1].Target != test.wantTarget {
				t.Fatalf("intent/outcome targets = %#v, %#v", events[0].Target, events[1].Target)
			}
			for _, event := range events {
				for _, value := range event.Metadata {
					if text, ok := value.(string); ok && (strings.Contains(text, spoofedID) || strings.Contains(text, "never-audit-me") || strings.Contains(text, "attackerAlias")) {
						t.Fatalf("client value in audit metadata: %#v", event.Metadata)
					}
				}
			}
		})
	}
}

func TestGraphQLPreflightRejectsCompoundMutationBeforeAudit(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler { return graph.PreflightHTTP(next, graph.HTTPOptions{}) })
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
	router.Post("/graphql", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"operationName":"Change","query":"mutation Change { first: _contract second: _contract }"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("compound mutation status = %d", response.Code)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 0 {
		t.Fatalf("compound mutation audit events = %#v, %v", events, err)
	}
}
