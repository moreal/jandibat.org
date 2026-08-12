package syncer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	activityapp "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const testToken = "token-that-must-not-leak"

type staticCredentials struct {
	token []byte
	err   error
	calls int
}

type capturingProvider struct {
	input activityapp.ProviderFetchInput
}

func (*capturingProvider) Environment() activity.Environment {
	owner := activity.SubjectID("subject-1")
	return activity.Environment{ID: "ignored", Key: "github", Name: "GitHub", Scope: activity.EnvironmentScopeSubject, OwnerSubject: &owner}
}

func (provider *capturingProvider) Fetch(_ context.Context, input activityapp.ProviderFetchInput) ([]activity.Fact, error) {
	provider.input = input
	return nil, nil
}

func (source *staticCredentials) AccessToken(context.Context) ([]byte, error) {
	source.calls++
	return append([]byte(nil), source.token...), source.err
}

func (*staticCredentials) RefreshToken(context.Context) ([]byte, error) { return nil, nil }

func testConnection(providerID string) integrations.ProviderConnection {
	return integrations.ProviderConnection{
		ID:                 "connection-1",
		SubjectID:          "subject-1",
		ProviderID:         providerID,
		EnvironmentID:      "environment-1",
		ExternalAccountID:  "external-user",
		AuthMethod:         integrations.AuthToken,
		Status:             integrations.ConnectionActive,
		PrivateDataEnabled: true,
	}
}

func testConfig(server *httptest.Server) Config {
	return Config{
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
		Timezone:   "Asia/Seoul",
		RangeDays:  3,
		Now: func() time.Time {
			return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
		},
	}
}

func TestSyncUsesExplicitDateRange(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	syncer := &Syncer{
		providerID: "github", timezone: "UTC", rangeDays: 3,
		now:       func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) },
		newClient: func(*http.Client, string) (forgeProvider, error) { return provider, nil },
	}
	from, to := activity.Date("2026-08-01"), activity.Date("2026-08-05")
	_, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: testConnection("github"), Credentials: &staticCredentials{token: []byte(testToken)},
		From: &from, To: &to, Force: true, FailurePolicy: activity.FetchFailurePurge,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.input.From != from || provider.input.To != to {
		t.Fatalf("fetch range = %s..%s, want %s..%s", provider.input.From, provider.input.To, from, to)
	}
}

func TestPublicOnlyCredentialConnectionNeverLoadsOrSendsToken(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	var clientToken string
	syncer := &Syncer{
		providerID: "gitlab", timezone: "UTC", rangeDays: 1,
		now: func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) },
		newClient: func(_ *http.Client, token string) (forgeProvider, error) {
			clientToken = token
			return provider, nil
		},
	}
	connection := testConnection("gitlab")
	connection.PrivateDataEnabled = false
	credentials := &staticCredentials{token: []byte(testToken)}
	if _, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: connection, Credentials: credentials,
	}); err != nil {
		t.Fatal(err)
	}
	if credentials.calls != 0 || clientToken != "" {
		t.Fatalf("public-only fetch loaded/sent credential: calls=%d token=%q", credentials.calls, clientToken)
	}
}

func TestPublicOnlyCredentialConnectionsUseAnonymousProviderContracts(t *testing.T) {
	t.Parallel()
	timestamp := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		providerID string
		path       string
		body       string
		wantValue  int
		newSyncer  func(Config) (*Syncer, error)
	}{
		{
			providerID: "github", path: "/users/external-user/events/public", wantValue: 2,
			body:      `[{"id":"event-1","type":"PushEvent","created_at":"2026-08-12T00:00:00Z","repo":{"name":"org/repo"},"payload":{"size":2}}]`,
			newSyncer: NewGitHub,
		},
		{
			providerID: "gitlab", path: "/users/external-user/events", wantValue: 3,
			body:      `[{"id":1,"project_id":7,"action_name":"pushed to","created_at":"2026-08-12T00:00:00Z","push_data":{"commit_count":3}}]`,
			newSyncer: NewGitLab,
		},
		{
			providerID: "codeberg", path: "/users/external-user/heatmap", wantValue: 4,
			body:      fmt.Sprintf(`[{"timestamp":%d,"contributions":4}]`, timestamp.Unix()),
			newSyncer: NewCodeberg,
		},
	}
	for _, test := range tests {
		t.Run(test.providerID, func(t *testing.T) {
			credentials := &staticCredentials{token: []byte(testToken)}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.path {
					t.Errorf("path = %q, want %q", request.URL.Path, test.path)
				}
				if authorization := request.Header.Get("Authorization"); authorization != "" {
					t.Errorf("unexpected Authorization header %q", authorization)
				}
				if privateToken := request.Header.Get("PRIVATE-TOKEN"); privateToken != "" {
					t.Errorf("unexpected PRIVATE-TOKEN header %q", privateToken)
				}
				writer.Header().Set("Content-Type", "application/json")
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()

			syncer, err := test.newSyncer(testConfig(server))
			if err != nil {
				t.Fatal(err)
			}
			connection := testConnection(test.providerID)
			connection.PrivateDataEnabled = false
			result, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
				Connection: connection, Credentials: credentials,
			})
			if err != nil {
				t.Fatal(err)
			}
			if credentials.calls != 0 {
				t.Fatalf("credential source calls = %d, want 0", credentials.calls)
			}
			if len(result.Facts) != 1 || result.Facts[0].Metric.Value != test.wantValue ||
				result.Facts[0].Subject != "subject-1" || result.Facts[0].EnvironmentID != "environment-1" {
				t.Fatalf("public fixture facts = %#v", result.Facts)
			}
		})
	}
}

func TestSyncUsesProviderLoginRatherThanLocalOrImmutableAccountID(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	syncer := &Syncer{
		providerID: "github", timezone: "UTC", rangeDays: 1,
		now:       func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) },
		newClient: func(*http.Client, string) (forgeProvider, error) { return provider, nil },
	}
	connection := testConnection("github")
	connection.SubjectID = "sub_018f0000"
	connection.ExternalAccountID = "1234567"
	connection.ExternalAccountLogin = "octocat"
	if _, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: connection, Credentials: &staticCredentials{token: []byte(testToken)},
	}); err != nil {
		t.Fatal(err)
	}
	if provider.input.Subject != "sub_018f0000" || provider.input.ProviderSubject != "octocat" {
		t.Fatalf("provider fetch identities = %#v", provider.input)
	}
}

func TestSyncUsesSubjectTimezoneForDefaultRange(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	syncer := &Syncer{
		providerID: "github", timezone: "UTC", rangeDays: 1,
		now:       func() time.Time { return time.Date(2026, 8, 11, 23, 30, 0, 0, time.UTC) },
		newClient: func(*http.Client, string) (forgeProvider, error) { return provider, nil },
	}
	_, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: testConnection("github"), Credentials: &staticCredentials{token: []byte(testToken)},
		Timezone: "Pacific/Kiritimati",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.input.Timezone != "Pacific/Kiritimati" || provider.input.From != "2026-08-12" || provider.input.To != "2026-08-12" {
		t.Fatalf("subject timezone fetch input = %#v", provider.input)
	}

	_, err = syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: testConnection("github"), Credentials: &staticCredentials{token: []byte(testToken)},
		Timezone: "Mars/Olympus",
	})
	if !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("invalid request timezone error = %v", err)
	}
}

func TestGitHubSyncPaginatesAndNormalizesConnection(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	pages := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/users/external-user/events/public" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("authorization = %q", got)
		}
		page := request.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if page == "1" {
			writer.Header().Set("Link", fmt.Sprintf("<%s/users/external-user/events/public?page=2>; rel=\"next\"", request.Host))
			writer.Write([]byte(githubEvents(100, "2026-08-11T10:00:00Z")))
			return
		}
		writer.Write([]byte(githubEvents(1, "2026-08-12T10:00:00Z")))
	}))
	defer server.Close()

	syncer, err := NewGitHub(testConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	credentials := &staticCredentials{token: []byte(testToken)}
	result, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: testConnection("github"), Credentials: credentials,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pages, []string{"1", "2"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
	if len(result.Environments) != 1 || result.Environments[0].ID != "environment-1" || result.Environments[0].Key != "environment-1" {
		t.Fatalf("environments = %#v", result.Environments)
	}
	if result.Environments[0].Scope != activity.EnvironmentScopeSubject ||
		result.Environments[0].OwnerSubject == nil || *result.Environments[0].OwnerSubject != "subject-1" {
		t.Fatalf("credential-backed environment is not subject-scoped: %#v", result.Environments[0])
	}
	if result.Environments[0].Metadata["visibility"] != "private" || result.Environments[0].Metadata["connection_id"] != "connection-1" {
		t.Fatalf("credential-backed environment lacks privacy provenance: %#v", result.Environments[0].Metadata)
	}
	if len(result.Facts) != 101 {
		t.Fatalf("facts len = %d, want 101", len(result.Facts))
	}
	for _, fact := range result.Facts {
		if fact.Subject != "subject-1" || fact.EnvironmentID != "environment-1" {
			t.Fatalf("fact was not normalized: %#v", fact)
		}
	}
	if credentials.calls != 1 {
		t.Fatalf("credential calls = %d, want 1", credentials.calls)
	}
}

func githubEvents(count int, timestamp string) string {
	var body strings.Builder
	body.WriteByte('[')
	for index := 0; index < count; index++ {
		if index > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"id":"event-%d","type":"PushEvent","created_at":%q,"repo":{"name":"owner/repo"},"payload":{"size":2}}`, index, timestamp)
	}
	body.WriteByte(']')
	return body.String()
}

func TestGitLabSyncUsesLastSyncOverlapAndNextPage(t *testing.T) {
	t.Parallel()
	var requested []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/users/external-user/events" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("PRIVATE-TOKEN"); got != testToken {
			t.Errorf("private token = %q", got)
		}
		query := request.URL.Query()
		// LastSyncedAt falls on Aug 12 in the configured timezone. The
		// syncer retains that day as the overlap, and the GitLab adapter expands
		// its exclusive date query by one day on either side.
		if query.Get("after") != "2026-08-11" || query.Get("before") != "2026-08-13" {
			t.Errorf("date query = %q", request.URL.RawQuery)
		}
		page := query.Get("page")
		mu.Lock()
		requested = append(requested, page)
		mu.Unlock()
		if page == "1" {
			writer.Header().Set("X-Next-Page", "2")
		}
		fmt.Fprintf(writer, `[{"id":%s,"project_id":7,"action_name":"pushed to","created_at":"2026-08-12T01:00:00Z","push_data":{"commit_count":3}}]`, page)
	}))
	defer server.Close()

	config := testConfig(server)
	syncer, err := NewGitLab(config)
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection("gitlab")
	lastSync := time.Date(2026, 8, 11, 23, 30, 0, 0, time.UTC) // 2026-08-12 in Asia/Seoul.
	connection.LastSyncedAt = &lastSync
	result, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: connection, Credentials: &staticCredentials{token: []byte(testToken)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(requested), "[1 2]"; got != want {
		t.Fatalf("pages = %s, want %s", got, want)
	}
	if len(result.Facts) != 2 || result.Facts[0].Action != activity.ActionCommit || result.Facts[0].Metric.Value != 3 {
		t.Fatalf("facts = %#v", result.Facts)
	}
}

func TestCodebergSyncUsesAnonymousConnectionWithoutCredentials(t *testing.T) {
	t.Parallel()
	timestamp := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Errorf("unexpected authorization header %q", authorization)
		}
		fmt.Fprintf(writer, `[{"timestamp":%d,"contributions":4}]`, timestamp)
	}))
	defer server.Close()

	syncer, err := NewCodeberg(testConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection("codeberg")
	connection.AuthMethod = integrations.AuthNone
	result, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{Connection: connection})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Date != "2026-08-12" || result.Facts[0].Metric.Value != 4 {
		t.Fatalf("facts = %#v", result.Facts)
	}
	if len(result.Environments) != 1 || result.Environments[0].Scope != activity.EnvironmentScopeGlobal || result.Environments[0].OwnerSubject != nil {
		t.Fatalf("anonymous environment is not global: %#v", result.Environments)
	}
}

func TestSyncReportsRateLimitWithoutExposingSecret(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "17")
		writer.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(writer, `{"message":"token %s rejected"}`, testToken)
	}))
	defer server.Close()

	syncer, err := NewGitLab(testConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	_, err = syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
		Connection: testConnection("gitlab"), Credentials: &staticCredentials{token: []byte(testToken)},
	})
	if !errors.Is(err, ErrProviderRateLimited) {
		t.Fatalf("error = %v, want rate limit", err)
	}
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.StatusCode != http.StatusTooManyRequests || providerError.RetryAfter != 17*time.Second {
		t.Fatalf("provider error = %#v", providerError)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(fmt.Sprintf("%#v", err), testToken) {
		t.Fatalf("error exposed token: %v", err)
	}
}

func TestSyncRedactsUpstreamErrorsAndCredentialSourceErrors(t *testing.T) {
	t.Parallel()
	t.Run("upstream body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(writer, testToken)
		}))
		defer server.Close()
		syncer, err := NewGitHub(testConfig(server))
		if err != nil {
			t.Fatal(err)
		}
		_, err = syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
			Connection: testConnection("github"), Credentials: &staticCredentials{token: []byte(testToken)},
		})
		if !errors.Is(err, ErrProviderRequest) || strings.Contains(err.Error(), testToken) {
			t.Fatalf("unsafe error = %v", err)
		}
	})

	t.Run("credential source", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		syncer, err := NewCodeberg(testConfig(server))
		if err != nil {
			t.Fatal(err)
		}
		credentials := &staticCredentials{err: errors.New("failed with " + testToken)}
		_, err = syncer.Sync(context.Background(), integrations.ProviderSyncRequest{
			Connection: testConnection("codeberg"), Credentials: credentials,
		})
		if !errors.Is(err, ErrMissingCredentials) || strings.Contains(err.Error(), testToken) {
			t.Fatalf("unsafe credential error = %v", err)
		}
	})
}

func TestSyncValidation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	syncer, err := NewGitHub(testConfig(server))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		connection  integrations.ProviderConnection
		credentials integrations.CredentialSource
		want        error
	}{
		{name: "provider", connection: testConnection("gitlab"), credentials: &staticCredentials{token: []byte(testToken)}, want: ErrInvalidProvider},
		{name: "subject", connection: func() integrations.ProviderConnection { c := testConnection("github"); c.SubjectID = ""; return c }(), credentials: &staticCredentials{token: []byte(testToken)}, want: ErrMissingSubject},
		{name: "environment", connection: func() integrations.ProviderConnection { c := testConnection("github"); c.EnvironmentID = ""; return c }(), credentials: &staticCredentials{token: []byte(testToken)}, want: ErrMissingEnvironment},
		{name: "credentials", connection: testConnection("github"), want: ErrMissingCredentials},
	}
	for index, test := range tests {
		t.Run(strconv.Itoa(index)+"_"+test.name, func(t *testing.T) {
			_, err := syncer.Sync(context.Background(), integrations.ProviderSyncRequest{Connection: test.connection, Credentials: test.credentials})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
