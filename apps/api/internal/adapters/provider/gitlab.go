package provider

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	GitLabKey            = "gitlab"
	GitLabDefaultBaseURL = "https://gitlab.com/api/v4"
)

type GitLabConfig struct {
	HTTPClient *http.Client
	Network    HTTPNetwork
	BaseURL    string
	Token      string
	MaxPages   int
}

type GitLab struct {
	http     httpProvider
	maxPages int
}

func NewGitLab(config GitLabConfig) (*GitLab, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = GitLabDefaultBaseURL
	}
	httpAdapter, err := newHTTPProvider(config.HTTPClient, baseURL, GitLabDefaultBaseURL, config.Token, config.Network)
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
	return &GitLab{http: httpAdapter, maxPages: maxPages}, nil
}

func (provider *GitLab) Environment() domain.Environment {
	return domain.Environment{
		ID: GitLabKey, Key: GitLabKey, Name: "GitLab", Scope: domain.EnvironmentScopeGlobal,
		Metadata: map[string]string{"category": "git-hosting"},
	}
}

type gitLabEvent struct {
	ID         int64          `json:"id"`
	ProjectID  int64          `json:"project_id"`
	ActionName string         `json:"action_name"`
	TargetType string         `json:"target_type"`
	CreatedAt  time.Time      `json:"created_at"`
	PushData   gitLabPushData `json:"push_data"`
}

type gitLabPushData struct {
	CommitCount int `json:"commit_count"`
}

func (provider *GitLab) Fetch(ctx context.Context, input app.ProviderFetchInput) ([]domain.Fact, error) {
	location, from, to, err := resolveFetchInput(input)
	if err != nil {
		return nil, err
	}

	facts := make([]domain.Fact, 0)
	page := 1
	for requests := 0; requests < provider.maxPages; requests++ {
		endpoint := provider.http.endpoint("users", string(providerSubject(input)), "events")
		query := endpoint.Query()
		query.Set("after", from.AddDate(0, 0, -1).Format(time.DateOnly))
		query.Set("before", to.AddDate(0, 0, 1).Format(time.DateOnly))
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "jandibat.org")
		if provider.http.token != "" {
			request.Header.Set("PRIVATE-TOKEN", provider.http.token)
		}

		var events []gitLabEvent
		response, err := provider.http.doJSON(request, GitLabKey, &events)
		if err != nil {
			if requests == 0 && isAccountNotFound(err) {
				return facts, nil
			}
			return nil, err
		}
		if len(events) > 100 {
			return nil, fmt.Errorf("%w: gitlab returned more than per_page records", ErrInvalidResponse)
		}
		for _, event := range events {
			if event.ID <= 0 || event.CreatedAt.IsZero() || event.PushData.CommitCount < 0 || event.PushData.CommitCount > maxMetricValue {
				return nil, fmt.Errorf("%w: gitlab event has invalid id, created_at, or commit_count", ErrInvalidResponse)
			}
			date := dateAt(event.CreatedAt, location)
			if !factInRange(date, from, to) {
				continue
			}
			facts = append(facts, gitLabEventFact(input, date, event))
		}
		next := strings.TrimSpace(response.Header.Get("X-Next-Page"))
		if next == "" {
			break
		}
		nextPage, err := strconv.Atoi(next)
		if err != nil || nextPage <= page {
			return nil, ErrInvalidResponse
		}
		page = nextPage
	}
	return facts, nil
}

func gitLabEventFact(input app.ProviderFetchInput, date domain.Date, event gitLabEvent) domain.Fact {
	action := domain.ActionCustom
	value := 1
	if event.PushData.CommitCount > 0 || strings.Contains(strings.ToLower(event.ActionName), "push") {
		action = domain.ActionCommit
		value = event.PushData.CommitCount
		if value <= 0 {
			value = 1
		}
	} else {
		switch strings.ToLower(event.TargetType) {
		case "issue":
			action = domain.ActionIssue
		case "mergerequest", "merge_request":
			action = domain.ActionPr
		}
	}
	return newFact(input, GitLabKey, date, action, value, map[string]string{
		"provider_event_id": stringInt(event.ID),
		"project_id":        stringInt(event.ProjectID),
		"provider_action":   event.ActionName,
	})
}
