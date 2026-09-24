package apihttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
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

type failingIssuedSessionReadAuth struct {
	fakeAuth
	revoked []string
}

func (*failingIssuedSessionReadAuth) AuthenticateSession(context.Context, string) (auth.User, error) {
	return auth.User{}, auth.ErrInvalidSession
}

func (service *failingIssuedSessionReadAuth) RevokeSession(_ context.Context, token string) error {
	service.revoked = append(service.revoked, token)
	return nil
}

func TestMagicConsumeEnforcesRateLimitContract(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		scopes []string
	}{
		{name: "consume magic link", path: "/v1/auth/magic-link/consume", body: `{"token":"magic-secret"}`, scopes: []string{"magic_link_consume_ip", "magic_link_consume_token"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter := &denyingRateLimiter{}
			router := apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}, RateLimiter: limiter})
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
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

func TestTrustedProxyHeadersRequireOneCanonicalClientAddress(t *testing.T) {
	for _, test := range []struct {
		name       string
		xForwarded string
		xRealIP    string
		want       string
	}{
		{name: "single rewritten address", xForwarded: "198.51.100.20", want: "198.51.100.20"},
		{name: "ambiguous forwarding chain", xForwarded: "198.51.100.20, 127.0.0.1", want: "192.0.2.44"},
		{name: "conflicting header families", xForwarded: "198.51.100.20", xRealIP: "203.0.113.8", want: "192.0.2.44"},
	} {
		t.Run(test.name, func(t *testing.T) {
			limiter := &denyingRateLimiter{}
			router := apihttp.NewRouter(apihttp.Dependencies{
				Auth: fakeAuth{}, RateLimiter: limiter, TrustProxyHeaders: true,
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(`{"token":"opaque"}`))
			request.RemoteAddr = "192.0.2.44:12345"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-For", test.xForwarded)
			request.Header.Set("X-Real-IP", test.xRealIP)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			assertContractProblem(t, response, http.StatusTooManyRequests, "rate_limited")
			if len(limiter.keys) == 0 || limiter.keys[0] != test.want {
				t.Fatalf("IP limiter keys = %v, want %q", limiter.keys, test.want)
			}
		})
	}
}

type denyOwner struct{}

func (denyOwner) OwnsSubject(context.Context, string, string) (bool, error) { return false, nil }

func TestPublicSVGEnforcesIndependentRateLimits(t *testing.T) {
	path := "/v1/render/octocat.svg?from=2026-08-12&to=2026-08-12"
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

func TestIssuedSessionReadFailureDoesNotCommitCookieAndRevokesSession(t *testing.T) {
	tests := []struct {
		name, path, body, issuedToken string
	}{
		{name: "magic link", path: "/v1/auth/magic-link/consume", body: `{"token":"magic-secret"}`, issuedToken: "issued-token"},
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
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}}).ServeHTTP(recorder, request)

	assertContractProblem(t, recorder, http.StatusRequestEntityTooLarge, "payload_too_large")
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
