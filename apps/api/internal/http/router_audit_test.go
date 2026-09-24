package apihttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const auditTestSourceKey = "audit-pseudonym-test-key"

func TestAuditMiddlewareRecordsIntentBeforeCorrelatedOutcome(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
	router.Post("/v1/custom-providers/{customProviderId}/activities:ingest", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodPost, "/v1/custom-providers/thing-1/activities:ingest", nil)
	request.Header.Set(middleware.RequestIDHeader, "request-correlation-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	intent, outcome := events[0], events[1]
	if intent.Action != "http.mutation.intent" || intent.Actor != (operations.AuditActor{Type: operations.AuditActorAnonymous}) ||
		intent.Target != (operations.AuditTarget{Type: "custom_provider"}) || intent.Outcome != operations.AuditSucceeded {
		t.Fatalf("intent = %#v", intent)
	}
	if intent.Metadata["method"] != http.MethodPost || intent.Metadata["phase"] != "intent" {
		t.Fatalf("intent metadata = %#v", intent.Metadata)
	}
	if _, exists := intent.Metadata["path"]; exists {
		t.Fatalf("intent metadata includes raw path: %#v", intent.Metadata)
	}
	if outcome.Action != "post.v1.custom-providers.customProviderId.activities.ingest" || outcome.Actor != (operations.AuditActor{Type: operations.AuditActorAnonymous}) ||
		outcome.Target != (operations.AuditTarget{Type: "custom_provider", ID: "thing-1"}) || outcome.Outcome != operations.AuditSucceeded || outcome.Metadata["phase"] != "outcome" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if intent.RequestID == "" || intent.RequestID == "request-correlation-1" || outcome.RequestID != intent.RequestID || intent.ID == outcome.ID {
		t.Fatalf("uncorrelated events: intent=%#v outcome=%#v", intent, outcome)
	}
	wantSource := auditSourceIP(request.RemoteAddr, []byte(auditTestSourceKey))
	if wantSource == "" || intent.SourceIP != wantSource || outcome.SourceIP != wantSource || wantSource == request.RemoteAddr {
		t.Fatalf("source pseudonyms: intent=%q outcome=%q want=%q", intent.SourceIP, outcome.SourceIP, wantSource)
	}
}

func TestAuditMiddlewareDoesNotTrustInboundRequestIDForDurableCorrelation(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
	router.Post("/v1/custom-providers/{customProviderId}/activities:ingest", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/custom-providers/thing-1/activities:ingest", nil)
		request.Header.Set(middleware.RequestIDHeader, "attacker-reused-correlation")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 4 {
		t.Fatalf("events = %#v, error = %v", events, err)
	}
	first, second := events[0].RequestID, events[2].RequestID
	if first == "" || second == "" || first == second || first == "attacker-reused-correlation" || second == "attacker-reused-correlation" {
		t.Fatalf("durable correlations = %q, %q", first, second)
	}
	if events[1].RequestID != first || events[3].RequestID != second {
		t.Fatalf("intent/outcome pairs = %#v", events)
	}
}

func TestAuditMiddlewareBuffersSuccessUntilAtomicOutboxCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		enqueueErr error
		commitErr  error
		wantStatus int
		wantBody   string
		wantCommit int
	}{
		{name: "success", wantStatus: http.StatusCreated, wantBody: `{"secret":"response"}`, wantCommit: 1},
		{name: "enqueue failure", enqueueErr: errors.New("outbox unavailable"), wantStatus: http.StatusServiceUnavailable},
		{name: "commit failure", commitErr: errors.New("commit failed"), wantStatus: http.StatusServiceUnavailable, wantCommit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := operations.NewMemoryAuditSink()
			recorder, _ := operations.NewAuditRecorder(sink)
			transaction := &fakeMutationAuditTransaction{enqueueErr: test.enqueueErr, commitErr: test.commitErr}
			router := chi.NewRouter()
			router.Use(middleware.RequestID)
			router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
			router.Post("/v1/auth/magic-link/consume", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Set-Cookie", "private=response")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"secret":"response"}`))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			if test.wantBody != "" && response.Body.String() != test.wantBody {
				t.Fatalf("body = %q", response.Body.String())
			}
			if test.wantBody == "" && (strings.Contains(response.Body.String(), "secret") || response.Header().Get("Set-Cookie") != "") {
				t.Fatalf("buffered response leaked on rollback: headers=%v body=%q", response.Header(), response.Body.String())
			}
			if transaction.commits != test.wantCommit || (test.wantStatus == http.StatusServiceUnavailable && transaction.rollbacks == 0) {
				t.Fatalf("transaction commits=%d rollbacks=%d", transaction.commits, transaction.rollbacks)
			}
		})
	}
}

func TestAuditMiddlewareCommitsIntentionalFailureMutationWithOutbox(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, _ := operations.NewAuditRecorder(sink)
	transaction := &fakeMutationAuditTransaction{active: true}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	router.Post("/v1/auth/magic-link/consume", func(w http.ResponseWriter, r *http.Request) {
		if !operations.MarkMutationFailureCommit(r.Context()) {
			t.Fatal("failure commit marker was not installed")
		}
		writeFrameworkProblem(w, r, http.StatusUnauthorized, "Unauthorized", "unauthorized", "invalid passkey")
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
	if response.Code != http.StatusUnauthorized || transaction.commits != 1 || transaction.rollbacks != 1 || len(transaction.events) != 1 {
		t.Fatalf("response=%d commits=%d deferred rollbacks=%d", response.Code, transaction.commits, transaction.rollbacks)
	}
	if transaction.events[0].Outcome != operations.AuditDenied {
		t.Fatalf("outbox outcome = %q", transaction.events[0].Outcome)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 1 || events[0].Action != "http.mutation.intent" {
		t.Fatalf("direct audit events = %#v, %v", events, err)
	}
}

func TestAuditMiddlewareRollsBackUnmarkedFailureAndRecordsDirectOutcome(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, _ := operations.NewAuditRecorder(sink)
	transaction := &fakeMutationAuditTransaction{active: true}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	router.Post("/v1/auth/magic-link/consume", func(w http.ResponseWriter, r *http.Request) {
		writeFrameworkProblem(w, r, http.StatusInternalServerError, "Internal Server Error", "internal_error", "failed")
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
	if response.Code != http.StatusInternalServerError || transaction.commits != 0 || len(transaction.events) != 0 || transaction.rollbacks == 0 {
		t.Fatalf("response=%d commits=%d enqueued=%d rollbacks=%d", response.Code, transaction.commits, len(transaction.events), transaction.rollbacks)
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 || events[1].Outcome != operations.AuditFailed {
		t.Fatalf("direct audit events = %#v, %v", events, err)
	}
}

func TestAuditMiddlewarePreservesInactiveMarkedReplayFailure(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, _ := operations.NewAuditRecorder(sink)
	transaction := &fakeMutationAuditTransaction{}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	router.Post("/v1/auth/magic-link/consume", func(w http.ResponseWriter, r *http.Request) {
		operations.MarkMutationFailureCommit(r.Context())
		writeFrameworkProblem(w, r, http.StatusUnauthorized, "Unauthorized", "unauthorized", "replayed ceremony")
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
	if response.Code != http.StatusUnauthorized || transaction.commits != 0 || len(transaction.events) != 0 {
		t.Fatalf("response=%d commits=%d enqueued=%d", response.Code, transaction.commits, len(transaction.events))
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 || events[1].Outcome != operations.AuditDenied {
		t.Fatalf("direct audit events = %#v, %v", events, err)
	}
}

func TestAuditMiddlewareRollsBackActiveMutationWhenDeadlineExpires(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, _ := operations.NewAuditRecorder(sink)
	transaction := &fakeMutationAuditTransaction{active: true}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(timeoutProblems(5 * time.Millisecond))
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), fakeMutationAuditCoordinator{transaction: transaction}))
	router.Post("/v1/custom-providers/{customProviderId}/activities:ingest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "must-not-escape=secret")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"uncommitted":true}`))
		<-r.Context().Done()
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/custom-providers/timeout-target/activities:ingest", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" {
		t.Fatalf("timeout response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "uncommitted") || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("buffered success escaped timeout rollback: headers=%v body=%s", response.Header(), response.Body.String())
	}
	if transaction.commits != 0 || transaction.rollbacks == 0 || len(transaction.events) != 0 {
		t.Fatalf("commits=%d rollbacks=%d outbox=%d", transaction.commits, transaction.rollbacks, len(transaction.events))
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 || events[1].Outcome != operations.AuditFailed || events[1].Target.ID != "timeout-target" {
		t.Fatalf("direct timeout audit=%#v err=%v", events, err)
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if response.Header().Get(name) == "" {
			t.Fatalf("timeout response omitted %s", name)
		}
	}
}

type fakeMutationAuditCoordinator struct{ transaction *fakeMutationAuditTransaction }

func (coordinator fakeMutationAuditCoordinator) BeginMutation(ctx context.Context) (context.Context, operations.MutationAuditTransaction, error) {
	ctx, marker := operations.WithMutationFailureCommitMarker(ctx)
	coordinator.transaction.failure = marker
	return ctx, coordinator.transaction, nil
}

type fakeMutationAuditTransaction struct {
	enqueueErr error
	commitErr  error
	commits    int
	rollbacks  int
	active     bool
	failure    *operations.MutationFailureCommitMarker
	events     []operations.AuditEvent
}

func (transaction *fakeMutationAuditTransaction) Active() bool { return transaction.active }
func (transaction *fakeMutationAuditTransaction) CommitFailure() bool {
	return transaction.failure != nil && transaction.failure.Marked()
}

func (transaction *fakeMutationAuditTransaction) Enqueue(_ context.Context, event operations.AuditEvent) error {
	transaction.events = append(transaction.events, event)
	return transaction.enqueueErr
}
func (transaction *fakeMutationAuditTransaction) Commit() error {
	transaction.commits++
	return transaction.commitErr
}
func (transaction *fakeMutationAuditTransaction) Rollback() error {
	transaction.rollbacks++
	return nil
}

func TestNewRouterAuditOutcomeUsesAuthenticatedActorForMagicLinkConsume(t *testing.T) {
	const userID = "usr_018f-audit-actor"
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	validAuth := auditRouteAuth{user: auth.User{
		ID: userID, PrimaryEmail: "actor@example.com", Status: auth.UserStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
	router := NewRouter(Dependencies{Auth: validAuth, Audit: recorder, AuditSourceKey: []byte(auditTestSourceKey)})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(`{"token":"valid-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %#v, error = %v", events, err)
	}
	if events[0].Actor != (operations.AuditActor{Type: operations.AuditActorAnonymous}) ||
		events[1].Actor != (operations.AuditActor{Type: operations.AuditActorUser, ID: userID}) {
		t.Fatalf("intent/outcome actors = %#v, %#v", events[0].Actor, events[1].Actor)
	}
}

func TestAuditMiddlewareSanitizesHostileOutcomeTargetForAllStatuses(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		status     int
		wantTarget string
	}{
		{name: "success credential", path: "/v1/custom-providers/Bearer%20target-secret/activities:ingest", status: http.StatusOK, wantTarget: operations.RedactedValue},
		{name: "bad request query secret", path: "/v1/custom-providers/subject%3Faccess_token%3Dtarget-secret/activities:ingest", status: http.StatusBadRequest, wantTarget: operations.RedactedValue},
		{name: "forbidden CRLF", path: "/v1/custom-providers/subject%0D%0Aforged/activities:ingest", status: http.StatusForbidden, wantTarget: "subjectforged"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := operations.NewMemoryAuditSink()
			recorder, err := operations.NewAuditRecorder(sink)
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			router.Use(middleware.RequestID)
			router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
			router.Post("/v1/custom-providers/{customProviderId}/activities:ingest", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			events, err := sink.Events(context.Background())
			if err != nil || len(events) != 2 {
				t.Fatalf("events = %#v, error = %v", events, err)
			}
			if events[1].Target.ID != test.wantTarget {
				t.Fatalf("target ID = %q, want %q", events[1].Target.ID, test.wantTarget)
			}
			for _, secret := range []string{"target-secret", "access_token", "\r", "\n"} {
				if strings.Contains(events[1].Target.ID, secret) {
					t.Fatalf("target ID retained %q: %q", secret, events[1].Target.ID)
				}
			}
		})
	}
}

type auditRouteAuth struct {
	user auth.User
	err  error
}

func (auditRouteAuth) RequestMagicLink(context.Context, string, string) error { return nil }
func (auditRouteAuth) CompleteMagicLink(context.Context, string, auth.SessionMetadata) (auth.SessionGrant, error) {
	return auth.SessionGrant{Token: "session-token", SessionID: "session-id", UserID: "usr_018f-audit-actor", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (service auditRouteAuth) AuthenticateSession(context.Context, string) (auth.User, error) {
	if service.err != nil {
		return auth.User{}, service.err
	}
	return service.user, nil
}
func (auditRouteAuth) RevokeSession(context.Context, string) error { return nil }
func (auditRouteAuth) BeginPasskeyRegistration(context.Context, string) (auth.PasskeyOptions, error) {
	return auth.PasskeyOptions{}, nil
}
func (auditRouteAuth) CompletePasskeyRegistration(context.Context, string, json.RawMessage, string) (auth.PasskeyCredential, error) {
	return auth.PasskeyCredential{}, nil
}
func (auditRouteAuth) BeginPasskeyLogin(context.Context, string) (auth.PasskeyOptions, error) {
	return auth.PasskeyOptions{}, nil
}
func (auditRouteAuth) CompletePasskeyLogin(context.Context, string, []byte, json.RawMessage, auth.SessionMetadata) (auth.SessionGrant, error) {
	return auth.SessionGrant{}, nil
}

func TestAuditMiddlewareFailsClosedBeforeMutationWhenIntentSinkFails(t *testing.T) {
	sink := &capturingFailAuditSink{err: errors.New("database password=do-not-leak")}
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	handlerCalled := false
	core, logs := observer.New(zap.InfoLevel)
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), zap.New(core)))
	router.Post("/v1/custom-providers/{customProviderId}/activities:ingest", func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/custom-providers/access_token=do-not-leak/activities:ingest", nil))
	if handlerCalled {
		t.Fatal("mutation handler was called after intent persistence failed")
	}
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = status %d headers %#v", response.Code, response.Header())
	}
	var body struct {
		Type      string `json:"type"`
		Status    int    `json:"status"`
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Type != "https://jandibat.org/problems/service_unavailable" || body.Status != http.StatusServiceUnavailable || body.Code != "service_unavailable" || body.RequestID == "" {
		t.Fatalf("problem = %#v", body)
	}
	if strings.Contains(response.Body.String(), "password") || strings.Contains(response.Body.String(), "audit unavailable") {
		t.Fatalf("problem leaked sink failure: %s", response.Body.String())
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("intent attempts = %d, want 1", len(sink.attempts))
	}
	intent := sink.attempts[0]
	if intent.Action != "http.mutation.intent" || intent.Actor.ID != "" || intent.Target.ID != "" || intent.RequestID == "" || intent.RequestID == body.RequestID {
		t.Fatalf("safe intent = %#v", intent)
	}
	if _, exists := intent.Metadata["path"]; exists {
		t.Fatalf("intent metadata includes raw path: %#v", intent.Metadata)
	}
	entries := logs.All()
	if len(entries) != 1 || entries[0].Message != "http.audit_intent_failed" {
		t.Fatalf("audit failure log entries = %#v", entries)
	}
	for _, value := range entries[0].ContextMap() {
		text, _ := value.(string)
		for _, secret := range []string{"password", "do-not-leak", "access_token", "provider body"} {
			if strings.Contains(text, secret) {
				t.Fatalf("audit failure log leaked %q: %#v", secret, entries)
			}
		}
	}
}

func TestAuditMiddlewareLeavesDetectableIntentWhenOutcomeIsMissing(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey)))
	router.Post("/v1/auth/magic-link/consume", func(http.ResponseWriter, *http.Request) { panic("simulated process interruption") })

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("handler did not panic")
			}
		}()
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
	}()

	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	if events[0].Action != "http.mutation.intent" || events[0].RequestID == "" || events[0].Metadata["phase"] != "intent" {
		t.Fatalf("orphan intent = %#v", events[0])
	}
}

func TestMutationAuditClassificationIncludesOAuthCallback(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/anything", false},
		{http.MethodPut, "/anything", false},
		{http.MethodPatch, "/anything", false},
		{http.MethodDelete, "/anything", false},
		{http.MethodPost, "/v1/auth/magic-link/consume", true},
		{http.MethodPost, "/v1/custom-providers/provider-1/activities:ingest", true},
		{http.MethodPost, "/v1/subjects", false},
		{http.MethodDelete, "/v1/subjects/subject-1", false},
		{http.MethodGet, "/v1/integrations/github/callback", true},
		{http.MethodGet, "/v1/integrations/github/not-callback", false},
		{http.MethodGet, "/v1/activities/public", false},
	}
	for _, test := range tests {
		if got := isMutationRequest(test.method, test.path); got != test.want {
			t.Errorf("isMutationRequest(%q, %q) = %t, want %t", test.method, test.path, got, test.want)
		}
	}
}

func TestMutationRouteRegistryHasExplicitAtomicOutboxInventory(t *testing.T) {
	// Every registered success route must end in one of the participating SQL
	// write boundaries below in production. This inventory intentionally lives
	// beside mutationRoutePatterns so adding a route cannot silently omit its
	// atomic state+outbox contract.
	type route struct{ method, path string }
	registeredRoutes := make(map[route]struct{})
	routes, ok := NewRouter().(chi.Routes)
	if !ok {
		t.Fatal("production router does not expose chi routes")
	}
	if err := chi.Walk(routes, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := route{method: method, path: path}
		registeredRoutes[key] = struct{}{}
		if !strings.HasPrefix(path, "/v1/") {
			return nil
		}
		writeMethod := method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
		oauthCallback := method == http.MethodGet && path == "/v1/integrations/{provider}/callback"
		if writeMethod || oauthCallback {
			if _, classified := mutationRequestTarget(method, path); !classified {
				t.Errorf("production mutation route bypasses audit registry: %s %s", method, path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	capabilities := map[route]string{
		{http.MethodPost, "/v1/auth/magic-link/consume"}:                               "auth.ConsumeMagicLink+SaveSession",
		{http.MethodPost, "/v1/custom-providers/{customProviderId}/activities:ingest"}: "integrations.SaveIngestedActivities",
		{http.MethodGet, "/v1/integrations/{provider}/callback"}:                       "integrations.SaveConnection (OAuth state is preflight)",
	}
	registered := 0
	for method, patterns := range mutationRoutePatterns {
		for _, pattern := range patterns {
			registered++
			key := route{method, pattern.path}
			if _, exists := registeredRoutes[key]; !exists {
				t.Errorf("audit registry route is absent from production router: %s %s", method, pattern.path)
			}
			if capabilities[key] == "" {
				t.Errorf("mutation route has no atomic outbox capability: %s %s", method, pattern.path)
			}
			delete(capabilities, key)
		}
	}
	if registered != 3 || len(capabilities) != 0 {
		t.Fatalf("mutation registry=%d unmatched capabilities=%v", registered, capabilities)
	}
}

func TestAuditMiddlewareSkipsUnknownMutationsAndCapsIntentWrites(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	limiter := &auditTestLimiter{remaining: 1}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), limiter))
	router.Post("/v1/auth/magic-link/consume", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })

	unknown := httptest.NewRecorder()
	router.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/not-a-contract-route", nil))
	if events, _ := sink.Events(context.Background()); len(events) != 0 || limiter.calls != 0 {
		t.Fatalf("unknown mutation created durable state: events=%d limiter_calls=%d", len(events), limiter.calls)
	}

	unknownDenied := httptest.NewRecorder()
	unknownDeniedRouter := chi.NewRouter()
	unknownDeniedRouter.Use(middleware.RequestID)
	unknownDeniedRouter.Use(auditRequests(recorder, []byte(auditTestSourceKey), limiter))
	unknownDeniedRouter.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	request := httptest.NewRequest(http.MethodPost, "/still-not-a-contract-route", nil)
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "attacker-controlled"})
	request.Header.Set("Origin", "https://invalid.example")
	unknownDeniedRouter.ServeHTTP(unknownDenied, request)
	if events, _ := sink.Events(context.Background()); unknownDenied.Code != http.StatusForbidden || len(events) != 0 || limiter.calls != 0 {
		t.Fatalf("unknown denied request created durable state: status=%d events=%d limiter_calls=%d", unknownDenied.Code, len(events), limiter.calls)
	}

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", nil))
	events, _ := sink.Events(context.Background())
	if first.Code != http.StatusCreated || second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "60" || len(events) != 2 {
		t.Fatalf("responses=%d/%d retry=%q events=%d", first.Code, second.Code, second.Header().Get("Retry-After"), len(events))
	}
}

func TestAuditMiddlewareSkipsMatchedReadDenialsWithoutPreGate(t *testing.T) {
	sink := operations.NewMemoryAuditSink()
	recorder, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	limiter := &auditTestLimiter{remaining: 1}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(auditRequests(recorder, []byte(auditTestSourceKey), limiter))
	router.Get("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	for range 3 {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "attacker-controlled"})
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", response.Code)
		}
	}
	events, err := sink.Events(context.Background())
	if err != nil || len(events) != 0 || limiter.calls != 0 {
		t.Fatalf("matched read denials created durable state: events=%d limiter_calls=%d error=%v", len(events), limiter.calls, err)
	}
}

type auditTestLimiter struct {
	remaining int
	calls     int
}

func (limiter *auditTestLimiter) Allow(context.Context, string, string) (bool, time.Duration, error) {
	limiter.calls++
	if limiter.remaining <= 0 {
		return false, time.Minute, nil
	}
	limiter.remaining--
	return true, 0, nil
}

var _ handlers.RateLimiter = (*auditTestLimiter)(nil)

func TestAuditSourceIPRejectsMalformedAddressesAndRequiresKey(t *testing.T) {
	if got := auditSourceIP("not-an-ip", []byte("key")); got != "" {
		t.Fatalf("malformed source = %q", got)
	}
	if got := auditSourceIP("192.0.2.1:1234", nil); got != "" {
		t.Fatalf("unkeyed source = %q", got)
	}
}

type capturingFailAuditSink struct {
	attempts []operations.AuditEvent
	err      error
}

func (sink *capturingFailAuditSink) WriteAuditEvent(_ context.Context, event operations.AuditEvent) error {
	sink.attempts = append(sink.attempts, event)
	return sink.err
}
