package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	GitHubKey            = "github"
	GitHubDefaultBaseURL = "https://api.github.com"
	defaultMaxPages      = 10
)

type GitHubConfig struct {
	HTTPClient *http.Client
	Network    HTTPNetwork
	BaseURL    string
	Token      string
	MaxPages   int
}

type GitHub struct {
	http     httpProvider
	maxPages int
}

func NewGitHub(config GitHubConfig) (*GitHub, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = GitHubDefaultBaseURL
	}
	httpAdapter, err := newHTTPProvider(config.HTTPClient, baseURL, GitHubDefaultBaseURL, config.Token, config.Network)
	if err != nil {
		return nil, err
	}
	maxPages := config.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	if maxPages > defaultMaxPages {
		maxPages = defaultMaxPages
	}
	return &GitHub{http: httpAdapter, maxPages: maxPages}, nil
}

func (provider *GitHub) Environment() domain.Environment {
	return domain.Environment{
		ID: GitHubKey, Key: GitHubKey, Name: "GitHub", Scope: domain.EnvironmentScopeGlobal,
		Metadata: map[string]string{"category": "git-hosting"},
	}
}

type githubEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Repo      githubRepo      `json:"repo"`
	Payload   json.RawMessage `json:"payload"`
}

type githubRepo struct {
	Name string `json:"name"`
}

type githubPayload struct {
	Action string `json:"action"`
	Size   int    `json:"size"`
}

func (provider *GitHub) Fetch(ctx context.Context, input app.ProviderFetchInput) ([]domain.Fact, error) {
	location, from, to, err := resolveFetchInput(input)
	if err != nil {
		return nil, err
	}

	facts := make([]domain.Fact, 0)
	for page := 1; page <= provider.maxPages; page++ {
		endpoint := provider.http.endpoint("users", string(providerSubject(input)), "events", "public")
		query := endpoint.Query()
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		request.Header.Set("User-Agent", "jandibat.org")
		if provider.http.token != "" {
			request.Header.Set("Authorization", "Bearer "+provider.http.token)
		}

		var events []githubEvent
		response, err := provider.http.doJSON(request, GitHubKey, &events)
		if err != nil {
			if page == 1 && isAccountNotFound(err) {
				return facts, nil
			}
			return nil, err
		}
		if len(events) > 100 {
			return nil, fmt.Errorf("%w: github returned more than per_page records", ErrInvalidResponse)
		}
		oldestIsBeforeRange := false
		for _, event := range events {
			if event.ID == "" || event.CreatedAt.IsZero() {
				return nil, fmt.Errorf("%w: github event is missing id or created_at", ErrInvalidResponse)
			}
			date := dateAt(event.CreatedAt, location)
			if date < domain.Date(from.Format(time.DateOnly)) {
				oldestIsBeforeRange = true
				continue
			}
			if !factInRange(date, from, to) {
				continue
			}
			fact, ok, err := githubEventFact(input, date, event)
			if err != nil {
				return nil, err
			}
			if ok {
				facts = append(facts, fact)
			}
		}
		if len(events) < 100 || oldestIsBeforeRange || !hasNextLink(response.Header.Get("Link")) {
			break
		}
	}
	return facts, nil
}

func githubEventFact(input app.ProviderFetchInput, date domain.Date, event githubEvent) (domain.Fact, bool, error) {
	if event.ID == "" || event.CreatedAt.IsZero() {
		return domain.Fact{}, false, fmt.Errorf("%w: github event is missing id or created_at", ErrInvalidResponse)
	}
	payload := githubPayload{}
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return domain.Fact{}, false, fmt.Errorf("%w: github event %q payload: %v", ErrInvalidResponse, event.ID, err)
		}
	}
	action := domain.ActionCustom
	value := 1
	switch event.Type {
	case "PushEvent":
		action = domain.ActionCommit
		value = payload.Size
		if value < 0 || value > maxMetricValue {
			return domain.Fact{}, false, fmt.Errorf("%w: github event %q has invalid size", ErrInvalidResponse, event.ID)
		}
		if value <= 0 {
			value = 1
		}
	case "IssuesEvent":
		action = domain.ActionIssue
	case "PullRequestEvent":
		action = domain.ActionPr
	default:
		return domain.Fact{}, false, nil
	}
	metadata := map[string]string{
		"provider_event_id": event.ID,
		"repository":        event.Repo.Name,
	}
	if payload.Action != "" {
		metadata["provider_action"] = payload.Action
	}
	return newFact(input, GitHubKey, date, action, value, metadata), true, nil
}

func hasNextLink(link string) bool {
	for _, item := range parseLinkHeader(link) {
		if item == "next" {
			return true
		}
	}
	return false
}

func parseLinkHeader(header string) []string {
	relations := make([]string, 0)
	for _, part := range splitComma(header) {
		for _, parameter := range splitSemicolon(part) {
			parameter = trimSpace(parameter)
			if len(parameter) > 6 && parameter[:4] == "rel=" {
				relations = append(relations, trimQuotes(parameter[4:]))
			}
		}
	}
	return relations
}

func splitComma(value string) []string     { return split(value, ',') }
func splitSemicolon(value string) []string { return split(value, ';') }
func split(value string, separator rune) []string {
	parts := make([]string, 0)
	start := 0
	for index, current := range value {
		if current == separator {
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	return append(parts, value[start:])
}
func trimSpace(value string) string  { return stringTrimSpace(value) }
func trimQuotes(value string) string { return stringTrim(value, `"`) }
