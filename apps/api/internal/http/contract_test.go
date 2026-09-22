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

type inertCustomProviders struct{}

func (inertCustomProviders) Create(context.Context, integrations.CreateCustomProviderInput) (integrations.CustomProvider, error) {
	return integrations.CustomProvider{}, integrations.ErrNotFound
}
func (inertCustomProviders) Update(context.Context, integrations.UpdateCustomProviderInput) (integrations.CustomProvider, error) {
	return integrations.CustomProvider{}, integrations.ErrNotFound
}
func (inertCustomProviders) RotateIngestSecret(context.Context, string, string) error {
	return integrations.ErrNotFound
}
func (inertCustomProviders) Get(context.Context, string) (integrations.CustomProvider, error) {
	return integrations.CustomProvider{}, integrations.ErrNotFound
}
func (inertCustomProviders) List(context.Context, string) ([]integrations.CustomProvider, error) {
	return []integrations.CustomProvider{}, nil
}
func (inertCustomProviders) Delete(context.Context, string) error { return integrations.ErrNotFound }
func (inertCustomProviders) Ingest(context.Context, integrations.IngestCustomActivitiesInput) (integrations.IngestCustomActivitiesResult, error) {
	return integrations.IngestCustomActivitiesResult{}, integrations.ErrUnauthorized
}

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

type referenceResolver struct {
	id     string
	handle string
	err    error
}

func (resolver referenceResolver) ResolveSubjectID(context.Context, string) (string, error) {
	return resolver.id, resolver.err
}
func (resolver referenceResolver) ResolveSubjectReference(context.Context, string) (string, string, error) {
	return resolver.id, resolver.handle, resolver.err
}

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

type recordingSync struct{ input integrations.ManualSyncInput }

func (service *recordingSync) EnqueueManualSync(_ context.Context, input integrations.ManualSyncInput) (integrations.SyncJob, error) {
	service.input = input
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	return integrations.SyncJob{ID: "22222222-2222-4222-8222-222222222222", ConnectionID: input.ConnectionID, Status: integrations.SyncJobPending, CreatedAt: now, UpdatedAt: now}, nil
}
func (service *recordingSync) GetJob(context.Context, string) (integrations.SyncJob, error) {
	return integrations.SyncJob{}, integrations.ErrNotFound
}

type staticSubjectVisibility struct {
	public bool
	err    error
}

func (visibility staticSubjectVisibility) AuthorizeSubjectRead(context.Context, string, string) (bool, error) {
	return visibility.public, visibility.err
}

func TestActivityContractShapeAndQueryParsing(t *testing.T) {
	timeline := &recordingTimeline{}
	router := apihttp.NewRouter(apihttp.Dependencies{Timeline: timeline})
	request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice?from=2026-08-01&to=2026-08-02&timezone=Asia%2FSeoul", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Subject      string `json:"subject"`
		Timezone     string `json:"timezone"`
		From         string `json:"from"`
		To           string `json:"to"`
		GeneratedAt  string `json:"generatedAt"`
		Stale        bool   `json:"stale"`
		Environments []any  `json:"environments"`
		Days         []struct {
			Entries []any `json:"entries"`
		} `json:"days"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Subject != "alice" || payload.Timezone != "Asia/Seoul" || payload.From == "" || payload.To == "" || payload.GeneratedAt == "" || !payload.Stale {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Environments == nil || len(payload.Days) != 1 || payload.Days[0].Entries == nil {
		t.Fatalf("required arrays must be present: %+v", payload)
	}
	if timeline.input.FailurePolicy != domain.FetchFailureKeepStale {
		t.Fatalf("activity read failure policy = %q, want keep_stale", timeline.input.FailurePolicy)
	}
	if recorder.Header().Get("ETag") == "" {
		t.Fatal("expected ETag")
	}
}

func TestActivityAndSVGRejectDestructiveFailurePolicy(t *testing.T) {
	for _, path := range []string{
		"/v1/activities/alice?failurePolicy=purge",
		"/v1/render/alice.svg?failurePolicy=purge",
	} {
		t.Run(path, func(t *testing.T) {
			timeline := &recordingTimeline{}
			response := httptest.NewRecorder()
			apihttp.NewRouter(apihttp.Dependencies{Timeline: timeline}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusBadRequest || timeline.calls != 0 {
				t.Fatalf("status=%d timeline_calls=%d body=%s", response.Code, timeline.calls, response.Body.String())
			}
		})
	}
}

func TestManagedPublicActivitySeparatesLocalSubjectFromProviderHandle(t *testing.T) {
	timeline := &recordingTimeline{}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Timeline: timeline, SubjectResolver: referenceResolver{id: "sub_018f", handle: "octocat"},
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/activities/octocat", nil))
	if response.Code != http.StatusOK || timeline.input.Subject != "sub_018f" || timeline.input.ProviderSubject != "octocat" {
		t.Fatalf("status=%d timeline input=%#v body=%s", response.Code, timeline.input, response.Body.String())
	}
}

func TestManagedPublicActivityStopsWhenSubjectReferenceResolutionFails(t *testing.T) {
	timeline := &recordingTimeline{}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Timeline: timeline, SubjectResolver: referenceResolver{err: errors.New("resolver unavailable")},
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/activities/octocat", nil))
	if response.Code != http.StatusInternalServerError || timeline.calls != 0 {
		t.Fatalf("status=%d timeline calls=%d body=%s", response.Code, timeline.calls, response.Body.String())
	}
}

func TestActivityPrivacyScopesOwnerFactsAndCacheControl(t *testing.T) {
	t.Run("anonymous public activity", func(t *testing.T) {
		timeline := &recordingTimeline{}
		request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice?timezone=UTC", nil)
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, SubjectVisibility: staticSubjectVisibility{public: true}, SubjectAuthorizer: allowOwner{},
		}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=300" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if responseVaryContains(recorder.Header(), "Cookie") || responseVaryContains(recorder.Header(), "Authorization") {
			t.Fatalf("public activity varies on credentials: %q", recorder.Header().Values("Vary"))
		}
	})

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

	t.Run("public subject owner", func(t *testing.T) {
		timeline := &recordingTimeline{}
		request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice?timezone=UTC", nil)
		request.Header.Set("Authorization", "Bearer token")
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, Auth: fakeAuth{}, SubjectVisibility: staticSubjectVisibility{public: true}, SubjectAuthorizer: allowOwner{},
		}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		if !timeline.input.IncludeSubjectEnvironments {
			t.Fatal("owner request did not load subject-scoped environments")
		}
		if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("Cache-Control = %q", got)
		}
	})

	t.Run("private subject anonymous", func(t *testing.T) {
		timeline := &recordingTimeline{}
		request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice", nil)
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{
			Timeline: timeline, SubjectVisibility: staticSubjectVisibility{err: subjects.ErrUnauthenticated},
		}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
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

func TestInvalidActivityQueryUsesRFC9457Problem(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice?from=08-01-2026", nil)
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("unexpected content type %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"type", "title", "status", "code", "requestId"} {
		if _, ok := payload[name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
}

func TestStrictJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	for _, body := range []string{`{"email":"a@example.com","unknown":true}`, `{"email":"a@example.com"}{}`} {
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", body, recorder.Code)
		}
	}
}

func TestMagicLinkRequestForwardsOnlyExactlyAllowedRedirect(t *testing.T) {
	allowed := "https://app.example/?auth=magic#auth"
	recorded := &magicLinkRequest{}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth:             fakeAuth{magicLinkRequest: recorded},
		AllowedRedirects: []string{allowed},
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(
		`{"email":"person@example.com","redirectUri":"https://app.example/?auth=magic#auth"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("allowed request status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}
	if recorded.email != "person@example.com" || recorded.redirectURI != allowed {
		t.Fatalf("forwarded request = %+v", recorded)
	}

	recorded.email, recorded.redirectURI = "", ""
	request = httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(
		`{"email":"person@example.com","redirectUri":"https://app.example.evil/?auth=magic#auth"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unallowed request status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if recorded.email != "" || recorded.redirectURI != "" {
		t.Fatalf("unallowed redirect reached auth service: %+v", recorded)
	}
}

func TestBearerAndCookieAuthentication(t *testing.T) {
	for name, configure := range map[string]func(*http.Request){
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer opaque-token") },
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "opaque-token"}) },
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			configure(request)
			recorder := httptest.NewRecorder()
			apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestForceRefreshRequiresAuthenticatedOwner(t *testing.T) {
	for _, deps := range []apihttp.Dependencies{{Timeline: &recordingTimeline{}, Auth: fakeAuth{}}, {Timeline: &recordingTimeline{}, Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}}} {
		request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice?force=true", nil)
		if deps.Auth != nil {
			request.Header.Set("Authorization", "Bearer token")
		}
		recorder := httptest.NewRecorder()
		apihttp.NewRouter(deps).ServeHTTP(recorder, request)
		want := http.StatusServiceUnavailable
		if deps.SubjectAuthorizer != nil {
			want = http.StatusOK
		}
		if recorder.Code != want {
			t.Fatalf("expected %d, got %d: %s", want, recorder.Code, recorder.Body.String())
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

func TestAllContractOperationsAreRouted(t *testing.T) {
	operations := []struct{ method, path string }{
		{"GET", "/healthz"}, {"GET", "/v1/providers"}, {"GET", "/v1/activities/a"}, {"GET", "/v1/render/a.svg"},
		{"POST", "/v1/auth/magic-link/request"}, {"POST", "/v1/auth/magic-link/consume"}, {"POST", "/v1/auth/passkey/register/options"}, {"POST", "/v1/auth/passkey/register/finish"}, {"POST", "/v1/auth/passkey/sign-in/options"}, {"POST", "/v1/auth/passkey/sign-in/finish"}, {"GET", "/v1/auth/session"}, {"DELETE", "/v1/auth/session"}, {"GET", "/v1/auth/sessions"}, {"DELETE", "/v1/auth/sessions"}, {"DELETE", "/v1/auth/sessions/s"},
		{"GET", "/v1/me"}, {"GET", "/v1/me/settings"}, {"PATCH", "/v1/me/settings"}, {"GET", "/v1/subjects"}, {"POST", "/v1/subjects"}, {"GET", "/v1/subjects/a"}, {"PATCH", "/v1/subjects/a"}, {"DELETE", "/v1/subjects/a"}, {"GET", "/v1/subjects/a/settings"}, {"PATCH", "/v1/subjects/a/settings"},
		{"GET", "/v1/subjects/a/provider-connections"}, {"POST", "/v1/subjects/a/provider-connections"}, {"GET", "/v1/subjects/a/provider-connections/c"}, {"PATCH", "/v1/subjects/a/provider-connections/c"}, {"DELETE", "/v1/subjects/a/provider-connections/c"}, {"POST", "/v1/subjects/a/provider-connections/c/sync"}, {"GET", "/v1/integrations/github/callback"}, {"GET", "/v1/sync-jobs/j"},
		{"GET", "/v1/subjects/a/custom-providers"}, {"POST", "/v1/subjects/a/custom-providers"}, {"GET", "/v1/subjects/a/custom-providers/p"}, {"PATCH", "/v1/subjects/a/custom-providers/p"}, {"DELETE", "/v1/subjects/a/custom-providers/p"}, {"POST", "/v1/subjects/a/custom-providers/p/rotate-key"}, {"POST", "/v1/custom-providers/p/activities:ingest"},
	}
	subjectService, err := subjects.NewService(subjects.NewMemoryRepository(), subjects.Config{})
	if err != nil {
		t.Fatalf("construct subjects service: %v", err)
	}
	router := apihttp.NewRouter(apihttp.Dependencies{Timeline: &recordingTimeline{}, Auth: fakeAuth{}, Subjects: subjectService, CustomProviders: inertCustomProviders{}})
	for _, operation := range operations {
		t.Run(operation.method+" "+operation.path, func(t *testing.T) {
			request := httptest.NewRequest(operation.method, operation.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code == http.StatusMethodNotAllowed || recorder.Code == http.StatusNotImplemented || recorder.Code == http.StatusServiceUnavailable ||
				recorder.Code == http.StatusNotFound && !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/problem+json") {
				t.Fatalf("operation is not implemented: %d: %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("X-Request-ID") == "" {
				t.Fatal("operation response is missing X-Request-ID")
			}
		})
	}
}

func TestInternalErrorsAreSanitized(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/activities/alice", nil)
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

func TestPayloadLimitAndUnavailableRetryHeaders(t *testing.T) {
	oversized := `{"email":"` + strings.Repeat("a", (1<<20)+1) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(oversized))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("payload status = %d, want 413: %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	request.Header.Set("Authorization", "Bearer token")
	recorder = httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("unavailable response = %d Retry-After=%q", recorder.Code, recorder.Header().Get("Retry-After"))
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

func TestSyncContractRequiresAndPropagatesIdempotencyAndOptions(t *testing.T) {
	connection := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github",
	}
	syncService := &recordingSync{}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, Connections: staticConnections{item: connection}, Sync: syncService,
	})
	body := `{"from":"2026-08-01","to":"2026-08-12","force":true,"failurePolicy":"purge"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections/"+connection.ID+"/sync", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "sync-request-0001")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("sync response = %d: %s", recorder.Code, recorder.Body.String())
	}
	if syncService.input.IdempotencyKey != "sync-request-0001" || syncService.input.From == nil || *syncService.input.From != "2026-08-01" ||
		syncService.input.To == nil || *syncService.input.To != "2026-08-12" || !syncService.input.Force || syncService.input.FailurePolicy != domain.FetchFailurePurge {
		t.Fatalf("sync input not propagated: %#v", syncService.input)
	}
	if recorder.Header().Get("Location") != "/v1/sync-jobs/22222222-2222-4222-8222-222222222222" {
		t.Fatalf("unexpected Location: %q", recorder.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections/"+connection.ID+"/sync", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status = %d, want 400", recorder.Code)
	}
}

func TestOAuthHTTPFlowBindsSessionAndUsesAllowlistedRedirect(t *testing.T) {
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
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, Connections: connections, OAuthConnections: connections,
		OAuthFlows: map[string]adapteroauth.Flow{"github": flow}, OAuthWebURL: "https://app.example", AllowedRedirects: []string{redirect, "https://app.example/settings/providers"},
		AllowedOrigins: []string{"https://app.example"}, Audit: auditRecorder, AuditSourceKey: []byte("oauth-target-audit-key"),
	})
	body := `{"providerId":"github","authMethod":"oauth2","redirectUri":"https://app.example/#connections"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections", strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "session-binding-token"})
	request.Header.Set("Authorization", "Bearer ignored-api-token")
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || flow.begin.SessionBinding != "session-binding-token" || flow.begin.ClientRedirectURI != redirect {
		t.Fatalf("OAuth begin = %d request=%#v body=%s", recorder.Code, flow.begin, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/integrations/github/callback?state="+strings.Repeat("s", 32)+"&code=code", nil)
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "session-binding-token"})
	request.Header.Set("Authorization", "Bearer ignored-browser-callback-token")
	recorder = httptest.NewRecorder()
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
		"post.v1.subjects.subject.provider-connections": false,
		"get.v1.integrations.provider.callback":         false,
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

func TestTokenAndPublicConnectionsPersistManagedSubjectHandle(t *testing.T) {
	for _, authMethod := range []integrations.AuthMethod{integrations.AuthNone, integrations.AuthToken} {
		t.Run(string(authMethod), func(t *testing.T) {
			var connected integrations.ConnectInput
			item := integrations.ProviderConnection{ID: "11111111-1111-4111-8111-111111111111", SubjectID: "sub_018f", ProviderID: "github", EnvironmentID: "github", AuthMethod: authMethod, Status: integrations.ConnectionActive}
			connections := staticConnections{item: item, connected: &connected}
			router := apihttp.NewRouter(apihttp.Dependencies{
				Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, SubjectResolver: referenceResolver{id: "sub_018f", handle: "octocat"}, Connections: connections,
			})
			body := `{"providerId":"github","authMethod":"` + string(authMethod) + `"`
			if authMethod == integrations.AuthToken {
				body += `,"token":"opaque-provider-token"`
			}
			body += `}`
			request := httptest.NewRequest(http.MethodPost, "/v1/subjects/octocat/provider-connections", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer session-token")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if connected.SubjectID != "sub_018f" || connected.ExternalAccountLogin != "octocat" || connected.ExternalAccountID != "octocat" {
				t.Fatalf("connection identity = %#v", connected)
			}
		})
	}
}

func TestCreateConnectionWiresExplicitPrivateConsentAndResponse(t *testing.T) {
	var connected integrations.ConnectInput
	item := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "sub_018f", ProviderID: "gitlab",
		EnvironmentID: "connection:11111111-1111-4111-8111-111111111111", AuthMethod: integrations.AuthToken,
		Status: integrations.ConnectionActive, PrivateDataEnabled: true,
	}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, SubjectResolver: referenceResolver{id: "sub_018f", handle: "octocat"},
		Connections: staticConnections{item: item, connected: &connected},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/octocat/provider-connections", strings.NewReader(
		`{"providerId":"gitlab","authMethod":"token","token":"opaque","includePrivate":true}`,
	))
	request.Header.Set("Authorization", "Bearer session-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !connected.IncludePrivate {
		t.Fatalf("private consent was not passed to service: %#v", connected)
	}
	var body struct {
		Connection struct {
			PrivateDataEnabled bool `json:"privateDataEnabled"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.Connection.PrivateDataEnabled {
		t.Fatalf("response consent = %#v, error=%v, body=%s", body, err, response.Body.String())
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
