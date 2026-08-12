package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type forgeProviderContract struct {
	key            string
	successFixture string
	successPath    string
	wantFacts      int
	new            func(*http.Client, string) (app.Provider, error)
}

func forgeProviderContracts() []forgeProviderContract {
	return []forgeProviderContract{
		{
			key: GitHubKey, successFixture: "github-events.json",
			successPath: "/users/moreal/events/public", wantFacts: 3,
			new: func(client *http.Client, baseURL string) (app.Provider, error) {
				return NewGitHub(GitHubConfig{HTTPClient: client, BaseURL: baseURL})
			},
		},
		{
			key: GitLabKey, successFixture: "gitlab-events.json",
			successPath: "/users/moreal/events", wantFacts: 3,
			new: func(client *http.Client, baseURL string) (app.Provider, error) {
				return NewGitLab(GitLabConfig{HTTPClient: client, BaseURL: baseURL})
			},
		},
		{
			key: CodebergKey, successFixture: "codeberg-heatmap.json",
			successPath: "/users/moreal/heatmap", wantFacts: 1,
			new: func(client *http.Client, baseURL string) (app.Provider, error) {
				return NewCodeberg(CodebergConfig{HTTPClient: client, BaseURL: baseURL})
			},
		},
	}
}

// TestForgeProviderContractSuite runs the same observable contract against
// every built-in forge adapter. Provider-specific fixture tests remain useful
// for source mappings, while this suite prevents drift in shared behavior.
func TestForgeProviderContractSuite(t *testing.T) {
	for _, contract := range forgeProviderContracts() {
		t.Run(contract.key, func(t *testing.T) {
			t.Run("environment metadata", func(t *testing.T) {
				provider := newContractProvider(t, contract, http.StatusOK, contract.successFixture, nil)
				environment := provider.Environment()
				if environment.ID != domain.EnvironmentID(contract.key) || environment.Key != contract.key ||
					environment.Name == "" || environment.Scope != domain.EnvironmentScopeGlobal ||
					environment.Metadata["category"] != "git-hosting" {
					t.Fatalf("Environment() = %#v", environment)
				}
			})

			t.Run("success normalization", func(t *testing.T) {
				payload := fixture(t, contract.successFixture)
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if request.Method != http.MethodGet || request.URL.Path != contract.successPath {
						t.Errorf("unexpected request: %s %s", request.Method, request.URL.String())
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write(payload)
				}))
				defer server.Close()
				provider, err := contract.new(server.Client(), server.URL)
				if err != nil {
					t.Fatal(err)
				}
				input := fetchInput()
				input.Subject = "local-subject"
				input.ProviderSubject = "moreal"
				facts, err := provider.Fetch(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				if len(facts) != contract.wantFacts {
					t.Fatalf("facts len = %d, want %d: %#v", len(facts), contract.wantFacts, facts)
				}
				for _, fact := range facts {
					if fact.Subject != input.Subject || fact.EnvironmentID != domain.EnvironmentID(contract.key) ||
						fact.Date < input.From || fact.Date > input.To || fact.Metric.Value <= 0 {
						t.Fatalf("fact violates shared contract: %#v", fact)
					}
				}
			})

			t.Run("rejects invalid input before network", func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					requests.Add(1)
				}))
				defer server.Close()
				provider, err := contract.new(server.Client(), server.URL)
				if err != nil {
					t.Fatal(err)
				}
				inputs := []struct {
					name  string
					input app.ProviderFetchInput
					want  error
				}{
					{name: "unsafe subject", input: app.ProviderFetchInput{Subject: "../private", Timezone: "UTC", From: "2026-03-02", To: "2026-03-03"}, want: ErrInvalidSubject},
					{name: "unknown timezone", input: app.ProviderFetchInput{Subject: "moreal", Timezone: "Mars/Olympus", From: "2026-03-02", To: "2026-03-03"}, want: ErrInvalidTimezone},
					{name: "reversed range", input: app.ProviderFetchInput{Subject: "moreal", Timezone: "UTC", From: "2026-03-03", To: "2026-03-02"}, want: ErrInvalidRange},
				}
				for _, input := range inputs {
					t.Run(input.name, func(t *testing.T) {
						if _, err := provider.Fetch(context.Background(), input.input); !errors.Is(err, input.want) {
							t.Fatalf("Fetch() error = %v, want %v", err, input.want)
						}
					})
				}
				if requests.Load() != 0 {
					t.Fatalf("invalid inputs made %d network requests", requests.Load())
				}
			})

			t.Run("account not found is empty snapshot", func(t *testing.T) {
				provider := newContractProvider(t, contract, http.StatusNotFound, "account-not-found.json", nil)
				facts, err := provider.Fetch(context.Background(), fetchInput())
				if err != nil || facts == nil || len(facts) != 0 {
					t.Fatalf("facts=%#v error=%v", facts, err)
				}
			})

			t.Run("429 is sanitized rate limit", func(t *testing.T) {
				provider := newContractProvider(t, contract, http.StatusTooManyRequests, "rate-limited.json", func(header http.Header) {
					header.Set("Retry-After", "12")
				})
				_, err := provider.Fetch(context.Background(), fetchInput())
				assertContractHTTPError(t, err, contract.key, http.StatusTooManyRequests, true, 12*time.Second, "fixture-rate-limit-secret")
			})

			t.Run("5xx is sanitized upstream error", func(t *testing.T) {
				provider := newContractProvider(t, contract, http.StatusBadGateway, "server-error.json", nil)
				_, err := provider.Fetch(context.Background(), fetchInput())
				assertContractHTTPError(t, err, contract.key, http.StatusBadGateway, false, 0, "fixture-server-secret")
			})

			t.Run("malformed JSON is sanitized invalid response", func(t *testing.T) {
				provider := newContractProvider(t, contract, http.StatusOK, "malformed.json", nil)
				_, err := provider.Fetch(context.Background(), fetchInput())
				if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), contract.key) ||
					strings.Contains(err.Error(), "fixture-malformed-secret") {
					t.Fatalf("unsafe or incorrect malformed response error: %v", err)
				}
			})
		})
	}
}

func newContractProvider(
	t *testing.T,
	contract forgeProviderContract,
	status int,
	fixtureName string,
	headers func(http.Header),
) app.Provider {
	t.Helper()
	payload := fixture(t, fixtureName)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if headers != nil {
			headers(writer.Header())
		}
		writer.WriteHeader(status)
		_, _ = writer.Write(payload)
	}))
	t.Cleanup(server.Close)
	provider, err := contract.new(server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func assertContractHTTPError(
	t *testing.T,
	err error,
	providerKey string,
	status int,
	rateLimited bool,
	retryAfter time.Duration,
	secret string,
) {
	t.Helper()
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.Provider != providerKey || httpError.StatusCode != status ||
		httpError.RateLimited != rateLimited || httpError.RetryAfter != retryAfter || httpError.Body != "" ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe or incorrect HTTP error: %#v / %v", httpError, err)
	}
	if rateLimited && !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}
