// Package syncer adapts connection-oriented integration sync jobs to the
// forge HTTP clients in adapters/provider.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	provideradapter "github.com/moreal/jandibat.org/apps/api/internal/adapters/provider"
	activityapp "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const (
	defaultTimezone  = "UTC"
	defaultRangeDays = 365
	maxErrorDetail   = 512
)

var (
	ErrInvalidProvider        = errors.New("integration syncer: connection provider does not match syncer")
	ErrMissingSubject         = errors.New("integration syncer: connection subject is required")
	ErrMissingEnvironment     = errors.New("integration syncer: connection environment is required")
	ErrMissingCredentials     = errors.New("integration syncer: credential source is required")
	ErrMissingAccessToken     = errors.New("integration syncer: access token is required")
	ErrMissingExternalAccount = errors.New("integration syncer: external account login is required")
	ErrInvalidTimezone        = errors.New("integration syncer: invalid timezone")
	ErrInvalidRangeDays       = errors.New("integration syncer: range days must be positive")
	ErrProviderRateLimited    = errors.New("integration syncer: provider rate limit exceeded")
	ErrProviderRequest        = errors.New("integration syncer: provider request failed")
)

// Config controls the common behavior of a connection syncer. RangeDays is
// inclusive of today. A subsequent sync starts at LastSyncedAt when that is
// newer, retaining that day as an overlap so late-arriving events are seen.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Timezone   string
	RangeDays  int
	MaxPages   int
	Now        func() time.Time
}

// ProviderError is deliberately credential-free. Upstream response bodies are
// not retained because they can echo request credentials or private details.
type ProviderError struct {
	ProviderID string
	StatusCode int
	RateLimit  bool
	RetryAfter time.Duration
	detail     string
}

func (err *ProviderError) Error() string {
	if err == nil {
		return "<nil>"
	}
	base := fmt.Sprintf("integration syncer %s", err.ProviderID)
	if err.RateLimit {
		base += ": rate limit exceeded"
	} else {
		base += ": request failed"
	}
	if err.StatusCode > 0 {
		base += fmt.Sprintf(" (HTTP %d)", err.StatusCode)
	}
	if err.RetryAfter > 0 {
		base += fmt.Sprintf("; retry after %s", err.RetryAfter)
	}
	if err.detail != "" {
		base += ": " + err.detail
	}
	return base
}

func (err *ProviderError) Unwrap() error {
	if err != nil && err.RateLimit {
		return ErrProviderRateLimited
	}
	return ErrProviderRequest
}

type forgeProvider interface {
	Environment() activity.Environment
	Fetch(context.Context, activityapp.ProviderFetchInput) ([]activity.Fact, error)
}

type providerFactory func(client *http.Client, token string) (forgeProvider, error)

// Syncer implements integrations.ProviderSyncer for one built-in forge.
type Syncer struct {
	providerID string
	client     *http.Client
	timezone   string
	rangeDays  int
	now        func() time.Time
	newClient  providerFactory
}

var _ integrations.ProviderSyncer = (*Syncer)(nil)

func NewGitHub(config Config) (*Syncer, error) {
	return newSyncer(integrations.ProviderGitHub, config, func(client *http.Client, token string) (forgeProvider, error) {
		return provideradapter.NewGitHub(provideradapter.GitHubConfig{
			HTTPClient: client,
			BaseURL:    config.BaseURL,
			Token:      token,
			MaxPages:   config.MaxPages,
		})
	})
}

func NewGitLab(config Config) (*Syncer, error) {
	return newSyncer(integrations.ProviderGitLab, config, func(client *http.Client, token string) (forgeProvider, error) {
		return provideradapter.NewGitLab(provideradapter.GitLabConfig{
			HTTPClient: client,
			BaseURL:    config.BaseURL,
			Token:      token,
			MaxPages:   config.MaxPages,
		})
	})
}

func NewCodeberg(config Config) (*Syncer, error) {
	return newSyncer(integrations.ProviderCodeberg, config, func(client *http.Client, token string) (forgeProvider, error) {
		return provideradapter.NewCodeberg(provideradapter.CodebergConfig{
			HTTPClient: client,
			BaseURL:    config.BaseURL,
			Token:      token,
		})
	})
}

func newSyncer(providerID integrations.ProviderKind, config Config, factory providerFactory) (*Syncer, error) {
	timezone := strings.TrimSpace(config.Timezone)
	if timezone == "" {
		timezone = defaultTimezone
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTimezone, timezone)
	}
	rangeDays := config.RangeDays
	if rangeDays == 0 {
		rangeDays = defaultRangeDays
	}
	if rangeDays < 0 {
		return nil, ErrInvalidRangeDays
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	// Validate the base URL at construction time. The returned client contains
	// no credentials and is discarded.
	if _, err := factory(config.HTTPClient, ""); err != nil {
		return nil, err
	}
	return &Syncer{
		providerID: string(providerID),
		client:     config.HTTPClient,
		timezone:   timezone,
		rangeDays:  rangeDays,
		now:        now,
		newClient:  factory,
	}, nil
}

func (syncer *Syncer) ProviderID() string {
	if syncer == nil {
		return ""
	}
	return syncer.providerID
}

func (syncer *Syncer) Sync(ctx context.Context, request integrations.ProviderSyncRequest) (integrations.ProviderSyncResult, error) {
	if syncer == nil || request.Connection.ProviderID != syncer.providerID {
		return integrations.ProviderSyncResult{}, ErrInvalidProvider
	}
	connection := request.Connection
	if strings.TrimSpace(connection.SubjectID) == "" {
		return integrations.ProviderSyncResult{}, ErrMissingSubject
	}
	if strings.TrimSpace(connection.EnvironmentID) == "" {
		return integrations.ProviderSyncResult{}, ErrMissingEnvironment
	}
	timezone := strings.TrimSpace(request.Timezone)
	if timezone == "" {
		timezone = syncer.timezone
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return integrations.ProviderSyncResult{}, fmt.Errorf("%w: %q", ErrInvalidTimezone, timezone)
	}

	token, err := accessToken(ctx, request)
	if err != nil {
		return integrations.ProviderSyncResult{}, err
	}
	defer clearBytes(token)

	observer := &rateLimitObserver{next: transportFor(syncer.client)}
	client := cloneHTTPClient(syncer.client, observer)
	provider, err := syncer.newClient(client, string(token))
	if err != nil {
		return integrations.ProviderSyncResult{}, safeProviderError(syncer.providerID, observer.snapshot(), token, err)
	}

	from, to := syncer.dateRange(connection.LastSyncedAt, timezone)
	if request.From != nil {
		from = *request.From
	}
	if request.To != nil {
		to = *request.To
	}
	externalAccount := strings.TrimSpace(connection.ExternalAccountLogin)
	if externalAccount == "" {
		externalAccount = strings.TrimSpace(connection.ExternalAccountID)
	}
	if externalAccount == "" {
		return integrations.ProviderSyncResult{}, ErrMissingExternalAccount
	}
	facts, err := provider.Fetch(ctx, activityapp.ProviderFetchInput{
		Subject:         activity.SubjectID(connection.SubjectID),
		ProviderSubject: activity.SubjectID(externalAccount),
		Timezone:        timezone,
		From:            from,
		To:              to,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return integrations.ProviderSyncResult{}, err
		}
		return integrations.ProviderSyncResult{}, safeProviderError(syncer.providerID, observer.snapshot(), token, err)
	}

	environment := provider.Environment()
	environment.ID = activity.EnvironmentID(connection.EnvironmentID)
	if connection.AuthMethod == integrations.AuthNone {
		environment.Scope = activity.EnvironmentScopeGlobal
		environment.OwnerSubject = nil
	} else {
		owner := activity.SubjectID(connection.SubjectID)
		environment.Key = connection.EnvironmentID
		environment.Scope = activity.EnvironmentScopeSubject
		environment.OwnerSubject = &owner
		if environment.Metadata == nil {
			environment.Metadata = make(map[string]string)
		}
		environment.Metadata["visibility"] = "private"
		environment.Metadata["connection_id"] = connection.ID
	}
	facts = normalizeFacts(facts, activity.SubjectID(connection.SubjectID), environment.ID, from, to)
	return integrations.ProviderSyncResult{
		Environments: []activity.Environment{environment},
		Facts:        facts,
	}, nil
}

func (syncer *Syncer) dateRange(lastSyncedAt *time.Time, timezone string) (activity.Date, activity.Date) {
	location, _ := time.LoadLocation(timezone)
	now := syncer.now().In(location)
	toTime := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	fromTime := toTime.AddDate(0, 0, -(syncer.rangeDays - 1))
	if lastSyncedAt != nil {
		last := lastSyncedAt.In(location)
		lastDay := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, location)
		if lastDay.After(fromTime) && !lastDay.After(toTime) {
			fromTime = lastDay
		}
	}
	return activity.Date(fromTime.Format(time.DateOnly)), activity.Date(toTime.Format(time.DateOnly))
}

func accessToken(ctx context.Context, request integrations.ProviderSyncRequest) ([]byte, error) {
	if request.Connection.AuthMethod == integrations.AuthNone || !request.Connection.PrivateDataEnabled {
		return nil, nil
	}
	if request.Credentials == nil {
		return nil, ErrMissingCredentials
	}
	token, err := request.Credentials.AccessToken(ctx)
	if err != nil {
		// Credential source errors are intentionally not wrapped; an arbitrary
		// source could include plaintext in its error message.
		return nil, ErrMissingCredentials
	}
	if len(token) == 0 {
		return nil, ErrMissingAccessToken
	}
	return token, nil
}

func normalizeFacts(facts []activity.Fact, subject activity.SubjectID, environmentID activity.EnvironmentID, from, to activity.Date) []activity.Fact {
	result := make([]activity.Fact, 0, len(facts))
	for _, fact := range facts {
		if fact.Date < from || fact.Date > to || fact.Metric.Value < 0 {
			continue
		}
		fact.Subject = subject
		fact.EnvironmentID = environmentID
		fact.Metadata = cloneMetadata(fact.Metadata)
		result = append(result, fact)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Date != result[j].Date {
			return result[i].Date < result[j].Date
		}
		if result[i].Action != result[j].Action {
			return result[i].Action < result[j].Action
		}
		return metadataIdentity(result[i].Metadata) < metadataIdentity(result[j].Metadata)
	})
	return result
}

func cloneMetadata(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func metadataIdentity(metadata map[string]string) string {
	if value := metadata["provider_event_id"]; value != "" {
		return value
	}
	return metadata["source"]
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type rateLimitState struct {
	statusCode int
	limited    bool
	retryAfter time.Duration
}

type rateLimitObserver struct {
	next http.RoundTripper
	mu   sync.Mutex
	last rateLimitState
}

func (observer *rateLimitObserver) ProviderBaseTransport() http.RoundTripper {
	return observer.next
}

func (observer *rateLimitObserver) WrapProviderTransport(next http.RoundTripper) http.RoundTripper {
	observer.next = next
	return observer
}

func (observer *rateLimitObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	observer.mu.Lock()
	observer.last = rateLimitState{}
	observer.mu.Unlock()
	response, err := observer.next.RoundTrip(request)
	if response == nil {
		return response, err
	}
	state := rateLimitState{}
	remaining := strings.TrimSpace(response.Header.Get("RateLimit-Remaining"))
	if remaining == "" {
		remaining = strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining"))
	}
	state.limited = response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && remaining == "0")
	if state.limited {
		state.statusCode = response.StatusCode
		state.retryAfter = retryDelay(response.Header, time.Now())
	}
	observer.mu.Lock()
	observer.last = state
	observer.mu.Unlock()
	return response, err
}

func (observer *rateLimitObserver) snapshot() rateLimitState {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.last
}

func retryDelay(header http.Header, now time.Time) time.Duration {
	if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
			return retryAt.Sub(now).Round(time.Second)
		}
	}
	for _, key := range []string{"RateLimit-Reset", "X-RateLimit-Reset"} {
		if seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get(key)), 10, 64); err == nil && seconds > 0 {
			retryAt := time.Unix(seconds, 0)
			if retryAt.After(now) {
				return retryAt.Sub(now).Round(time.Second)
			}
		}
	}
	return 0
}

func transportFor(client *http.Client) http.RoundTripper {
	if client != nil && client.Transport != nil {
		return client.Transport
	}
	return http.DefaultTransport
}

func cloneHTTPClient(client *http.Client, transport http.RoundTripper) *http.Client {
	if client == nil {
		return &http.Client{Timeout: 10 * time.Second, Transport: transport}
	}
	clone := *client
	clone.Transport = transport
	return &clone
}

func safeProviderError(providerID string, state rateLimitState, token []byte, source error) error {
	statusCode := state.statusCode
	var httpError *provideradapter.HTTPError
	if errors.As(source, &httpError) {
		statusCode = httpError.StatusCode
	}
	limited := state.limited || statusCode == http.StatusTooManyRequests
	detail := ""
	if statusCode == 0 {
		detail = redactAndLimit(source.Error(), token)
	}
	return &ProviderError{
		ProviderID: providerID,
		StatusCode: statusCode,
		RateLimit:  limited,
		RetryAfter: state.retryAfter,
		detail:     detail,
	}
}

func redactAndLimit(message string, token []byte) string {
	if len(token) > 0 {
		message = strings.ReplaceAll(message, string(token), "[REDACTED]")
	}
	message = strings.TrimSpace(message)
	if len(message) > maxErrorDetail {
		message = message[:maxErrorDetail] + "..."
	}
	return message
}
