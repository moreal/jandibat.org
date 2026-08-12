package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	CodebergKey            = "codeberg"
	CodebergDefaultBaseURL = "https://codeberg.org/api/v1"
)

type CodebergConfig struct {
	HTTPClient *http.Client
	Network    HTTPNetwork
	BaseURL    string
	Token      string
}

type Codeberg struct {
	http httpProvider
}

func NewCodeberg(config CodebergConfig) (*Codeberg, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = CodebergDefaultBaseURL
	}
	httpAdapter, err := newHTTPProvider(config.HTTPClient, baseURL, CodebergDefaultBaseURL, config.Token, config.Network)
	if err != nil {
		return nil, err
	}
	return &Codeberg{http: httpAdapter}, nil
}

func (provider *Codeberg) Environment() domain.Environment {
	return domain.Environment{
		ID: CodebergKey, Key: CodebergKey, Name: "Codeberg", Scope: domain.EnvironmentScopeGlobal,
		Metadata: map[string]string{"category": "git-hosting", "forge": "forgejo"},
	}
}

type codebergHeatmapPoint struct {
	Timestamp     int64 `json:"timestamp"`
	Contributions int   `json:"contributions"`
}

func (provider *Codeberg) Fetch(ctx context.Context, input app.ProviderFetchInput) ([]domain.Fact, error) {
	location, from, to, err := resolveFetchInput(input)
	if err != nil {
		return nil, err
	}
	endpoint := provider.http.endpoint("users", string(providerSubject(input)), "heatmap")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "jandibat.org")
	if provider.http.token != "" {
		request.Header.Set("Authorization", "token "+provider.http.token)
	}

	var points []codebergHeatmapPoint
	if _, err := provider.http.doJSON(request, CodebergKey, &points); err != nil {
		if isAccountNotFound(err) {
			return []domain.Fact{}, nil
		}
		return nil, err
	}
	if len(points) > maxProviderRecords {
		return nil, fmt.Errorf("%w: codeberg returned too many heatmap records", ErrInvalidResponse)
	}
	facts := make([]domain.Fact, 0, len(points))
	for _, point := range points {
		if point.Timestamp <= 0 || point.Contributions < 0 || point.Contributions > maxMetricValue {
			return nil, fmt.Errorf("%w: codeberg heatmap record has invalid timestamp or contributions", ErrInvalidResponse)
		}
		if point.Contributions <= 0 {
			continue
		}
		date := dateAt(time.Unix(point.Timestamp, 0), location)
		if !factInRange(date, from, to) {
			continue
		}
		facts = append(facts, newFact(input, CodebergKey, date, domain.ActionCommit, point.Contributions, map[string]string{
			"source": "heatmap",
		}))
	}
	return facts, nil
}
