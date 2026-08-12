package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type revokeRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn revokeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func revokeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newRevocationAdapter(t *testing.T, provider string, transport http.RoundTripper) *Adapter {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		response, err := transport.RoundTrip(request)
		if err != nil {
			http.Error(responseWriter, "fixture transport failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			responseWriter.Header()[key] = append([]string(nil), values...)
		}
		responseWriter.WriteHeader(response.StatusCode)
		_, _ = io.Copy(responseWriter, response.Body)
	}))
	t.Cleanup(server.Close)
	config := Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURI:  "https://app.example.test/callback",
		Endpoints: Endpoints{
			AuthorizationURL: server.URL + "/authorize",
			TokenURL:         server.URL + "/token",
			IdentityURL:      server.URL + "/user",
			RevocationURL:    server.URL + "/revoke",
		},
		HTTPClient: server.Client(),
	}
	var (
		adapter *Adapter
		err     error
	)
	switch provider {
	case "github":
		adapter, err = NewGitHub(config)
	case "gitlab":
		adapter, err = NewGitLab(config)
	case "codeberg":
		adapter, err = NewCodeberg(config)
	default:
		t.Fatalf("unknown provider %q", provider)
	}
	if err != nil {
		t.Fatalf("construct %s adapter: %v", provider, err)
	}
	return adapter
}

func TestGitHubTokenRevocationUsesOfficialApplicationTokenContract(t *testing.T) {
	var calls int
	adapter := newRevocationAdapter(t, "github", revokeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodDelete || request.URL.Path != "/revoke/client-id/token" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != "client-id" || password != "client-secret" {
			t.Errorf("basic auth = %q/%q, %v", username, password, ok)
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" ||
			request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("headers = %#v", request.Header)
		}
		var payload struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.AccessToken != `secret"token` {
			t.Errorf("payload = %#v, %v", payload, err)
		}
		return revokeResponse(http.StatusNoContent, ""), nil
	}))

	if err := adapter.RevokeToken(context.Background(), []byte(`secret"token`)); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestGitLabTokenRevocationUsesOfficialOAuthFormContract(t *testing.T) {
	adapter := newRevocationAdapter(t, "gitlab", revokeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/revoke" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"client_id": "client-id", "client_secret": "client-secret",
			"token": "gitlab token&value", "token_type_hint": "access_token",
		} {
			if got := values.Get(key); got != want {
				t.Errorf("form[%q] = %q, want %q", key, got, want)
			}
		}
		return revokeResponse(http.StatusOK, `{}`), nil
	}))

	if err := adapter.RevokeToken(context.Background(), []byte("gitlab token&value")); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
}

func TestCodebergTokenRevocationIsExplicitlyLocalOnly(t *testing.T) {
	adapter := newRevocationAdapter(t, "codeberg", revokeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("Codeberg local-only revocation made a network request")
		return nil, errors.New("unreachable")
	}))
	if err := adapter.RevokeToken(context.Background(), []byte("codeberg-token")); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
}

func TestTokenRevocationRejectsRedirectsAndSanitizesFailures(t *testing.T) {
	secret := "sensitive-token"
	var calls int
	adapter := newRevocationAdapter(t, "gitlab", revokeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		response := revokeResponse(http.StatusFound, "provider leaked "+secret+" and client-secret")
		response.Header.Set("Location", "https://attacker.example/collect")
		response.Request = request
		return response, nil
	}))
	err := adapter.RevokeToken(context.Background(), []byte(secret))
	if !errors.Is(err, ErrTokenRevocation) {
		t.Fatalf("RevokeToken() error = %v, want ErrTokenRevocation", err)
	}
	if calls != 1 {
		t.Fatalf("redirect was followed: calls = %d", calls)
	}
	for _, leaked := range []string{secret, "client-secret", "provider leaked"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("sanitized error exposed %q: %v", leaked, err)
		}
	}
}

func TestTokenRevocationBoundsProviderResponse(t *testing.T) {
	adapter := newRevocationAdapter(t, "gitlab", revokeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return revokeResponse(http.StatusOK, strings.Repeat("x", maxProviderResponseBytes+1)), nil
	}))
	if err := adapter.RevokeToken(context.Background(), []byte("token")); !errors.Is(err, ErrTokenRevocation) {
		t.Fatalf("oversized response error = %v, want ErrTokenRevocation", err)
	}
}

func TestTokenRevocationUsesBoundedRequestDeadline(t *testing.T) {
	constructors := map[string]func(Config) (*Adapter, error){
		"github": NewGitHub,
		"gitlab": NewGitLab,
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			observer := &deadlineObserver{next: server.Client().Transport, observed: make(chan time.Time, 1)}
			adapter, err := construct(Config{
				ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "https://app.example.test/callback",
				Endpoints: Endpoints{
					AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token",
					IdentityURL: server.URL + "/user", RevocationURL: server.URL + "/revoke",
				},
				HTTPClient: &http.Client{Transport: observer},
			})
			if err != nil {
				t.Fatalf("construct adapter: %v", err)
			}
			if err := adapter.RevokeToken(context.Background(), []byte("token")); err != nil {
				t.Fatalf("RevokeToken: %v", err)
			}
			select {
			case deadline := <-observer.observed:
				remaining := time.Until(deadline)
				if remaining <= 0 || remaining > tokenRevocationTimeout {
					t.Fatalf("revocation deadline remaining = %s", remaining)
				}
			default:
				t.Fatal("revocation request deadline was not observed")
			}
		})
	}
}

type deadlineObserver struct {
	next     http.RoundTripper
	observed chan time.Time
}

func (observer *deadlineObserver) ProviderBaseTransport() http.RoundTripper { return observer.next }
func (observer *deadlineObserver) WrapProviderTransport(next http.RoundTripper) http.RoundTripper {
	observer.next = next
	return observer
}
func (observer *deadlineObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	if deadline, ok := request.Context().Deadline(); ok {
		observer.observed <- deadline
	}
	return observer.next.RoundTrip(request)
}

var _ TokenRevoker = (*Adapter)(nil)
