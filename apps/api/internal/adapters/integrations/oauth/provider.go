package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fixedhttp"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const maxProviderResponseBytes = 1 << 20

const tokenRevocationTimeout = 10 * time.Second

type revocationStyle uint8

const (
	revocationLocalOnly revocationStyle = iota
	revocationGitHub
	revocationOAuthForm
)

type providerDefinition struct {
	id               string
	defaultEndpoints Endpoints
	defaultScopes    []string
	usernameField    string
	revocationStyle  revocationStyle
}

// Adapter implements a complete authorization-code + PKCE flow for one
// provider. Construct one with NewGitHub, NewGitLab, or NewCodeberg.
type Adapter struct {
	definition            providerDefinition
	clientID              string
	secret                string
	redirect              string
	scopes                []string
	endpoints             Endpoints
	http                  *http.Client
	states                StateStore
	clock                 Clock
	random                io.Reader
	stateTTL              time.Duration
	requireSessionBinding bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func clockOrDefault(clock Clock) Clock {
	if clock == nil {
		return systemClock{}
	}
	return clock
}

func newAdapter(definition providerDefinition, config Config) (*Adapter, error) {
	clock := clockOrDefault(config.Clock)
	endpoints := mergeEndpoints(definition.defaultEndpoints, config.Endpoints)
	stateTTL := config.StateTTL
	if stateTTL == 0 {
		stateTTL = defaultStateTTL
	}
	if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" ||
		strings.TrimSpace(config.RedirectURI) == "" || stateTTL <= 0 {
		return nil, ErrInvalidConfiguration
	}
	if !absoluteURL(config.RedirectURI) || !absoluteURL(endpoints.AuthorizationURL) ||
		!absoluteURL(endpoints.TokenURL) || !absoluteURL(endpoints.IdentityURL) ||
		(definition.revocationStyle != revocationLocalOnly && !absoluteURL(endpoints.RevocationURL)) {
		return nil, ErrInvalidConfiguration
	}
	serverEndpoints := []string{endpoints.TokenURL, endpoints.IdentityURL}
	if definition.revocationStyle != revocationLocalOnly {
		serverEndpoints = append(serverEndpoints, endpoints.RevocationURL)
	}
	client, err := fixedhttp.NewClient(
		config.HTTPClient,
		serverEndpoints,
		fixedhttp.Network(config.Network),
		defaultTimeout,
		http.ErrUseLastResponse,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	stateStore := config.StateStore
	if stateStore == nil {
		stateStore = NewMemoryStateStore(clock)
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	scopes := normalizeScopes(config.Scopes)
	if len(scopes) == 0 {
		scopes = append([]string(nil), definition.defaultScopes...)
	}
	return &Adapter{
		definition:            definition,
		clientID:              strings.TrimSpace(config.ClientID),
		secret:                config.ClientSecret,
		redirect:              config.RedirectURI,
		scopes:                scopes,
		endpoints:             endpoints,
		http:                  client,
		states:                stateStore,
		clock:                 clock,
		random:                random,
		stateTTL:              stateTTL,
		requireSessionBinding: config.RequireSessionBinding,
	}, nil
}

func (adapter *Adapter) ProviderID() string { return adapter.definition.id }

// RevokeToken invalidates one OAuth access token where the provider exposes a
// documented server-side revocation endpoint. Codeberg is intentionally
// local-only until a compatible official endpoint and authentication contract
// can be guaranteed.
func (adapter *Adapter) RevokeToken(ctx context.Context, token []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(token) == 0 {
		return nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, tokenRevocationTimeout)
	defer cancel()
	var (
		request *http.Request
		err     error
	)
	switch adapter.definition.revocationStyle {
	case revocationLocalOnly:
		return nil
	case revocationGitHub:
		endpoint := strings.TrimRight(adapter.endpoints.RevocationURL, "/") + "/" + url.PathEscape(adapter.clientID) + "/token"
		body, marshalErr := json.Marshal(struct {
			AccessToken string `json:"access_token"`
		}{AccessToken: string(token)})
		if marshalErr != nil {
			return ErrTokenRevocation
		}
		defer clear(body)
		request, err = http.NewRequestWithContext(requestCtx, http.MethodDelete, endpoint, strings.NewReader(string(body)))
		if err == nil {
			request.SetBasicAuth(adapter.clientID, adapter.secret)
			request.Header.Set("Accept", "application/vnd.github+json")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		}
	case revocationOAuthForm:
		form := url.Values{
			"client_id":       {adapter.clientID},
			"client_secret":   {adapter.secret},
			"token":           {string(token)},
			"token_type_hint": {"access_token"},
		}
		request, err = http.NewRequestWithContext(requestCtx, http.MethodPost, adapter.endpoints.RevocationURL, strings.NewReader(form.Encode()))
		if err == nil {
			request.Header.Set("Accept", "application/json")
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	default:
		return nil
	}
	if err != nil {
		return ErrTokenRevocation
	}
	request.Header.Set("User-Agent", "jandibat.org")

	client := *adapter.http
	if client.Timeout <= 0 || client.Timeout > tokenRevocationTimeout {
		client.Timeout = tokenRevocationTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrTokenRevocation
	}
	defer response.Body.Close()
	body, err := readProviderBody(response.Body)
	clear(body)
	if err != nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ErrTokenRevocation
	}
	return nil
}

func (adapter *Adapter) Begin(ctx context.Context, request AuthorizationRequest) (AuthorizationResult, error) {
	if err := ctx.Err(); err != nil {
		return AuthorizationResult{}, err
	}
	if strings.TrimSpace(request.ConnectionID) == "" || strings.TrimSpace(request.SubjectID) == "" {
		return AuthorizationResult{}, ErrInvalidRequest
	}
	if adapter.requireSessionBinding && strings.TrimSpace(request.SessionBinding) == "" {
		return AuthorizationResult{}, ErrInvalidRequest
	}
	state, err := randomValue(adapter.random)
	if err != nil {
		return AuthorizationResult{}, fmt.Errorf("oauth: generate state: %w", err)
	}
	verifier, err := randomValue(adapter.random)
	if err != nil {
		return AuthorizationResult{}, fmt.Errorf("oauth: generate PKCE verifier: %w", err)
	}
	scopes := normalizeScopes(request.Scopes)
	if len(scopes) == 0 {
		scopes = append([]string(nil), adapter.scopes...)
	}
	if !scopeSubset(scopes, adapter.scopes) {
		return AuthorizationResult{}, ErrInvalidRequest
	}
	sessionBindingHash := ""
	if request.SessionBinding != "" {
		digest := sha256.Sum256([]byte(request.SessionBinding))
		sessionBindingHash = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	expiresAt := adapter.clock.Now().Add(adapter.stateTTL)
	flow := FlowState{
		ProviderID:         adapter.definition.id,
		ConnectionID:       request.ConnectionID,
		SubjectID:          request.SubjectID,
		RedirectURI:        adapter.redirect,
		CodeVerifier:       verifier,
		RequestedScopes:    scopes,
		SessionBindingHash: sessionBindingHash,
		ClientRedirectURI:  request.ClientRedirectURI,
		ExpiresAt:          expiresAt,
	}
	if err := adapter.states.Put(ctx, state, flow); err != nil {
		return AuthorizationResult{}, fmt.Errorf("oauth: store state: %w", err)
	}
	authorizationURL, err := url.Parse(adapter.endpoints.AuthorizationURL)
	if err != nil {
		return AuthorizationResult{}, ErrInvalidConfiguration
	}
	query := authorizationURL.Query()
	query.Set("client_id", adapter.clientID)
	query.Set("redirect_uri", adapter.redirect)
	query.Set("response_type", "code")
	query.Set("state", state)
	query.Set("scope", strings.Join(scopes, " "))
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	authorizationURL.RawQuery = query.Encode()
	return AuthorizationResult{URL: authorizationURL.String(), State: state, ExpiresAt: expiresAt}, nil
}

func (adapter *Adapter) Complete(ctx context.Context, request CallbackRequest) (CallbackResult, error) {
	sessionBindingHash := ""
	if request.SessionBinding != "" {
		digest := sha256.Sum256([]byte(request.SessionBinding))
		sessionBindingHash = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	flow, err := adapter.states.Consume(ctx, request.State, StateBinding{
		ProviderID:            adapter.definition.id,
		RedirectURI:           adapter.redirect,
		SessionBindingHash:    sessionBindingHash,
		RequireSessionBinding: adapter.requireSessionBinding,
	})
	if err != nil {
		if errorsIsContext(err) {
			return CallbackResult{}, err
		}
		return CallbackResult{}, ErrInvalidState
	}
	if request.Error != "" {
		return CallbackResult{}, ErrAuthorizationDenied
	}
	if strings.TrimSpace(request.Code) == "" {
		return CallbackResult{}, ErrInvalidRequest
	}
	credentials, grantedScopes, err := adapter.exchangeToken(ctx, request.Code, flow)
	if err != nil {
		return CallbackResult{}, err
	}
	identity, err := adapter.fetchIdentity(ctx, credentials.AccessToken)
	if err != nil {
		token := []byte(credentials.AccessToken)
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRevocationTimeout)
		_ = adapter.RevokeToken(revokeCtx, token)
		cancel()
		clear(token)
		return CallbackResult{}, err
	}
	if len(grantedScopes) == 0 {
		grantedScopes = append([]string(nil), flow.RequestedScopes...)
	}
	return CallbackResult{
		ProviderID:        adapter.definition.id,
		ConnectionID:      flow.ConnectionID,
		SubjectID:         flow.SubjectID,
		Identity:          identity,
		Scopes:            grantedScopes,
		Credentials:       credentials,
		ClientRedirectURI: flow.ClientRedirectURI,
	}, nil
}

func scopeSubset(requested, allowed []string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, scope := range allowed {
		set[scope] = struct{}{}
	}
	for _, scope := range requested {
		if _, ok := set[scope]; !ok {
			return false
		}
	}
	return true
}

func (adapter *Adapter) exchangeToken(ctx context.Context, code string, flow FlowState) (integrations.TokenCredentials, []string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {adapter.clientID},
		"client_secret": {adapter.secret},
		"code":          {code},
		"redirect_uri":  {flow.RedirectURI},
		"code_verifier": {flow.CodeVerifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return integrations.TokenCredentials{}, nil, ErrTokenExchange
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "jandibat.org")
	response, err := adapter.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return integrations.TokenCredentials{}, nil, ctx.Err()
		}
		return integrations.TokenCredentials{}, nil, ErrTokenExchange
	}
	defer response.Body.Close()
	body, err := readProviderBody(response.Body)
	if err != nil {
		return integrations.TokenCredentials{}, nil, ErrTokenExchange
	}
	defer clear(body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return integrations.TokenCredentials{}, nil, fmt.Errorf("%w (status %d)", ErrTokenExchange, response.StatusCode)
	}
	var payload struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		Scope        string          `json:"scope"`
		Error        string          `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Error != "" || payload.AccessToken == "" {
		return integrations.TokenCredentials{}, nil, ErrTokenExchange
	}
	var expiresAt *time.Time
	if seconds, ok := positiveSeconds(payload.ExpiresIn); ok {
		value := adapter.clock.Now().Add(time.Duration(seconds) * time.Second)
		expiresAt = &value
	}
	return integrations.TokenCredentials{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    expiresAt,
	}, parseGrantedScopes(payload.Scope), nil
}

func (adapter *Adapter) fetchIdentity(ctx context.Context, accessToken string) (AccountIdentity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, adapter.endpoints.IdentityURL, nil)
	if err != nil {
		return AccountIdentity{}, ErrIdentityLookup
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("User-Agent", "jandibat.org")
	response, err := adapter.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return AccountIdentity{}, ctx.Err()
		}
		return AccountIdentity{}, ErrIdentityLookup
	}
	defer response.Body.Close()
	body, err := readProviderBody(response.Body)
	if err != nil {
		return AccountIdentity{}, ErrIdentityLookup
	}
	defer clear(body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return AccountIdentity{}, fmt.Errorf("%w (status %d)", ErrIdentityLookup, response.StatusCode)
	}
	var payload struct {
		ID        json.RawMessage `json:"id"`
		Login     string          `json:"login"`
		Username  string          `json:"username"`
		Name      string          `json:"name"`
		AvatarURL string          `json:"avatar_url"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return AccountIdentity{}, ErrIdentityLookup
	}
	externalID, ok := parseExternalID(payload.ID)
	if !ok {
		return AccountIdentity{}, ErrIdentityLookup
	}
	username := payload.Login
	if adapter.definition.usernameField == "username" {
		username = payload.Username
	}
	if username == "" {
		username = payload.Username
	}
	return AccountIdentity{
		ExternalAccountID: externalID,
		Username:          username,
		DisplayName:       payload.Name,
		AvatarURL:         payload.AvatarURL,
	}, nil
}

func randomValue(random io.Reader) (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func pkceChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func mergeEndpoints(defaults, configured Endpoints) Endpoints {
	if configured.AuthorizationURL != "" {
		defaults.AuthorizationURL = configured.AuthorizationURL
	}
	if configured.TokenURL != "" {
		defaults.TokenURL = configured.TokenURL
	}
	if configured.IdentityURL != "" {
		defaults.IdentityURL = configured.IdentityURL
	}
	if configured.RevocationURL != "" {
		defaults.RevocationURL = configured.RevocationURL
	}
	return defaults
}

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func normalizeScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}

func parseGrantedScopes(value string) []string {
	return normalizeScopes(strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	}))
}

func positiveSeconds(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	value := strings.Trim(string(raw), `"`)
	seconds, err := strconv.ParseInt(value, 10, 64)
	return seconds, err == nil && seconds > 0
}

func parseExternalID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var text string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return "", false
		}
	} else {
		text = string(raw)
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

func readProviderBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxProviderResponseBytes+1))
	if err != nil || len(body) > maxProviderResponseBytes {
		clear(body)
		return nil, ErrTokenExchange
	}
	return body, nil
}

func errorsIsContext(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
