// Package provider contains HTTP adapters that normalize forge activity into
// the activity application's provider contract.
package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fixedhttp"
	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	defaultHTTPTimeout = 10 * time.Second
	maxResponseBody    = 2 << 20
	maxProviderRecords = 10_000
	maxMetricValue     = 1_000_000
	maxSubjectLength   = 255
)

var (
	ErrInvalidBaseURL     = errors.New("activity provider: invalid base URL")
	ErrInvalidSubject     = errors.New("activity provider: invalid subject")
	ErrInvalidTimezone    = errors.New("activity provider: invalid timezone")
	ErrInvalidRange       = errors.New("activity provider: invalid date range")
	ErrInvalidResponse    = errors.New("activity provider: invalid response")
	ErrResponseTooLarge   = errors.New("activity provider: response too large")
	ErrRateLimited        = errors.New("activity provider: rate limited")
	ErrRedirectNotAllowed = errors.New("activity provider: redirect not allowed")
)

// HTTPError is the stable representation of an upstream non-success response.
// Body is retained for source compatibility but is deliberately always empty:
// provider bodies can contain private repository details or echoed credentials.
type HTTPError struct {
	Provider    string
	StatusCode  int
	Body        string
	RateLimited bool
	RetryAfter  time.Duration
}

type requestError struct {
	provider string
	cause    error
}

func (err *requestError) Error() string {
	return fmt.Sprintf("activity provider %s: request failed", err.provider)
}

func (err *requestError) Unwrap() error { return err.cause }

func (err *HTTPError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.RateLimited {
		return fmt.Sprintf("activity provider %s: rate limited (HTTP %d)", err.Provider, err.StatusCode)
	}
	return fmt.Sprintf("activity provider %s: HTTP status %d", err.Provider, err.StatusCode)
}

func (err *HTTPError) Unwrap() error {
	if err != nil && err.RateLimited {
		return ErrRateLimited
	}
	return nil
}

type httpProvider struct {
	client  *http.Client
	baseURL *url.URL
	token   string
}

func newHTTPProvider(client *http.Client, baseURL, allowedBaseURL, token string, network HTTPNetwork) (httpProvider, error) {
	parsed, err := validateBaseURL(baseURL, allowedBaseURL, client != nil)
	if err != nil {
		return httpProvider{}, fmt.Errorf("%w: %q", ErrInvalidBaseURL, baseURL)
	}
	securedClient, err := fixedhttp.NewClient(
		client,
		[]string{parsed.String()},
		fixedhttp.Network(network),
		defaultHTTPTimeout,
		ErrRedirectNotAllowed,
	)
	if err != nil {
		return httpProvider{}, err
	}
	return httpProvider{client: securedClient, baseURL: parsed, token: token}, nil
}

func validateBaseURL(raw, allowed string, allowLoopbackFixture bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidBaseURL
	}
	allowedURL, err := url.Parse(allowed)
	if err != nil {
		return nil, ErrInvalidBaseURL
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	allowedURL.Path = strings.TrimSuffix(allowedURL.Path, "/")
	if parsed.Scheme == allowedURL.Scheme && strings.EqualFold(parsed.Host, allowedURL.Host) && parsed.Path == allowedURL.Path {
		return parsed, nil
	}
	// Local fixture servers are the sole supported override. Requiring an
	// explicitly supplied client keeps this path out of default production use.
	if allowLoopbackFixture && (parsed.Scheme == "http" || parsed.Scheme == "https") && isLoopbackHost(parsed.Hostname()) {
		return parsed, nil
	}
	return nil, ErrInvalidBaseURL
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (provider httpProvider) endpoint(segments ...string) *url.URL {
	endpoint := *provider.baseURL
	parts := append([]string{endpoint.Path}, segments...)
	endpoint.Path = path.Join(parts...)
	return &endpoint
}

func (provider httpProvider) doJSON(request *http.Request, providerKey string, target any) (*http.Response, error) {
	response, err := provider.client.Do(request)
	if err != nil {
		return nil, &requestError{provider: providerKey, cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Drain only a small bounded prefix to make connection reuse possible,
		// but never retain or expose an untrusted upstream error body.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		rateLimited := response.StatusCode == http.StatusTooManyRequests ||
			(response.StatusCode == http.StatusForbidden && rateLimitRemaining(response.Header) == "0")
		return nil, &HTTPError{
			Provider: providerKey, StatusCode: response.StatusCode, RateLimited: rateLimited,
			RetryAfter: retryAfter(response.Header, time.Now()),
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: read response", ErrInvalidResponse, providerKey)
	}
	if len(body) > maxResponseBody {
		return nil, fmt.Errorf("%w: %s", ErrResponseTooLarge, providerKey)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidResponse, providerKey, err)
	}
	return response, nil
}

func rateLimitRemaining(header http.Header) string {
	if remaining := strings.TrimSpace(header.Get("X-RateLimit-Remaining")); remaining != "" {
		return remaining
	}
	return strings.TrimSpace(header.Get("RateLimit-Remaining"))
}

func isAccountNotFound(err error) bool {
	var httpError *HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode == http.StatusNotFound
}

func retryAfter(header http.Header, now time.Time) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return retryAt.Sub(now).Round(time.Second)
	}
	for _, name := range []string{"X-RateLimit-Reset", "RateLimit-Reset"} {
		if seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get(name)), 10, 64); err == nil {
			retryAt := time.Unix(seconds, 0)
			if retryAt.After(now) {
				return retryAt.Sub(now).Round(time.Second)
			}
		}
	}
	return 0
}

func resolveFetchInput(input app.ProviderFetchInput) (*time.Location, time.Time, time.Time, error) {
	providerSubject := input.ProviderSubject
	if providerSubject == "" {
		providerSubject = input.Subject
	}
	if !validSubject(string(providerSubject)) {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%w: %q", ErrInvalidSubject, providerSubject)
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%w: %q", ErrInvalidTimezone, input.Timezone)
	}
	from, err := time.Parse(time.DateOnly, string(input.From))
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%w: from %q", ErrInvalidRange, input.From)
	}
	to, err := time.Parse(time.DateOnly, string(input.To))
	if err != nil || from.After(to) {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%w: to %q", ErrInvalidRange, input.To)
	}
	return location, from, to, nil
}

func providerSubject(input app.ProviderFetchInput) domain.SubjectID {
	if input.ProviderSubject != "" {
		return input.ProviderSubject
	}
	return input.Subject
}

func validSubject(subject string) bool {
	if len(subject) == 0 || len(subject) > maxSubjectLength || subject == "." || subject == ".." {
		return false
	}
	for _, character := range subject {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func dateAt(timestamp time.Time, location *time.Location) domain.Date {
	return domain.Date(timestamp.In(location).Format(time.DateOnly))
}

func factInRange(date domain.Date, from, to time.Time) bool {
	text := string(date)
	return text >= from.Format(time.DateOnly) && text <= to.Format(time.DateOnly)
}

func newFact(
	input app.ProviderFetchInput,
	environmentID domain.EnvironmentID,
	date domain.Date,
	action domain.ActionID,
	value int,
	metadata map[string]string,
) domain.Fact {
	return domain.Fact{
		Subject:       input.Subject,
		Date:          date,
		EnvironmentID: environmentID,
		Action:        action,
		Metric:        domain.Metric{Name: domain.MetricCount, Value: value},
		Metadata:      metadata,
	}
}

func stringInt(value int64) string { return strconv.FormatInt(value, 10) }
