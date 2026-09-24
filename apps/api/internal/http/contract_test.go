package apihttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	adapteroauth "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/memory"
	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type recordingTimeline struct {
	input appactivity.GetTimelineInput
	calls int
}

type failingTimeline struct{ err error }

func (service failingTimeline) Execute(context.Context, appactivity.GetTimelineInput) (appactivity.GetTimelineOutput, error) {
	return appactivity.GetTimelineOutput{}, service.err
}

func (service *recordingTimeline) Execute(_ context.Context, input appactivity.GetTimelineInput) (appactivity.GetTimelineOutput, error) {
	service.input = input
	service.calls++
	from, to := domain.Date("2026-08-01"), domain.Date("2026-08-02")
	return appactivity.GetTimelineOutput{
		From: from, To: to, Stale: true,
		Timeline: domain.Timeline{
			Subject: input.Subject, Timezone: input.Timezone,
			Environments: []domain.Environment{},
			Days:         []domain.Day{{Date: from, Count: 2, Level: domain.LevelLow, Entries: []domain.DayEntry{}}},
		},
	}, nil
}

type magicLinkRequest struct {
	email       string
	redirectURI string
}

type fakeAuth struct {
	magicLinkRequest *magicLinkRequest
}

func (auth fakeAuth) RequestMagicLink(_ context.Context, email, redirectURI string) error {
	if auth.magicLinkRequest != nil {
		auth.magicLinkRequest.email = email
		auth.magicLinkRequest.redirectURI = redirectURI
	}
	return nil
}
func (fakeAuth) CompleteMagicLink(context.Context, string, auth.SessionMetadata) (auth.SessionGrant, error) {
	return auth.SessionGrant{Token: "issued-token", SessionID: "session-1", UserID: "user-1", ExpiresAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, nil
}
func (fakeAuth) AuthenticateSession(context.Context, string) (auth.User, error) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	return auth.User{ID: "user-1", PrimaryEmail: "user@example.com", Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}, nil
}
func (fakeAuth) RevokeSession(context.Context, string) error { return nil }
func (fakeAuth) BeginPasskeyRegistration(context.Context, string) (auth.PasskeyOptions, error) {
	return auth.PasskeyOptions{}, nil
}
func (fakeAuth) CompletePasskeyRegistration(context.Context, string, json.RawMessage, string) (auth.PasskeyCredential, error) {
	return auth.PasskeyCredential{}, nil
}
func (fakeAuth) BeginPasskeyLogin(context.Context, string) (auth.PasskeyOptions, error) {
	return auth.PasskeyOptions{}, nil
}
func (fakeAuth) CompletePasskeyLogin(context.Context, string, []byte, json.RawMessage, auth.SessionMetadata) (auth.SessionGrant, error) {
	return auth.SessionGrant{}, nil
}

type allowOwner struct{}

func (allowOwner) OwnsSubject(context.Context, string, string) (bool, error) { return true, nil }

type staticConnections struct {
	item            integrations.ProviderConnection
	connected       *integrations.ConnectInput
	oauthCompletion *recordedOAuthCompletion
	oauthErr        error
}

func (store staticConnections) ConnectToken(_ context.Context, input integrations.ConnectTokenInput) (integrations.ProviderConnection, error) {
	if store.connected != nil {
		*store.connected = input.ConnectInput
	}
	return store.item, nil
}
func (store staticConnections) ConnectPublic(_ context.Context, input integrations.ConnectInput) (integrations.ProviderConnection, error) {
	if store.connected != nil {
		*store.connected = input
	}
	return store.item, nil
}
func (store staticConnections) BeginOAuth(_ context.Context, input integrations.ConnectInput) (integrations.ProviderConnection, error) {
	if store.connected != nil {
		*store.connected = input
	}
	return store.item, nil
}
func (store staticConnections) Update(context.Context, integrations.UpdateConnectionInput) (integrations.ProviderConnection, error) {
	return store.item, nil
}
func (store staticConnections) Revoke(context.Context, string) (integrations.ProviderConnection, error) {
	return store.item, nil
}
func (store staticConnections) Get(context.Context, string) (integrations.ProviderConnection, error) {
	return store.item, nil
}
func (store staticConnections) List(context.Context, string) ([]integrations.ProviderConnection, error) {
	return []integrations.ProviderConnection{store.item}, nil
}
func (store staticConnections) CompleteOAuthConnection(_ context.Context, _ string, _ integrations.TokenCredentials, externalID, login string, _ []string) (integrations.ProviderConnection, error) {
	if store.oauthCompletion != nil {
		*store.oauthCompletion = recordedOAuthCompletion{externalID: externalID, login: login}
	}
	if store.oauthErr != nil {
		return integrations.ProviderConnection{}, store.oauthErr
	}
	return store.item, nil
}

type recordedOAuthCompletion struct{ externalID, login string }

type recordingOAuthFlow struct {
	begin    adapteroauth.AuthorizationRequest
	complete adapteroauth.CallbackRequest
	redirect string
	revoked  string
}

func (flow *recordingOAuthFlow) RevokeToken(_ context.Context, token []byte) error {
	flow.revoked = string(token)
	return nil
}

func (flow *recordingOAuthFlow) ProviderID() string { return "github" }
func (flow *recordingOAuthFlow) Begin(_ context.Context, request adapteroauth.AuthorizationRequest) (adapteroauth.AuthorizationResult, error) {
	flow.begin = request
	return adapteroauth.AuthorizationResult{URL: "https://github.com/login/oauth/authorize?state=opaque"}, nil
}
func (flow *recordingOAuthFlow) Complete(_ context.Context, request adapteroauth.CallbackRequest) (adapteroauth.CallbackResult, error) {
	flow.complete = request
	return adapteroauth.CallbackResult{
		ProviderID: "github", ConnectionID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1",
		Identity:          adapteroauth.AccountIdentity{ExternalAccountID: "123", Username: "octocat"},
		ClientRedirectURI: flow.redirect, Credentials: integrations.TokenCredentials{AccessToken: "not-exposed"},
	}, nil
}

type staticSubjectVisibility struct {
	public bool
	err    error
}

func (visibility staticSubjectVisibility) AuthorizeSubjectRead(context.Context, string, string) (bool, error) {
	return visibility.public, visibility.err
}

func TestSVGRejectsDestructiveFailurePolicy(t *testing.T) {
	timeline := &recordingTimeline{}
	response := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Timeline: timeline}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/render/alice.svg?failurePolicy=purge", nil))
	if response.Code != http.StatusBadRequest || timeline.calls != 0 {
		t.Fatalf("status=%d timeline_calls=%d body=%s", response.Code, timeline.calls, response.Body.String())
	}
}

func TestSVGPrivacyScopesOwnerFactsAndCacheControl(t *testing.T) {
	t.Run("anonymous public svg", func(t *testing.T) {
		timeline := &recordingTimeline{}
		request := httptest.NewRequest(http.MethodGet, "/v1/render/alice.svg?timezone=UTC", nil)
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, SubjectVisibility: staticSubjectVisibility{public: true}, SubjectAuthorizer: allowOwner{},
		}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		if timeline.input.IncludeSubjectEnvironments {
			t.Fatal("anonymous request was allowed to load subject-scoped environments")
		}
		if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=300" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if responseVaryContains(recorder.Header(), "Cookie") || responseVaryContains(recorder.Header(), "Authorization") {
			t.Fatalf("public SVG varies on credentials: %q", recorder.Header().Values("Vary"))
		}
	})

	t.Run("private subject anonymous", func(t *testing.T) {
		timeline := &recordingTimeline{}
		request := httptest.NewRequest(http.MethodGet, "/v1/render/alice.svg", nil)
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, SubjectVisibility: staticSubjectVisibility{err: subjects.ErrUnauthenticated},
		}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("private SVG status = %d, want 401: %s", recorder.Code, recorder.Body.String())
		}
		if timeline.calls != 0 {
			t.Fatal("private subject timeline was loaded before authorization")
		}
	})

	t.Run("svg projection", func(t *testing.T) {
		store := memory.New()
		owner := domain.SubjectID("alice")
		date := domain.Date("2026-08-12")
		if err := store.SaveEnvironments(context.Background(), domain.SaveEnvironmentsInput{Environments: []domain.Environment{
			{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
			{ID: "connection:private", Key: "connection:private", Name: "Private GitHub", Scope: domain.EnvironmentScopeSubject, OwnerSubject: &owner, Metadata: map[string]string{"visibility": "private"}},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveFacts(context.Background(), domain.SaveFactsInput{Subject: owner, Facts: []domain.Fact{
			{Subject: owner, Date: date, EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}},
			{Subject: owner, Date: date, EnvironmentID: "connection:private", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 10}},
		}}); err != nil {
			t.Fatal(err)
		}
		timeline, err := appactivity.NewGetTimeline(store, nil, appactivity.GetTimelineOptions{Now: func() time.Time {
			return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
		}})
		if err != nil {
			t.Fatal(err)
		}
		path := "/v1/render/alice.svg?from=2026-08-12&to=2026-08-12&timezone=UTC"
		publicRequest := httptest.NewRequest(http.MethodGet, path, nil)
		publicRecorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, SubjectVisibility: staticSubjectVisibility{public: true}, SubjectAuthorizer: allowOwner{},
		}).ServeHTTP(publicRecorder, publicRequest)
		if publicRecorder.Code != http.StatusOK || !strings.Contains(publicRecorder.Body.String(), `data-count="1"`) ||
			strings.Contains(publicRecorder.Body.String(), `data-count="11"`) {
			t.Fatalf("anonymous SVG included private count: status=%d body=%s", publicRecorder.Code, publicRecorder.Body.String())
		}

		ownerRequest := httptest.NewRequest(http.MethodGet, path, nil)
		ownerRequest.Header.Set("Authorization", "Bearer token")
		ownerRecorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, Auth: fakeAuth{}, SubjectVisibility: staticSubjectVisibility{public: true}, SubjectAuthorizer: allowOwner{},
		}).ServeHTTP(ownerRecorder, ownerRequest)
		if ownerRecorder.Code != http.StatusOK || !strings.Contains(ownerRecorder.Body.String(), `data-count="11"`) {
			t.Fatalf("owner SVG omitted private count: status=%d body=%s", ownerRecorder.Code, ownerRecorder.Body.String())
		}
		if got := ownerRecorder.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("owner SVG Cache-Control = %q", got)
		}
	})
}

func TestStrictJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	for _, body := range []string{`{"token":"opaque","unknown":true}`, `{"token":"opaque"}{}`} {
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", body, recorder.Code)
		}
	}
}

func TestSecurityHeadersAndAllowlistedCORS(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://app.example")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{AllowedOrigins: []string{"https://app.example"}}).ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatal("expected allowlisted CORS origin")
	}
	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy"} {
		if recorder.Header().Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
}

func TestInternalErrorsAreSanitized(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/render/alice.svg", nil)
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Timeline: failingTimeline{err: errors.New("database password=do-not-leak")}}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "do-not-leak") || strings.Contains(recorder.Body.String(), "database") {
		t.Fatalf("internal error leaked: %s", recorder.Body.String())
	}
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func TestMagicConsumePayloadLimit(t *testing.T) {
	oversized := `{"token":"` + strings.Repeat("a", (1<<20)+1) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(oversized))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("payload status = %d, want 413: %s", recorder.Code, recorder.Body.String())
	}
}

func TestDegradedHealthUsesContractProblem(t *testing.T) {
	readiness, err := operations.NewReadinessChecker(time.Second, operations.ReadinessDependency{
		Name: "database", Probe: operations.DependencyProbeFunc(func(context.Context) error { return errors.New("down") }),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Readiness: readiness}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/problem+json") || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("degraded health response = %d %q Retry-After=%q: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Header().Get("Retry-After"), recorder.Body.String())
	}
}

func TestOAuthCallbackBindsSessionAndUsesAllowlistedRedirect(t *testing.T) {
	connection := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "connection:11111111-1111-4111-8111-111111111111",
		AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionPending,
	}
	redirect := "https://app.example/#connections"
	flow := &recordingOAuthFlow{redirect: redirect}
	completion := &recordedOAuthCompletion{}
	connections := staticConnections{item: connection, oauthCompletion: completion}
	auditSink := operations.NewMemoryAuditSink()
	auditRecorder, err := operations.NewAuditRecorder(auditSink)
	if err != nil {
		t.Fatal(err)
	}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, OAuthConnections: connections,
		OAuthFlows: map[string]adapteroauth.Flow{"github": flow}, OAuthWebURL: "https://app.example", AllowedRedirects: []string{redirect, "https://app.example/settings/providers"},
		AllowedOrigins: []string{"https://app.example"}, Audit: auditRecorder, AuditSourceKey: []byte("oauth-target-audit-key"),
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/integrations/github/callback?state="+strings.Repeat("s", 32)+"&code=code", nil)
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "session-binding-token"})
	request.Header.Set("Authorization", "Bearer ignored-api-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || flow.complete.SessionBinding != "session-binding-token" {
		t.Fatalf("OAuth callback = %d request=%#v body=%s", recorder.Code, flow.complete, recorder.Body.String())
	}
	if completion.externalID != "123" || completion.login != "octocat" {
		t.Fatalf("OAuth identity completion = %#v", completion)
	}
	if flow.revoked != "not-exposed" {
		t.Fatalf("public-only OAuth token was not revoked: %q", flow.revoked)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil || location.Scheme != "https" || location.Host != "app.example" || location.Fragment != "connections" || location.Query().Get("status") != "connected" {
		t.Fatalf("OAuth redirect = %q, error=%v", recorder.Header().Get("Location"), err)
	}
	events, err := auditSink.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantActions := map[string]bool{
		"get.v1.integrations.provider.callback": false,
	}
	for _, event := range events {
		if _, tracked := wantActions[event.Action]; tracked && event.Metadata["phase"] == "outcome" {
			if event.Target != (operations.AuditTarget{Type: "provider_connection", ID: connection.ID}) {
				t.Fatalf("canonical OAuth audit target for %q = %#v", event.Action, event.Target)
			}
			wantActions[event.Action] = true
		}
	}
	for action, found := range wantActions {
		if !found {
			t.Fatalf("missing audited outcome %q in %#v", action, events)
		}
	}
}

func TestOAuthCallbackRevokesFreshTokenWhenLocalPersistenceFails(t *testing.T) {
	flow := &recordingOAuthFlow{}
	connections := staticConnections{oauthErr: errors.New("database unavailable")}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, OAuthConnections: connections,
		OAuthFlows: map[string]adapteroauth.Flow{"github": flow},
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/integrations/github/callback?state="+strings.Repeat("s", 32)+"&code=code", nil)
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "session-binding-token"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || flow.revoked != "not-exposed" {
		t.Fatalf("status=%d revoked=%q body=%s", response.Code, flow.revoked, response.Body.String())
	}
}
