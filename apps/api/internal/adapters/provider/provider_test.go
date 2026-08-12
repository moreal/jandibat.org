package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func fetchInput() app.ProviderFetchInput {
	return app.ProviderFetchInput{Subject: "moreal", Timezone: "Asia/Seoul", From: "2026-03-02", To: "2026-03-03"}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestGitHubFixtureContract(t *testing.T) {
	payload := fixture(t, "github-events.json")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/users/moreal/events/public" ||
			request.URL.Query().Get("page") != "1" || request.URL.Query().Get("per_page") != "100" ||
			request.Header.Get("Authorization") != "Bearer secret" ||
			request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("unexpected request: %s %#v", request.URL.String(), request.Header)
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	client, err := NewGitHub(GitHubConfig{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.Fetch(context.Background(), fetchInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 || facts[0].Date != "2026-03-03" || facts[0].Metric.Value != 3 ||
		facts[1].Action != domain.ActionIssue || facts[2].Action != domain.ActionPr {
		t.Fatalf("unexpected facts: %+v", facts)
	}
}

func TestGitLabFixtureContract(t *testing.T) {
	payload := fixture(t, "gitlab-events.json")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/users/moreal/events" ||
			request.Header.Get("PRIVATE-TOKEN") != "secret" || request.URL.Query().Get("after") != "2026-03-01" ||
			request.URL.Query().Get("before") != "2026-03-04" || request.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request: %s", request.URL.String())
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	client, err := NewGitLab(GitLabConfig{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.Fetch(context.Background(), fetchInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 || facts[0].Action != domain.ActionCommit || facts[0].Metric.Value != 2 ||
		facts[1].Action != domain.ActionIssue || facts[2].Action != domain.ActionPr {
		t.Fatalf("unexpected facts: %+v", facts)
	}
}

func TestCodebergFixtureContract(t *testing.T) {
	payload := fixture(t, "codeberg-heatmap.json")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/users/moreal/heatmap" ||
			request.Header.Get("Authorization") != "token secret" {
			t.Errorf("unexpected request: %s", request.URL.String())
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	client, err := NewCodeberg(CodebergConfig{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.Fetch(context.Background(), fetchInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Date != "2026-03-03" || facts[0].Metric.Value != 5 {
		t.Fatalf("unexpected facts: %+v", facts)
	}
}

func TestGitHubFollowsOnlyHeaderPagination(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("page")
		requests.Add(1)
		if page == "1" {
			events := make([]githubEvent, 100)
			for index := range events {
				events[index] = githubEvent{ID: strconv.Itoa(index + 1), Type: "PushEvent", CreatedAt: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), Payload: json.RawMessage(`{"size":1}`)}
			}
			writer.Header().Set("Link", `</users/moreal/events/public?page=2>; rel="next"`)
			_ = json.NewEncoder(writer).Encode(events)
			return
		}
		fmt.Fprint(writer, `[{"id":"101","type":"IssuesEvent","created_at":"2026-03-02T12:00:00Z","payload":{"action":"opened"}}]`)
	}))
	defer server.Close()
	client, err := NewGitHub(GitHubConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.Fetch(context.Background(), fetchInput())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || len(facts) != 101 {
		t.Fatalf("requests=%d facts=%d", requests.Load(), len(facts))
	}
}

func TestGitLabUsesValidatedNextPage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("page")
		requests.Add(1)
		if page == "1" {
			writer.Header().Set("X-Next-Page", "2")
		}
		fmt.Fprintf(writer, `[{"id":%s,"project_id":7,"action_name":"opened","target_type":"Issue","created_at":"2026-03-02T10:00:00Z","push_data":{"commit_count":0}}]`, page)
	}))
	defer server.Close()
	client, err := NewGitLab(GitLabConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.Fetch(context.Background(), fetchInput())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || len(facts) != 2 {
		t.Fatalf("requests=%d facts=%d", requests.Load(), len(facts))
	}
}

func TestProviderRateLimitMappingDoesNotExposeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-RateLimit-Remaining", "0")
		writer.Header().Set("Retry-After", "12")
		writer.WriteHeader(http.StatusForbidden)
		fmt.Fprint(writer, `secret-token and private repository details`)
	}))
	defer server.Close()
	client, err := NewGitHub(GitHubConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), fetchInput())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v", err)
	}
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusForbidden || !httpError.RateLimited ||
		httpError.RetryAfter != 12*time.Second || httpError.Body != "" || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe or incorrect HTTP error: %#v / %v", httpError, err)
	}
}

func TestProviderHTTPErrorMappingDoesNotExposeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(writer, `upstream-private-detail`)
	}))
	defer server.Close()
	client, err := NewGitLab(GitLabConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), fetchInput())
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusBadGateway || httpError.RateLimited ||
		httpError.Body != "" || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe or incorrect HTTP error: %#v / %v", httpError, err)
	}
}

func TestProviderAccountNotFoundIsAnEmptySnapshot(t *testing.T) {
	payload := fixture(t, "account-not-found.json")
	tests := []struct {
		name string
		new  func(*http.Client, string) (app.Provider, error)
	}{
		{name: "github", new: func(client *http.Client, baseURL string) (app.Provider, error) {
			return NewGitHub(GitHubConfig{HTTPClient: client, BaseURL: baseURL})
		}},
		{name: "gitlab", new: func(client *http.Client, baseURL string) (app.Provider, error) {
			return NewGitLab(GitLabConfig{HTTPClient: client, BaseURL: baseURL})
		}},
		{name: "codeberg", new: func(client *http.Client, baseURL string) (app.Provider, error) {
			return NewCodeberg(CodebergConfig{HTTPClient: client, BaseURL: baseURL})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write(payload)
			}))
			defer server.Close()
			provider, err := test.new(server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := provider.Fetch(context.Background(), fetchInput())
			if err != nil || facts == nil || len(facts) != 0 {
				t.Fatalf("facts=%#v error=%v", facts, err)
			}
		})
	}
}

func TestPaginatedProviderDoesNotTreatLaterPageNotFoundAsMissingAccount(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("page") == "2" {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			events := make([]githubEvent, 100)
			for index := range events {
				events[index] = githubEvent{ID: strconv.Itoa(index + 1), Type: "PushEvent", CreatedAt: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), Payload: json.RawMessage(`{"size":1}`)}
			}
			writer.Header().Set("Link", `</users/moreal/events/public?page=2>; rel="next"`)
			_ = json.NewEncoder(writer).Encode(events)
		}))
		defer server.Close()
		provider, err := NewGitHub(GitHubConfig{HTTPClient: server.Client(), BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Fetch(context.Background(), fetchInput())
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusNotFound {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("gitlab", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("page") == "2" {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("X-Next-Page", "2")
			fmt.Fprint(writer, `[{"id":1,"project_id":7,"action_name":"opened","target_type":"Issue","created_at":"2026-03-02T10:00:00Z","push_data":{"commit_count":0}}]`)
		}))
		defer server.Close()
		provider, err := NewGitLab(GitLabConfig{HTTPClient: server.Client(), BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Fetch(context.Background(), fetchInput())
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusNotFound {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestProviderRejectsRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer server.Close()
	client, err := NewCodeberg(CodebergConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), fetchInput())
	if !errors.Is(err, ErrRedirectNotAllowed) || redirected.Load() {
		t.Fatalf("error=%v redirected=%v", err, redirected.Load())
	}
}

func TestProviderBoundsSuccessfulResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat(" ", maxResponseBody+1)))
	}))
	defer server.Close()
	client, err := NewGitHub(GitHubConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), fetchInput())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderBaseURLAllowlist(t *testing.T) {
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://127.0.0.1:8080",
		"https://example.com/api",
		"https://api.github.com?redirect=https://example.com",
		"https://user:password@api.github.com",
	} {
		if _, err := NewGitHub(GitHubConfig{BaseURL: raw}); !errors.Is(err, ErrInvalidBaseURL) {
			t.Errorf("base URL %q error = %v", raw, err)
		}
	}
	client, err := NewGitHub(GitHubConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := client.http.endpoint("users", "octocat", "events", "public").String(); endpoint != "https://api.github.com/users/octocat/events/public" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}

func TestProviderClonesClientAndCapsTimeout(t *testing.T) {
	original := &http.Client{Timeout: time.Minute}
	client, err := NewGitLab(GitLabConfig{HTTPClient: original})
	if err != nil {
		t.Fatal(err)
	}
	if original.Timeout != time.Minute || client.http.client == original || client.http.client.Timeout != defaultHTTPTimeout {
		t.Fatalf("original=%p/%s provider=%p/%s", original, original.Timeout, client.http.client, client.http.client.Timeout)
	}
}

func TestProviderRejectsUnsafeSubjectBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(writer, `[]`)
	}))
	defer server.Close()
	client, err := NewGitHub(GitHubConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	input := fetchInput()
	input.Subject = "../../metadata"
	_, err = client.Fetch(context.Background(), input)
	if !errors.Is(err, ErrInvalidSubject) || requests.Load() != 0 {
		t.Fatalf("error=%v requests=%d", err, requests.Load())
	}
}

func TestProviderRejectsOutOfContractPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(writer, `[{"timestamp":%d,"contributions":%d}]`, time.Now().Unix(), maxMetricValue+1)
	}))
	defer server.Close()
	client, err := NewCodeberg(CodebergConfig{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), fetchInput())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v", err)
	}
}
