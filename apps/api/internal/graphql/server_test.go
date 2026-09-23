package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

func graphRequest(method, contentType, body string) *http.Request {
	r := httptest.NewRequest(method, "/graphql", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestGraphQLHTTPPreflightRejectsUnsafeTransport(t *testing.T) {
	handler := NewHTTPHandler(&Resolver{}, HTTPOptions{})
	for _, tc := range []struct {
		name, method, contentType, body string
		want                            int
	}{
		{"get", "GET", "application/json", `{}`, http.StatusMethodNotAllowed},
		{"websocket", "POST", "application/json", `{"query":"query Safe { _contract }"}`, http.StatusBadRequest},
		{"wrong content type", "POST", "text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"non utf8 charset", "POST", "application/json; charset=iso-8859-1", `{}`, http.StatusUnsupportedMediaType},
		{"batch", "POST", "application/json", `[{"query":"query Safe { _contract }"}]`, http.StatusBadRequest},
		{"trailing object", "POST", "application/json", `{"query":"query Safe { _contract }"}{"x":1}`, http.StatusBadRequest},
		{"malformed body", "POST", "application/json", `{"secret":"canary-secret"`, http.StatusBadRequest},
		{"invalid utf8", "POST", "application/json", "{\"query\":\"query Safe { _contract }\",\"x\":\"\xff\"}", http.StatusBadRequest},
		{"unnamed production operation", "POST", "application/json", `{"query":"{ _contract }"}`, http.StatusBadRequest},
		{"subscription", "POST", "application/json", `{"query":"subscription Watch { _contract }"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := graphRequest(tc.method, tc.contentType, tc.body)
			if tc.name == "websocket" {
				r.Header.Set("Upgrade", "websocket")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d, body %s", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "canary-secret") {
				t.Fatal("response leaked request body")
			}
		})
	}
}

func TestGraphQLHTTPBodyCapBeforeDecoder(t *testing.T) {
	handler := NewHTTPHandler(&Resolver{}, HTTPOptions{MaxBodyBytes: 512})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, graphRequest("POST", "application/json", fmt.Sprintf(`{"query":"query Safe { _contract }","padding":"%s"}`, strings.Repeat("x", 600))))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
}

func TestGraphQLHTTPPreflightLimitsFragmentsAliasesAndIntrospection(t *testing.T) {
	handler := NewHTTPHandler(&Resolver{}, HTTPOptions{})
	for _, query := range []string{
		`query Cycle { ...Loop } fragment Loop on Query { ...Loop }`,
		`query Alias { ` + strings.Repeat("a: _contract ", 250) + ` }`,
		`query Introspect { __schema { types { name } } }`,
		`query HugePage { viewer { sessions(first: 100000) { edges { node { id } } } } }`,
		`query Deep { ...A } fragment A on Query { ...B } fragment B on Query { ...C } fragment C on Query { ...D } fragment D on Query { ...E } fragment E on Query { ...F } fragment F on Query { ...G } fragment G on Query { ...H } fragment H on Query { ...I } fragment I on Query { ...J } fragment J on Query { ...K } fragment K on Query { ...L } fragment L on Query { ...M } fragment M on Query { ...N } fragment N on Query { ...O } fragment O on Query { _contract }`,
		`query Repeated { viewer { sessions(first: 100) { edges { node { id id id id id id id id id id id id } } } } }`,
	} {
		body, _ := json.Marshal(map[string]any{"query": query})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, graphRequest("POST", "application/json", string(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("query should be rejected: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestGraphQLHTTPPageCostUsesVariableAndDefaults(t *testing.T) {
	handler := NewHTTPHandler(&Resolver{}, HTTPOptions{})
	for _, body := range []string{
		`{"query":"query Huge($n: Int!) { viewer { subjects(first: $n) { edges { node { id } } } } }","variables":{"n":10000}}`,
		`{"query":"query Huge { viewer { sessions { edges { node { id id id id id id id id id id id id } } } } }"}`,
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, graphRequest("POST", "application/json", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unbounded page cost status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestGraphQLHTTPNamedQueryMetadataAndNoStore(t *testing.T) {
	var metadata OperationMetadata
	var found bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata, found = OperationMetadataFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	preflight := PreflightHTTP(inner, HTTPOptions{})
	w := httptest.NewRecorder()
	preflight.ServeHTTP(w, graphRequest("POST", "application/json", `{"query":"query Contract { _contract }"}`))
	if w.Code != http.StatusNoContent || !found || metadata.Name != "Contract" || metadata.Type != "query" || metadata.Cost < 1 || metadata.Depth < 1 {
		t.Fatalf("status=%d found=%t metadata=%+v", w.Code, found, metadata)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q", got)
	}
	// A preflight result cannot be forged by caller-provided context values.
	if _, ok := OperationMetadataFromContext(context.Background()); ok {
		t.Fatal("metadata unexpectedly present")
	}

	server := NewHTTPHandler(&Resolver{}, HTTPOptions{})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, graphRequest("POST", "application/json", `{"query":"query Contract { _contract }"}`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"_contract":true`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGraphQLHTTPPreflightPassesCORSOptionsThrough(t *testing.T) {
	preflight := PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), HTTPOptions{})
	w := httptest.NewRecorder()
	preflight.ServeHTTP(w, graphRequest(http.MethodOptions, "", ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("CORS OPTIONS did not reach downstream middleware: %d", w.Code)
	}
}

func TestGraphQLHTTPRejectsAmbiguousAndCompoundOperations(t *testing.T) {
	handler := NewHTTPHandler(&Resolver{}, HTTPOptions{})
	for _, body := range []string{
		`{"query":"query One { _contract } query Two { _contract }"}`,
		`{"query":"mutation Two { first: _contract second: _contract }"}`,
		`{"query":"mutation Spread { ...One ...Two } fragment One on Mutation { a: _contract } fragment Two on Mutation { b: _contract }"}`,
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, graphRequest(http.MethodPost, "application/json", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("compound operation accepted: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestGraphQLHTTPPresenterPreservesOnlyFixedSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		input error
		code  string
	}{
		{snapshotUserError("BAD_USER_INPUT", "canary-secret"), "BAD_USER_INPUT"},
		{snapshotUserError("INVALID_DATE_RANGE", "canary-secret"), "INVALID_DATE_RANGE"},
		{&gqlerror.Error{Message: "canary-secret", Extensions: map[string]any{"code": "INTERNAL", "details": "canary-secret"}}, ""},
		{fmt.Errorf("canary-secret"), ""},
	} {
		got := presentGraphQLError(context.Background(), tc.input)
		if strings.Contains(got.Message, "canary-secret") || strings.Contains(fmt.Sprint(got.Extensions), "canary-secret") {
			t.Fatalf("presenter leaked input: %#v", got)
		}
		if code, _ := got.Extensions["code"].(string); code != tc.code {
			t.Fatalf("code %q, want %q", code, tc.code)
		}
	}
}

func TestGraphQLHTTPRejectsOverlongOperationNameBeforeMetadata(t *testing.T) {
	name := "Op" + strings.Repeat("x", 64)
	called := false
	handler := PreflightHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}), HTTPOptions{})
	body, _ := json.Marshal(map[string]string{"query": "query " + name + " { _contract }", "operationName": name})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, graphRequest(http.MethodPost, "application/json", string(body)))
	if w.Code != http.StatusBadRequest || called || strings.Contains(w.Body.String(), name) {
		t.Fatalf("long operation name reached downstream or leaked: status=%d called=%t body=%s", w.Code, called, w.Body.String())
	}
}

func TestGraphQLMutationOutcomeIgnoresSelectedPayloadFields(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		failed      bool
		executed    bool
		wantStatus  int
	}{
		{"omitted errors", `mutation Invalid { requestMagicLink(input: {email: "invalid"}) { accepted } }`, true, true, http.StatusOK},
		{"aliased errors", `mutation Aliased { requestMagicLink(input: {email: "invalid"}) { e: errors { code } } }`, true, true, http.StatusOK},
		{"successful payload without errors selection", `mutation Valid { requestMagicLink(input: {email: "person@example.org"}) { accepted } }`, false, true, http.StatusOK},
		{"resolver error", `mutation Unauthorized { signOut { errors { code } } }`, true, true, http.StatusOK},
		{"execution error", `mutation Broken($input: RequestMagicLinkInput!) { requestMagicLink(input: $input) { accepted } }`, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outcome OperationOutcome
			var found bool
			port := &accountAuthPort{}
			ctx := ContextWithVerifiedViewer(context.Background(), "owner")
			ctx = ContextWithAuthAccountService(ctx, port)
			ctx = ContextWithAuthMutationFailureReporter(ctx, &accountFailureReporter{})
			handler := PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				NewHTTPHandler(&Resolver{}, HTTPOptions{}).ServeHTTP(w, r)
				outcome, found = OperationOutcomeFromContext(r.Context())
			}), HTTPOptions{})
			body, _ := json.Marshal(map[string]string{"query": tc.query})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, graphRequest(http.MethodPost, "application/json", string(body)).WithContext(ctx))
			if tc.wantStatus != 0 && w.Code != tc.wantStatus || !found || outcome.Failed != tc.failed || outcome.Executed != tc.executed {
				t.Fatalf("status=%d found=%t outcome=%+v body=%s", w.Code, found, outcome, w.Body.String())
			}
		})
	}
}

func TestGraphQLMutationOutcomeCatchesChildFieldErrorAfterRootSuccess(t *testing.T) {
	port := &subjectMutationPort{subject: mutationSubjectFixture()}
	ctx := mutationContext(port, nil) // Intentionally lacks SubjectSettings query port.
	var outcome OperationOutcome
	var found bool
	handler := PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		NewHTTPHandler(&Resolver{}, HTTPOptions{}).ServeHTTP(w, r)
		outcome, found = OperationOutcomeFromContext(r.Context())
	}), HTTPOptions{})
	w := httptest.NewRecorder()
	body := `{"query":"mutation CreateChild { createSubject(input: {handle: \"visible\", timezone: \"UTC\"}) { subject { id settings { timezone } } } }"}`
	handler.ServeHTTP(w, graphRequest(http.MethodPost, "application/json", body).WithContext(ctx))
	if w.Code != http.StatusOK || !found || !outcome.Executed || !outcome.Failed || !strings.Contains(w.Body.String(), `"errors"`) {
		t.Fatalf("child error not captured: status=%d outcome=%+v found=%t body=%s", w.Code, outcome, found, w.Body.String())
	}
}

func TestGraphQLHTTPDevelopmentAllowsUnnamedIntrospection(t *testing.T) {
	handler := NewHTTPHandler(&Resolver{}, HTTPOptions{Development: true})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, graphRequest("POST", "application/json", `{"query":"{ __schema { queryType { name } } }"}`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"queryType"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
