package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

func TestProviderAuthorizationCodeFlow(t *testing.T) {
	tests := []struct {
		name             string
		providerID       string
		newAdapter       func(Config) (*Adapter, error)
		expectedUsername string
	}{
		{name: "GitHub", providerID: "github", newAdapter: NewGitHub, expectedUsername: "octocat"},
		{name: "GitLab", providerID: "gitlab", newAdapter: NewGitLab, expectedUsername: "gitlab-user"},
		{name: "Codeberg", providerID: "codeberg", newAdapter: NewCodeberg, expectedUsername: "octocat"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &fixedClock{now: time.Date(2026, time.August, 12, 4, 0, 0, 0, time.UTC)}
			stateBytes := bytes.Repeat([]byte{1}, 32)
			verifierBytes := bytes.Repeat([]byte{2}, 32)
			expectedVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
			var tokenRequests atomic.Int32
			var identityRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/token":
					tokenRequests.Add(1)
					if request.Method != http.MethodPost {
						t.Errorf("token method = %s", request.Method)
					}
					if got := request.Header.Get("Accept"); got != "application/json" {
						t.Errorf("token Accept = %q", got)
					}
					if err := request.ParseForm(); err != nil {
						t.Fatalf("ParseForm: %v", err)
					}
					want := url.Values{
						"grant_type":    {"authorization_code"},
						"client_id":     {"client-id"},
						"client_secret": {"client-secret"},
						"code":          {"authorization-code"},
						"redirect_uri":  {"https://jandibat.test/v1/integrations/" + test.providerID + "/callback"},
						"code_verifier": {expectedVerifier},
					}
					if request.PostForm.Encode() != want.Encode() {
						t.Errorf("token form = %v, want %v", request.PostForm, want)
					}
					response.Header().Set("Content-Type", "application/json")
					fmt.Fprint(response, `{"access_token":"access-secret","refresh_token":"refresh-secret","expires_in":3600,"scope":"scope-a,scope-b"}`)
				case "/identity":
					identityRequests.Add(1)
					if got := request.Header.Get("Authorization"); got != "Bearer access-secret" {
						t.Errorf("identity authorization = %q", got)
					}
					response.Header().Set("Content-Type", "application/json")
					fmt.Fprint(response, `{"id":123,"login":"octocat","username":"gitlab-user","name":"Octo Cat","avatar_url":"https://images.test/avatar"}`)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()

			adapter, err := test.newAdapter(Config{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				RedirectURI:  "https://jandibat.test/v1/integrations/" + test.providerID + "/callback",
				Scopes:       []string{"scope-a", "scope-a", "scope-b"},
				Endpoints: Endpoints{
					AuthorizationURL: server.URL + "/authorize?existing=value",
					TokenURL:         server.URL + "/token",
					IdentityURL:      server.URL + "/identity",
				},
				HTTPClient: server.Client(),
				Clock:      clock,
				Random:     bytes.NewReader(append(stateBytes, verifierBytes...)),
				StateTTL:   5 * time.Minute,
			})
			if err != nil {
				t.Fatalf("new adapter: %v", err)
			}
			if got := adapter.ProviderID(); got != test.providerID {
				t.Fatalf("ProviderID = %q", got)
			}

			authorization, err := adapter.Begin(context.Background(), AuthorizationRequest{
				ConnectionID: "connection-1",
				SubjectID:    "subject-1",
			})
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			parsed, err := url.Parse(authorization.URL)
			if err != nil {
				t.Fatalf("parse authorization URL: %v", err)
			}
			query := parsed.Query()
			if parsed.Path != "/authorize" || query.Get("existing") != "value" {
				t.Errorf("authorization URL = %s", authorization.URL)
			}
			if query.Get("client_id") != "client-id" || query.Get("redirect_uri") != "https://jandibat.test/v1/integrations/"+test.providerID+"/callback" {
				t.Errorf("authorization client/redirect = %v", query)
			}
			if query.Get("response_type") != "code" || query.Get("state") != authorization.State || query.Get("scope") != "scope-a scope-b" {
				t.Errorf("authorization query = %v", query)
			}
			if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") != pkceChallenge(expectedVerifier) {
				t.Errorf("PKCE query = %v", query)
			}
			if !authorization.ExpiresAt.Equal(clock.now.Add(5 * time.Minute)) {
				t.Errorf("ExpiresAt = %s", authorization.ExpiresAt)
			}

			result, err := adapter.Complete(context.Background(), CallbackRequest{
				State: authorization.State,
				Code:  "authorization-code",
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if result.ProviderID != test.providerID || result.ConnectionID != "connection-1" || result.SubjectID != "subject-1" {
				t.Errorf("callback binding = %+v", result)
			}
			if result.Identity.ExternalAccountID != "123" || result.Identity.Username != test.expectedUsername || result.Identity.DisplayName != "Octo Cat" {
				t.Errorf("identity = %+v", result.Identity)
			}
			if result.Credentials.AccessToken != "access-secret" || result.Credentials.RefreshToken != "refresh-secret" {
				t.Errorf("credentials were not returned")
			}
			if result.Credentials.ExpiresAt == nil || !result.Credentials.ExpiresAt.Equal(clock.now.Add(time.Hour)) {
				t.Errorf("credential expiry = %v", result.Credentials.ExpiresAt)
			}
			if strings.Join(result.Scopes, ",") != "scope-a,scope-b" {
				t.Errorf("scopes = %v", result.Scopes)
			}
			if tokenRequests.Load() != 1 || identityRequests.Load() != 1 {
				t.Errorf("requests token=%d identity=%d", tokenRequests.Load(), identityRequests.Load())
			}
			if _, err := adapter.Complete(context.Background(), CallbackRequest{State: authorization.State, Code: "replay"}); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("replayed Complete error = %v", err)
			}
		})
	}
}

func TestMemoryStateStoreHashesExpiresAndConsumesAtomically(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, time.August, 12, 4, 0, 0, 0, time.UTC)}
	store := NewMemoryStateStore(clock)
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	flow := FlowState{ConnectionID: "connection", CodeVerifier: "verifier", ExpiresAt: clock.now.Add(time.Minute)}
	if err := store.Put(context.Background(), state, flow); err != nil {
		t.Fatalf("Put: %v", err)
	}
	hash := sha256.Sum256([]byte(state))
	if _, exists := store.entries[hash]; !exists {
		t.Fatalf("state hash was not stored")
	}

	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.Consume(context.Background(), state, StateBinding{}); err == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful concurrent consumes = %d", successes.Load())
	}

	expiringState := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	if err := store.Put(context.Background(), expiringState, FlowState{ExpiresAt: clock.now.Add(time.Minute)}); err != nil {
		t.Fatalf("Put expiring: %v", err)
	}
	clock.now = clock.now.Add(time.Minute)
	if _, err := store.Consume(context.Background(), expiringState, StateBinding{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expired Consume error = %v", err)
	}
	if _, err := store.Consume(context.Background(), "not-valid-base64", StateBinding{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("malformed Consume error = %v", err)
	}
}

func TestCallbackErrorsAreOneTimeAndDoNotExposeSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			response.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(response, `provider response contains authorization-code and client-secret`)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	newAdapter := func(randomByte byte) *Adapter {
		adapter, err := NewGitHub(Config{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RedirectURI:  "https://jandibat.test/callback",
			Endpoints: Endpoints{
				AuthorizationURL: server.URL + "/authorize",
				TokenURL:         server.URL + "/token",
				IdentityURL:      server.URL + "/identity",
			},
			HTTPClient: server.Client(),
			Random: bytes.NewReader(append(
				bytes.Repeat([]byte{randomByte}, 32),
				bytes.Repeat([]byte{randomByte + 1}, 32)...,
			)),
		})
		if err != nil {
			t.Fatalf("NewGitHub: %v", err)
		}
		return adapter
	}

	deniedAdapter := newAdapter(10)
	denied, err := deniedAdapter.Begin(context.Background(), AuthorizationRequest{ConnectionID: "c1", SubjectID: "s1"})
	if err != nil {
		t.Fatalf("Begin denied: %v", err)
	}
	if _, err := deniedAdapter.Complete(context.Background(), CallbackRequest{State: denied.State, Error: "access_denied"}); !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("denied Complete error = %v", err)
	}
	if _, err := deniedAdapter.Complete(context.Background(), CallbackRequest{State: denied.State, Code: "later"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("denied replay error = %v", err)
	}

	failingAdapter := newAdapter(20)
	failing, err := failingAdapter.Begin(context.Background(), AuthorizationRequest{ConnectionID: "c2", SubjectID: "s2"})
	if err != nil {
		t.Fatalf("Begin failing: %v", err)
	}
	_, err = failingAdapter.Complete(context.Background(), CallbackRequest{State: failing.State, Code: "authorization-code"})
	if !errors.Is(err, ErrTokenExchange) {
		t.Fatalf("failed Complete error = %v", err)
	}
	for _, secret := range []string{"authorization-code", "client-secret", "provider response"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error exposed %q: %v", secret, err)
		}
	}
}

func TestIdentityFailureRevokesFreshlyIssuedToken(t *testing.T) {
	var revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprint(response, `{"access_token":"fresh-token","token_type":"bearer"}`)
		case "/identity":
			response.WriteHeader(http.StatusBadGateway)
		case "/revoke/client-id/token":
			revoked = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := NewGitHub(Config{
		ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "https://jandibat.test/callback",
		Endpoints:  Endpoints{AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token", IdentityURL: server.URL + "/identity", RevocationURL: server.URL + "/revoke"},
		HTTPClient: server.Client(), Random: bytes.NewReader(bytes.Repeat([]byte{44}, 64)),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := adapter.Begin(context.Background(), AuthorizationRequest{ConnectionID: "connection", SubjectID: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Complete(context.Background(), CallbackRequest{State: started.State, Code: "code"}); !errors.Is(err, ErrIdentityLookup) {
		t.Fatalf("Complete() = %v", err)
	}
	if !revoked {
		t.Fatal("fresh token was not revoked after identity failure")
	}
}

func TestAdapterConfigurationAndRequestValidation(t *testing.T) {
	if _, err := NewGitHub(Config{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("empty config error = %v", err)
	}
	if _, err := NewGitHub(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "relative"}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("relative redirect error = %v", err)
	}
	adapter, err := NewGitHub(Config{
		ClientID: "id", ClientSecret: "secret", RedirectURI: "https://jandibat.test/callback",
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)),
	})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	if _, err := adapter.Begin(context.Background(), AuthorizationRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty Begin error = %v", err)
	}
	if _, err := adapter.Begin(context.Background(), AuthorizationRequest{ConnectionID: "connection", SubjectID: "subject", Scopes: []string{"repo"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("disallowed scope error = %v, want ErrInvalidRequest", err)
	}

	bound, err := NewGitHub(Config{
		ClientID: "id", ClientSecret: "secret", RedirectURI: "https://jandibat.test/callback",
		Random: bytes.NewReader(append(bytes.Repeat([]byte{2}, 64), bytes.Repeat([]byte{3}, 64)...)), RequireSessionBinding: true,
	})
	if err != nil {
		t.Fatalf("NewGitHub bound: %v", err)
	}
	if _, err := bound.Begin(context.Background(), AuthorizationRequest{ConnectionID: "connection", SubjectID: "subject"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing session binding error = %v, want ErrInvalidRequest", err)
	}
	first, err := bound.Begin(context.Background(), AuthorizationRequest{ConnectionID: "connection", SubjectID: "subject", SessionBinding: "session-one"})
	if err != nil {
		t.Fatalf("bound Begin: %v", err)
	}
	if _, err := bound.Complete(context.Background(), CallbackRequest{State: first.State, Error: "access_denied", SessionBinding: "another-session"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mismatched session callback error = %v, want ErrInvalidState", err)
	}
	if _, err := bound.Complete(context.Background(), CallbackRequest{State: first.State, Error: "access_denied", SessionBinding: "session-one"}); !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("matching session callback error = %v, want ErrAuthorizationDenied", err)
	}
	if _, err := bound.Complete(context.Background(), CallbackRequest{State: first.State, Code: "replay", SessionBinding: "session-one"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("replayed matching session callback error = %v, want ErrInvalidState", err)
	}
}

// Compile-time contract checks keep the adapter and ConnectionService seam
// visible even though router wiring belongs to another ownership boundary.
var _ Flow = (*Adapter)(nil)
var _ ConnectionCompleter = (*integrations.ConnectionService)(nil)
