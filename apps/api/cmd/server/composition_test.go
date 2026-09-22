package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"go.uber.org/zap"
)

func TestBuildApplicationDevelopmentUsesExplicitDependencies(t *testing.T) {
	settings := developmentConfig(t)
	settings.DevelopmentAllInOne = true
	settings.TrustProxyHeaders = true
	app, err := buildApplication(context.Background(), settings, zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})

	deps := app.dependencies
	if deps.Timeline == nil || deps.Readiness == nil || deps.Audit == nil || deps.Catalog == nil || deps.Auth == nil || deps.Sessions == nil ||
		deps.Connections == nil || deps.OAuthConnections == nil || deps.OAuthFlows == nil ||
		deps.CustomProviders == nil || deps.Sync == nil ||
		deps.SubjectAuthorizer == nil || deps.SubjectVisibility == nil || deps.SubjectResolver == nil || deps.Subjects == nil ||
		deps.RateLimiter == nil {
		t.Fatalf("runtime dependencies are incomplete: %#v", deps)
	}
	if app.scheduler == nil || app.oauth == nil {
		t.Fatal("background scheduler and OAuth registry must be constructed")
	}
	if len(app.background) != 1 || app.background[0].runner != app.scheduler {
		t.Fatalf("development background runners = %#v", app.background)
	}
	if len(app.oauth.flows) != 0 {
		t.Fatalf("development without OAuth credentials must not invent live flows: %#v", app.oauth.flows)
	}
	if len(deps.AllowedOrigins) != 1 || deps.AllowedOrigins[0] != "http://localhost:5173" {
		t.Fatalf("unexpected allowed origins: %#v", deps.AllowedOrigins)
	}
	if len(deps.AllowedRedirects) < 2 || deps.AllowedRedirects[1] != "http://localhost:5173/?auth=magic#auth" {
		t.Fatalf("unexpected redirect allowlist: %#v", deps.AllowedRedirects)
	}
	if deps.OAuthWebURL != settings.WebURL.String() {
		t.Fatalf("OAuth web URL = %q, want %q", deps.OAuthWebURL, settings.WebURL.String())
	}
	if !deps.TrustProxyHeaders {
		t.Fatal("trusted proxy setting was not propagated")
	}

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	newProcessHandler(deps, operations.NewLiveness(nil)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"dependencies":null`) {
		t.Fatalf("health response came from an incomplete dependency graph: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"local-storage":"ok"`) {
		t.Fatalf("health response did not exercise local storage readiness: %s", recorder.Body.String())
	}
}

func TestAPIProviderHTTPClientEmitsDependencyMetrics(t *testing.T) {
	registry := observability.Default()
	registry.SetResource(observability.Resource{Environment: "test"})
	client := newProviderHTTPClient()
	instrumented, ok := client.Transport.(interface {
		WrapProviderTransport(http.RoundTripper) http.RoundTripper
	})
	if !ok {
		t.Fatalf("API provider transport is not instrumented: %T", client.Transport)
	}
	client.Transport = instrumented.WrapProviderTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody, Header: make(http.Header)}, nil
	}))
	request, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("provider request = %#v, %v", response, err)
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), `provider_requests_total{provider="github",outcome="5xx"`) {
		t.Fatalf("provider metric missing:\n%s", recorder.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestAPIProcessExposesSeparateLiveAndReadyEndpoints(t *testing.T) {
	checker, err := operations.NewReadinessChecker(time.Second, operations.ReadinessDependency{
		Name: "database", Probe: operations.DependencyProbeFunc(func(context.Context) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newProcessHandler(apihttp.Dependencies{Readiness: checker}, operations.NewLiveness(nil))
	for _, path := range []string{"/livez", "/readyz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestBuildApplicationDefaultsToAPIOnlyProcess(t *testing.T) {
	settings := developmentConfig(t)
	app, err := buildApplication(context.Background(), settings, zap.NewNop())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if app.scheduler != nil || len(app.background) != 0 {
		t.Fatalf("API-only runtime started background work: scheduler=%v background=%#v", app.scheduler, app.background)
	}
	if app.dependencies.Sync == nil || app.dependencies.Audit == nil {
		t.Fatalf("API queue/audit dependencies missing: %#v", app.dependencies)
	}
}

func TestValidateRuntimeConfigProductionFailsClosed(t *testing.T) {
	settings := productionConfig(t)
	settings.DatabaseURL = ""
	settings.SMTPPassword = "must-not-reach-api"
	settings.GitLabClientSecret = ""
	settings.WebURL = mustURL(t, "http://example.com")
	settings.CredentialCipherKey = append([]byte(nil), settings.SessionSigningKey[:32]...)

	err := validateRuntimeConfig(settings)
	for _, target := range []error{
		errDatabaseRequired,
		errOAuthRequired,
		errHTTPSRequired,
		config.ErrForbiddenSecret,
	} {
		if !errors.Is(err, target) {
			t.Errorf("expected %v in validation error, got %v", target, err)
		}
	}
}

func TestValidateRuntimeConfigAcceptsCompleteProductionConfig(t *testing.T) {
	if err := validateRuntimeConfig(productionConfig(t)); err != nil {
		t.Fatalf("validate complete production config: %v", err)
	}
}

func TestValidateRuntimeConfigRejectsProductionAllInOne(t *testing.T) {
	settings := productionConfig(t)
	settings.DevelopmentAllInOne = true
	if err := validateRuntimeConfig(settings); !errors.Is(err, errAllInOneProduction) {
		t.Fatalf("validate production all-in-one error = %v", err)
	}
}

func TestBuildOAuthRegistryUsesFixedPublicCallbacks(t *testing.T) {
	settings := developmentConfig(t)
	settings.PublicURL = mustURL(t, "https://api.example.com/base/")
	settings.GitHubClientID = "github-client"
	settings.GitHubClientSecret = "github-secret"
	settings.GitLabClientID = "gitlab-client"
	settings.GitLabClientSecret = "gitlab-secret"
	settings.CodebergClientID = "codeberg-client"
	settings.CodebergSecret = "codeberg-secret"

	registry, err := buildOAuthRegistry(settings, &http.Client{Timeout: time.Second}, oauth.NewMemoryStateStore(nil))
	if err != nil {
		t.Fatalf("build OAuth registry: %v", err)
	}
	for _, providerID := range []string{"github", "gitlab", "codeberg"} {
		flow, ok := registry.Lookup(providerID)
		if !ok {
			t.Fatalf("missing %s OAuth flow", providerID)
		}
		result, err := flow.Begin(context.Background(), oauth.AuthorizationRequest{
			ConnectionID:   "connection-" + providerID,
			SubjectID:      "subject-1",
			SessionBinding: "test-session-token",
		})
		if err != nil {
			t.Fatalf("begin %s OAuth flow: %v", providerID, err)
		}
		authorizationURL, err := url.Parse(result.URL)
		if err != nil {
			t.Fatalf("parse %s authorization URL: %v", providerID, err)
		}
		want := "https://api.example.com/base/v1/integrations/" + providerID + "/callback"
		if got := authorizationURL.Query().Get("redirect_uri"); got != want {
			t.Errorf("%s redirect_uri = %q, want %q", providerID, got, want)
		}
		if authorizationURL.Query().Get("code_challenge_method") != "S256" {
			t.Errorf("%s OAuth flow does not require PKCE S256", providerID)
		}
	}
}

func TestBuildOAuthRegistryRejectsPartialCredentials(t *testing.T) {
	settings := developmentConfig(t)
	settings.GitHubClientID = "configured-without-secret"
	if _, err := buildOAuthRegistry(settings, &http.Client{Timeout: time.Second}, oauth.NewMemoryStateStore(nil)); err == nil {
		t.Fatal("partial OAuth credentials must fail startup")
	}
}

func TestBuildOAuthRegistryRequiresStateStore(t *testing.T) {
	if _, err := buildOAuthRegistry(developmentConfig(t), &http.Client{Timeout: time.Second}, nil); err == nil {
		t.Fatal("OAuth registry must not be constructed without a state store")
	}
}

type recordingRevocationFlow struct {
	providerID string
	token      []byte
	calls      int
}

func (flow *recordingRevocationFlow) ProviderID() string { return flow.providerID }
func (flow *recordingRevocationFlow) Begin(context.Context, oauth.AuthorizationRequest) (oauth.AuthorizationResult, error) {
	return oauth.AuthorizationResult{}, nil
}
func (flow *recordingRevocationFlow) Complete(context.Context, oauth.CallbackRequest) (oauth.CallbackResult, error) {
	return oauth.CallbackResult{}, nil
}
func (flow *recordingRevocationFlow) RevokeToken(_ context.Context, token []byte) error {
	flow.calls++
	flow.token = append([]byte(nil), token...)
	return nil
}

func TestOAuthRegistryRoutesTokenRevocationByExactProvider(t *testing.T) {
	github := &recordingRevocationFlow{providerID: "github"}
	gitlab := &recordingRevocationFlow{providerID: "gitlab"}
	registry, err := newOAuthRegistry(github, gitlab)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RevokeOAuthToken(context.Background(), "gitlab", []byte("gitlab-token")); err != nil {
		t.Fatal(err)
	}
	if github.calls != 0 || gitlab.calls != 1 || string(gitlab.token) != "gitlab-token" {
		t.Fatalf("revocation routing github=%d gitlab=%d token=%q", github.calls, gitlab.calls, gitlab.token)
	}
	if err := registry.RevokeOAuthToken(context.Background(), "codeberg", []byte("local-only")); err != nil {
		t.Fatal(err)
	}
	if github.calls != 0 || gitlab.calls != 1 {
		t.Fatal("unconfigured/local-only provider was routed to another OAuth flow")
	}
}

func TestBuildAuthServiceRejectsPartialDevelopmentSMTP(t *testing.T) {
	settings := developmentConfig(t)
	settings.SMTPUsername = "orphaned-credential"
	if _, err := buildAuthService(settings, auth.NewMemoryStore(), nil); err == nil {
		t.Fatal("partial SMTP configuration must not silently select the fake mailer")
	}
}

func TestProviderHTTPClientHasBoundedNoRedirectPolicy(t *testing.T) {
	client := newProviderHTTPClient()
	if client.Timeout != providerHTTPTimeout {
		t.Fatalf("provider timeout = %s, want %s", client.Timeout, providerHTTPTimeout)
	}
	request := httptest.NewRequest(http.MethodGet, "https://internal.invalid/", nil)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestAllowedRedirectsAreUnique(t *testing.T) {
	redirects := allowedRedirects(mustURL(t, "https://app.example/base"))
	if len(redirects) != 4 {
		t.Fatalf("redirects = %#v, want exactly four destinations", redirects)
	}
	seen := make(map[string]struct{}, len(redirects))
	for _, redirect := range redirects {
		if _, duplicate := seen[redirect]; duplicate {
			t.Fatalf("duplicate redirect %q in %#v", redirect, redirects)
		}
		seen[redirect] = struct{}{}
	}
	if _, ok := seen["https://app.example/base?auth=magic#auth"]; !ok {
		t.Fatalf("magic-link destination missing from %#v", redirects)
	}
}

func developmentConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Environment:       config.EnvironmentDevelopment,
		PublicURL:         mustURL(t, "http://localhost:8080"),
		WebURL:            mustURL(t, "http://localhost:5173"),
		SchedulerInterval: time.Minute,
	}
}

func productionConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Environment:                    config.EnvironmentProduction,
		PublicURL:                      mustURL(t, "https://api.example.com"),
		WebURL:                         mustURL(t, "https://example.com"),
		DatabaseURL:                    "postgresql://jandibat_api@database.example.com/jandibat",
		SessionSigningKey:              []byte("session-signing-key-material-that-is-long-enough"),
		CredentialActiveKeyID:          "current",
		CredentialEncryptionPublicKeys: map[string][]byte{"current": []byte("public-key-der-placeholder")},
		GitHubClientID:                 "github-client",
		GitHubClientSecret:             "github-secret",
		GitLabClientID:                 "gitlab-client",
		GitLabClientSecret:             "gitlab-secret",
		CodebergClientID:               "codeberg-client",
		CodebergSecret:                 "codeberg-secret",
		SchedulerInterval:              time.Minute,
	}
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL %q: %v", value, err)
	}
	return parsed
}

var _ integrations.OAuthTokenRevoker = (*oauthRegistry)(nil)
