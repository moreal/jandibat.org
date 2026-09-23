package apihttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
)

type graphQLLimitCall struct{ scope, key string }
type recordingGraphQLLimiter struct {
	calls     []graphQLLimitCall
	denyScope string
	failScope string
}

func (l *recordingGraphQLLimiter) Allow(_ context.Context, scope, key string) (bool, time.Duration, error) {
	l.calls = append(l.calls, graphQLLimitCall{scope, key})
	if scope == l.failScope {
		return false, 0, errors.New("secret limiter failure")
	}
	if scope == l.denyScope {
		return false, 75 * time.Second, nil
	}
	return true, 0, nil
}

func runGraphQLRateLimitRequest(t *testing.T, limiter *recordingGraphQLLimiter, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var delivered string
	handler := graph.PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !graphQLRateLimit(w, r, limiter) {
			return
		}
		content, _ := io.ReadAll(r.Body)
		delivered = string(content)
		w.WriteHeader(http.StatusNoContent)
	}), graph.HTTPOptions{})
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer private-token")
	r.RemoteAddr = "192.0.2.10:7000"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w, delivered
}

// A renamed operation, alias and fragment must not avoid the actual magic-link quota.
func TestGraphQLRateLimitUsesSelectedMutationFieldAndHashesEmail(t *testing.T) {
	limiter := &recordingGraphQLLimiter{}
	body := `{"query":"mutation Harmless($input: RequestMagicLinkInput!) { ...Magic } fragment Magic on Mutation { alias: requestMagicLink(input: $input) { accepted } }","operationName":"Harmless","variables":{"input":{"email":"  User@Example.Test  "}}}`
	w, delivered := runGraphQLRateLimitRequest(t, limiter, body)
	if w.Code != http.StatusNoContent || delivered != body {
		t.Fatalf("status=%d delivered=%q", w.Code, delivered)
	}
	want := []string{"magic_link_ip", "magic_link_account", "magic_link_ip_daily", "magic_link_account_daily"}
	if len(limiter.calls) != len(want) {
		t.Fatalf("calls=%#v", limiter.calls)
	}
	for i, scope := range want {
		if limiter.calls[i].scope != scope {
			t.Fatalf("call %d scope=%q, want %q", i, limiter.calls[i].scope, scope)
		}
		if strings.Contains(limiter.calls[i].key, "User@") || strings.Contains(limiter.calls[i].key, "private-token") {
			t.Fatalf("raw identifier reached limiter: %#v", limiter.calls[i])
		}
	}
	if limiter.calls[1].key != limiter.calls[3].key {
		t.Fatal("same normalized email should share account and daily buckets")
	}
}

func TestGraphQLRateLimitClassifiesPasskeyCeremonyAndOtherSensitiveMutations(t *testing.T) {
	for _, tc := range []struct{ name, query, variables, scope string }{
		{"finish passkey", `mutation Safe($input: FinishPasskeySignInInput!) { finishPasskeySignIn(input: $input) { errors { code } } }`, `{"input":{"ceremonyID":"private-ceremony","credentialJSON":"{}"}}`, "passkey_ceremony"},
		{"connection", `mutation Safe { connectProvider(input: {subjectID:"id", providerID:"github", authMethod:TOKEN}) { errors { code } } }`, `{}`, "connection_ip"},
		{"manual sync", `mutation Safe { enqueueManualSync(input: {connectionID:"id", idempotencyKey:"secret"}) { errors { code } } }`, `{}`, "sync_ip"},
		{"rotate key", `mutation Safe { rotateCustomProviderKey(input: {id:"id"}) { errors { code } } }`, `{}`, "rotate_key_ip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limiter := &recordingGraphQLLimiter{}
			body := `{"query":` + strconv.Quote(tc.query) + `,"operationName":"Safe","variables":` + tc.variables + `}`
			w, _ := runGraphQLRateLimitRequest(t, limiter, body)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			found := false
			for _, call := range limiter.calls {
				if call.scope == tc.scope {
					found = true
				}
				if strings.Contains(call.key, "private-ceremony") || strings.Contains(call.key, "secret") {
					t.Fatalf("raw secret reached limiter: %#v", call)
				}
			}
			if !found {
				t.Fatalf("missing %s in %#v", tc.scope, limiter.calls)
			}
		})
	}
}

func TestGraphQLRateLimitDenyAndBackendFailureAreFailClosed(t *testing.T) {
	body := `{"query":"mutation Any { beginPasskeySignIn { errors { code } } }","operationName":"Any"}`
	for _, tc := range []struct {
		name, deny, fail string
		status           int
	}{
		{"quota", "passkey_ip", "", http.StatusTooManyRequests},
		{"backend", "", "passkey_ip", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, delivered := runGraphQLRateLimitRequest(t, &recordingGraphQLLimiter{denyScope: tc.deny, failScope: tc.fail}, body)
			if w.Code != tc.status || delivered != "" {
				t.Fatalf("status=%d delivered=%q body=%s", w.Code, delivered, w.Body.String())
			}
			if tc.deny != "" && w.Header().Get("Retry-After") != "75" {
				t.Fatalf("Retry-After=%q", w.Header().Get("Retry-After"))
			}
			if strings.Contains(w.Body.String(), "secret limiter failure") {
				t.Fatal("limiter error leaked")
			}
		})
	}
}

// A valid single named operation may omit operationName; rejecting it here
// would turn every such mutation into a 503 after preflight already accepted it.
func TestGraphQLRateLimitAcceptsUnambiguousOmittedOperationName(t *testing.T) {
	body := `{"query":"mutation Named($input: RequestMagicLinkInput!) { requestMagicLink(input: $input) { accepted } }","variables":{"input":{"email":"test@example.test"}}}`
	w, delivered := runGraphQLRateLimitRequest(t, &recordingGraphQLLimiter{}, body)
	if w.Code != http.StatusNoContent || delivered != body {
		t.Fatalf("status=%d delivered=%q body=%s", w.Code, delivered, w.Body.String())
	}
}

func TestGraphQLVerifiedAccountLimitSharesQuotaAcrossSessionsAndCoversSubjectMutation(t *testing.T) {
	limiter := handlers.NewMemoryRateLimiter(map[string]handlers.RateLimitPolicy{
		"mutation_intent_session": {Limit: 1, Window: time.Hour},
	}, nil)
	body := `{"query":"mutation Change { updateSubject(input: {id:\"id\", name:\"name\"}) { errors { code } } }","operationName":"Change"}`
	request := func(token, userID string) int {
		handler := graph.PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !graphQLVerifiedAccountRateLimit(w, r, limiter, userID) {
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}), graph.HTTPOptions{})
		r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("token-one", "user-1"); got != http.StatusNoContent {
		t.Fatalf("first status=%d", got)
	}
	if got := request("token-two", "user-1"); got != http.StatusTooManyRequests {
		t.Fatalf("second session status=%d", got)
	}
	if got := request("token-three", "user-2"); got != http.StatusNoContent {
		t.Fatalf("other user status=%d", got)
	}
}

func TestGraphQLVerifiedAccountLimitUsesActionAccountScopeAndFailsClosed(t *testing.T) {
	body := `{"query":"mutation Change { rotateCustomProviderKey(input: {id:\"id\"}) { errors { code } } }","operationName":"Change"}`
	for _, tc := range []struct {
		scope string
		want  int
	}{
		{"mutation_intent_session", http.StatusServiceUnavailable},
		{"rotate_key_account", http.StatusServiceUnavailable},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			limiter := &recordingGraphQLLimiter{failScope: tc.scope}
			handler := graph.PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !graphQLVerifiedAccountRateLimit(w, r, limiter, "user-1") {
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}), graph.HTTPOptions{})
			r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "secret limiter failure") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// Unknown scopes silently allow requests in the current limiter adapters.
// This test catches policy drift for every scope used by GraphQL HTTP limits.
func TestGraphQLRateLimitScopesHaveEffectiveDefaultPolicies(t *testing.T) {
	policies := handlers.DefaultRateLimitPolicies()
	for _, scope := range []string{
		"public_activity_ip", "public_activity_account", "magic_link_ip", "magic_link_account", "magic_link_ip_daily", "magic_link_account_daily",
		"passkey_ip", "passkey_account", "passkey_email", "passkey_ceremony", "connection_ip", "connection_account", "sync_ip", "sync_account", "rotate_key_ip", "rotate_key_account", "mutation_intent_session",
	} {
		policy, ok := policies[scope]
		if !ok || policy.Limit <= 0 || policy.Window <= 0 {
			t.Fatalf("GraphQL scope %q has ineffective policy: %#v", scope, policy)
		}
	}
}

// A credential change must not reset a verified user's read quota. The
// pre-auth pass is IP-only; account identity comes from session verification.
func TestGraphQLReadAccountLimitIsSharedAcrossSessions(t *testing.T) {
	limiter := handlers.NewMemoryRateLimiter(map[string]handlers.RateLimitPolicy{
		"public_activity_ip":      {Limit: 100, Window: time.Hour},
		"public_activity_account": {Limit: 1, Window: time.Hour},
	}, nil)
	body := `{"query":"query Viewer { viewer { user { id } } }","operationName":"Viewer"}`
	request := func(token, userID string) int {
		handler := graph.PreflightHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !graphQLRateLimit(w, r, limiter) {
				return
			}
			if !graphQLVerifiedAccountRateLimit(w, r, limiter, userID) {
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}), graph.HTTPOptions{})
		r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("token-one", "user-1"); got != http.StatusNoContent {
		t.Fatalf("first status=%d", got)
	}
	if got := request("token-two", "user-1"); got != http.StatusTooManyRequests {
		t.Fatalf("second session status=%d", got)
	}
	if got := request("token-three", "user-2"); got != http.StatusNoContent {
		t.Fatalf("other account status=%d", got)
	}
}

func TestGraphQLPreAuthReadRateLimitDoesNotUseSessionAsAccount(t *testing.T) {
	limiter := &recordingGraphQLLimiter{}
	body := `{"query":"query Viewer { viewer { user { id } } }","operationName":"Viewer"}`
	w, _ := runGraphQLRateLimitRequest(t, limiter, body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
	if len(limiter.calls) != 1 || limiter.calls[0].scope != "public_activity_ip" {
		t.Fatalf("pre-auth scopes=%#v", limiter.calls)
	}
}
