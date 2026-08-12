package apihttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

type denyingRateLimiter struct {
	scopes []string
	keys   []string
	allow  map[string]bool
}

func (limiter *denyingRateLimiter) Allow(_ context.Context, scope, key string) (bool, time.Duration, error) {
	limiter.scopes = append(limiter.scopes, scope)
	limiter.keys = append(limiter.keys, key)
	if limiter.allow[scope] {
		return true, 0, nil
	}
	return false, 17 * time.Second, nil
}

type notSyncableService struct{}

func (notSyncableService) EnqueueManualSync(context.Context, integrations.ManualSyncInput) (integrations.SyncJob, error) {
	return integrations.SyncJob{}, integrations.ErrConnectionNotSyncable
}

func (notSyncableService) GetJob(context.Context, string) (integrations.SyncJob, error) {
	return integrations.SyncJob{}, integrations.ErrNotFound
}

type failingIssuedSessionReadAuth struct {
	fakeAuth
	revoked []string
}

func (service *failingIssuedSessionReadAuth) CompletePasskeyLogin(context.Context, string, []byte, json.RawMessage, auth.SessionMetadata) (auth.SessionGrant, error) {
	return auth.SessionGrant{Token: "passkey-issued-token", SessionID: "session-1", UserID: "user-1", ExpiresAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (*failingIssuedSessionReadAuth) AuthenticateSession(context.Context, string) (auth.User, error) {
	return auth.User{}, auth.ErrInvalidSession
}

func (service *failingIssuedSessionReadAuth) RevokeSession(_ context.Context, token string) error {
	service.revoked = append(service.revoked, token)
	return nil
}

func TestAuthenticationCeremoniesEnforceRateLimitContract(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		scopes []string
		auth   bool
	}{
		{name: "consume magic link", path: "/v1/auth/magic-link/consume", body: `{"token":"magic-secret"}`, scopes: []string{"magic_link_consume_ip", "magic_link_consume_token"}},
		{name: "begin passkey registration", path: "/v1/auth/passkey/register/options", body: `{}`, scopes: []string{"passkey_ip", "passkey_account"}, auth: true},
		{name: "finish passkey registration", path: "/v1/auth/passkey/register/finish", body: `{"ceremonyId":"register-secret"}`, scopes: []string{"passkey_ip", "passkey_account", "passkey_ceremony"}, auth: true},
		{name: "begin passkey sign-in", path: "/v1/auth/passkey/sign-in/options", body: `{"email":"Sensitive.User@Example.Test"}`, scopes: []string{"passkey_ip", "passkey_email"}},
		{name: "finish passkey sign-in", path: "/v1/auth/passkey/sign-in/finish", body: `{"ceremonyId":"signin-secret"}`, scopes: []string{"passkey_ip", "passkey_ceremony"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter := &denyingRateLimiter{}
			router := apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}, RateLimiter: limiter})
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.auth {
				request.Header.Set("Authorization", "Bearer session-token")
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			assertContractProblem(t, recorder, http.StatusTooManyRequests, "rate_limited")
			if got := recorder.Header().Get("Retry-After"); got != "17" {
				t.Fatalf("Retry-After = %q, want 17", got)
			}
			if strings.Join(limiter.scopes, ",") != strings.Join(test.scopes, ",") {
				t.Fatalf("limiter scopes = %v, want %v", limiter.scopes, test.scopes)
			}
			if len(limiter.keys) != len(test.scopes) {
				t.Fatalf("limiter keys = %v, want %d keys", limiter.keys, len(test.scopes))
			}
			for index, key := range limiter.keys {
				if strings.HasSuffix(test.scopes[index], "_ip") {
					if key != "192.0.2.1" {
						t.Fatalf("IP limiter key = %q, want canonical client address", key)
					}
					continue
				}
				if strings.Contains(key, "secret") || strings.Contains(key, "Sensitive.User") || !strings.Contains(key, ":sha256:") {
					t.Fatalf("sensitive limiter identity was not hashed: %q", key)
				}
			}
		})
	}
}

type denyOwner struct{}

func (denyOwner) OwnsSubject(context.Context, string, string) (bool, error) { return false, nil }

func TestPublicActivityAndSVGEnforceIndependentRateLimits(t *testing.T) {
	for _, path := range []string{
		"/v1/activities/octocat?from=2026-08-12&to=2026-08-12",
		"/v1/render/octocat.svg?from=2026-08-12&to=2026-08-12",
	} {
		for _, role := range []struct {
			name       string
			auth       bool
			authorizer interface {
				OwnsSubject(context.Context, string, string) (bool, error)
			}
			scopes []string
		}{
			{name: "anonymous", authorizer: denyOwner{}, scopes: []string{"public_activity_ip"}},
			{name: "authenticated non-owner", auth: true, authorizer: denyOwner{}, scopes: []string{"public_activity_ip", "public_activity_account"}},
			{name: "owner", auth: true, authorizer: allowOwner{}, scopes: []string{"public_activity_ip", "public_activity_account"}},
		} {
			t.Run(path+"/"+role.name, func(t *testing.T) {
				limiter := &denyingRateLimiter{}
				router := apihttp.NewRouter(apihttp.Dependencies{
					Timeline: &recordingTimeline{}, Auth: fakeAuth{}, RateLimiter: limiter,
					SubjectVisibility: staticSubjectVisibility{public: true}, SubjectAuthorizer: role.authorizer,
				})
				request := httptest.NewRequest(http.MethodGet, path, nil)
				if role.auth {
					request.Header.Set("Authorization", "Bearer session-token")
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)

				assertContractProblem(t, recorder, http.StatusTooManyRequests, "rate_limited")
				if recorder.Header().Get("Retry-After") != "17" {
					t.Fatalf("Retry-After = %q", recorder.Header().Get("Retry-After"))
				}
				if strings.Join(limiter.scopes, ",") != strings.Join(role.scopes, ",") || len(limiter.keys) != len(role.scopes) || limiter.keys[0] != "192.0.2.1" {
					t.Fatalf("rate-limit calls = scopes %#v keys %#v", limiter.scopes, limiter.keys)
				}
				if role.auth && (!strings.Contains(limiter.keys[1], ":sha256:") || strings.Contains(limiter.keys[1], "user-1")) {
					t.Fatalf("authenticated public identity is not opaque: %q", limiter.keys[1])
				}
			})
		}
	}
}

func TestPrivateActivityDoesNotConsumePublicFetchBuckets(t *testing.T) {
	limiter := &denyingRateLimiter{}
	request := httptest.NewRequest(http.MethodGet, "/v1/activities/private-subject?from=2026-08-12&to=2026-08-12", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{
		Timeline: &recordingTimeline{}, Auth: fakeAuth{}, RateLimiter: limiter,
		SubjectVisibility: staticSubjectVisibility{public: false}, SubjectAuthorizer: allowOwner{},
	}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("private activity status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(limiter.scopes) != 0 {
		t.Fatalf("private activity consumed unexpected buckets: %v", limiter.scopes)
	}
}

type identityAuth struct{ fakeAuth }

func (identityAuth) AuthenticateSession(_ context.Context, token string) (auth.User, error) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	return auth.User{ID: token, PrimaryEmail: token + "@example.test", Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func TestAuthenticatedCeremonyRateLimitsAccountAcrossIPsAndIPAcrossAccounts(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name                  string
		policy                map[string]handlers.RateLimitPolicy
		firstIP, secondIP     string
		firstUser, secondUser string
	}{
		{
			name: "same account across distributed IPs", policy: map[string]handlers.RateLimitPolicy{"passkey_account": {Limit: 1, Window: time.Hour}},
			firstIP: "192.0.2.10:4000", secondIP: "198.51.100.20:4000", firstUser: "user-one", secondUser: "user-one",
		},
		{
			name: "same IP across unrelated accounts", policy: map[string]handlers.RateLimitPolicy{"passkey_ip": {Limit: 1, Window: time.Hour}},
			firstIP: "192.0.2.10:4000", secondIP: "192.0.2.10:5000", firstUser: "user-one", secondUser: "user-two",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := apihttp.NewRouter(apihttp.Dependencies{
				Auth: identityAuth{}, RateLimiter: handlers.NewMemoryRateLimiter(test.policy, func() time.Time { return now }),
			})
			for index, requestInput := range []struct{ ip, user string }{{test.firstIP, test.firstUser}, {test.secondIP, test.secondUser}} {
				request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/register/options", strings.NewReader(`{}`))
				request.RemoteAddr = requestInput.ip
				request.Header.Set("Authorization", "Bearer "+requestInput.user)
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				want := http.StatusOK
				if index == 1 {
					want = http.StatusTooManyRequests
				}
				if recorder.Code != want {
					t.Fatalf("request %d status = %d, want %d: %s", index+1, recorder.Code, want, recorder.Body.String())
				}
			}
		})
	}
}

func TestMagicLinkRateLimitsAccountAcrossIPsAndIPAcrossAccounts(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		policy   map[string]handlers.RateLimitPolicy
		firstIP  string
		secondIP string
		first    string
		second   string
	}{
		{
			name: "same normalized account across distributed IPs", policy: map[string]handlers.RateLimitPolicy{"magic_link_account": {Limit: 1, Window: time.Hour}},
			firstIP: "192.0.2.10:4000", secondIP: "198.51.100.20:4000", first: " User@Example.Test ", second: "user@example.test",
		},
		{
			name: "same IP across unrelated accounts", policy: map[string]handlers.RateLimitPolicy{"magic_link_ip": {Limit: 1, Window: time.Hour}},
			firstIP: "192.0.2.10:4000", secondIP: "192.0.2.10:5000", first: "first@example.test", second: "second@example.test",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := apihttp.NewRouter(apihttp.Dependencies{
				Auth: fakeAuth{}, RateLimiter: handlers.NewMemoryRateLimiter(test.policy, func() time.Time { return now }),
			})
			for index, requestInput := range []struct{ ip, email string }{{test.firstIP, test.first}, {test.secondIP, test.second}} {
				request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(`{"email":`+strconv.Quote(requestInput.email)+`}`))
				request.RemoteAddr = requestInput.ip
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				want := http.StatusAccepted
				if index == 1 {
					want = http.StatusTooManyRequests
				}
				if recorder.Code != want {
					t.Fatalf("request %d status = %d, want %d: %s", index+1, recorder.Code, want, recorder.Body.String())
				}
			}
		})
	}
}

func TestConnectionNotSyncableUsesConflictProblem(t *testing.T) {
	connection := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github",
	}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, Connections: staticConnections{item: connection}, Sync: notSyncableService{},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections/"+connection.ID+"/sync", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	request.Header.Set("Idempotency-Key", "sync-request-0001")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assertContractProblem(t, recorder, http.StatusConflict, "conflict")
}

func TestSyncEnforcesIndependentOwnerAndIPBuckets(t *testing.T) {
	connection := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github",
	}
	limiter := &denyingRateLimiter{}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, Connections: staticConnections{item: connection}, Sync: notSyncableService{}, RateLimiter: limiter,
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections/"+connection.ID+"/sync", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	request.Header.Set("Idempotency-Key", "sync-request-0001")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assertContractProblem(t, recorder, http.StatusTooManyRequests, "rate_limited")
	if strings.Join(limiter.scopes, ",") != "sync_ip,sync_account" || limiter.keys[0] != "192.0.2.1" {
		t.Fatalf("sync rate-limit calls = scopes %#v keys %#v", limiter.scopes, limiter.keys)
	}
	if strings.Contains(limiter.keys[1], "user-1") || strings.Contains(limiter.keys[1], "subject-1") || !strings.Contains(limiter.keys[1], ":sha256:") {
		t.Fatalf("sync owner key is not opaque: %q", limiter.keys[1])
	}
}

func TestIssuedSessionReadFailureDoesNotCommitCookieAndRevokesSession(t *testing.T) {
	tests := []struct {
		name, path, body, issuedToken string
	}{
		{name: "magic link", path: "/v1/auth/magic-link/consume", body: `{"token":"magic-secret"}`, issuedToken: "issued-token"},
		{name: "passkey", path: "/v1/auth/passkey/sign-in/finish", body: `{"ceremonyId":"ceremony-secret","credential":{"id":"AQ","rawId":"AQ","type":"public-key","response":{},"clientExtensionResults":{}}}`, issuedToken: "passkey-issued-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &failingIssuedSessionReadAuth{}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			apihttp.NewRouter(apihttp.Dependencies{Auth: service}).ServeHTTP(recorder, request)

			assertContractProblem(t, recorder, http.StatusUnauthorized, "unauthorized")
			if values := recorder.Header().Values("Set-Cookie"); len(values) != 0 {
				t.Fatalf("failed sign-in committed cookies: %v", values)
			}
			if len(service.revoked) != 1 || service.revoked[0] != test.issuedToken {
				t.Fatalf("issued session rollback = %v, want [%s]", service.revoked, test.issuedToken)
			}
		})
	}
}

func TestOversizedTrailingJSONWhitespaceUsesPayloadTooLargeProblem(t *testing.T) {
	body := `{}` + strings.Repeat(" ", (1<<20)+1)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)

	assertContractProblem(t, recorder, http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestPasskeySignInRejectsNonObjectCredentialAsBadRequest(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", strings.NewReader(`{"ceremonyId":"ceremony-1","credential":"not-an-object"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)

	assertContractProblem(t, recorder, http.StatusBadRequest, "invalid_request")
}

func assertContractProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	var body struct {
		Type      string `json:"type"`
		Status    int    `json:"status"`
		Code      string `json:"code"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Status != status || body.Code != code || body.Type != "https://jandibat.org/problems/"+code || body.RequestID == "" {
		t.Fatalf("problem = %+v", body)
	}
}
