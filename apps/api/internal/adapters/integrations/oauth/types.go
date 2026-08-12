// Package oauth implements the OAuth 2.0 authorization-code flow used by the
// built-in Git hosting integrations.
package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fixedhttp"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const (
	defaultStateTTL = 10 * time.Minute
	defaultTimeout  = 10 * time.Second
)

var (
	ErrInvalidConfiguration = errors.New("oauth: invalid configuration")
	ErrInvalidRequest       = errors.New("oauth: invalid request")
	ErrInvalidState         = errors.New("oauth: invalid or expired state")
	ErrAuthorizationDenied  = errors.New("oauth: authorization denied")
	ErrTokenExchange        = errors.New("oauth: token exchange failed")
	ErrIdentityLookup       = errors.New("oauth: account identity lookup failed")
	ErrTokenRevocation      = errors.New("oauth: token revocation failed")
)

// Clock is intentionally compatible with integrations.Clock.
type Clock interface {
	Now() time.Time
}

// Endpoints allows deployments such as self-managed GitLab to replace the
// provider defaults. RevocationURL is provider-specific: GitHub treats it as
// the applications API base, while GitLab treats it as the exact RFC 7009
// endpoint. Codeberg connections currently use local-only revocation.
type Endpoints struct {
	AuthorizationURL string
	TokenURL         string
	IdentityURL      string
	RevocationURL    string
}

// Config contains credentials and process-local dependencies for an Adapter.
// ClientSecret is only sent to the provider's token endpoint and is never
// included in an authorization URL or error.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scopes       []string
	Endpoints    Endpoints

	HTTPClient *http.Client
	Network    HTTPNetwork
	StateStore StateStore
	Clock      Clock
	Random     io.Reader
	StateTTL   time.Duration
	// RequireSessionBinding rejects callbacks that do not carry the same
	// authenticated session token that initiated the flow. Only a SHA-256
	// digest is persisted in FlowState.
	RequireSessionBinding bool
}

// HTTPNetwork exposes deterministic resolver and dialer injection while the
// adapter retains its fixed-origin and resolved-address policy.
type HTTPNetwork = fixedhttp.Network

// AuthorizationRequest identifies the pending ConnectionService record and
// the authenticated subject that initiated it. Both values are bound to state.
type AuthorizationRequest struct {
	ConnectionID      string
	SubjectID         string
	Scopes            []string
	SessionBinding    string
	ClientRedirectURI string
}

// AuthorizationResult is safe to return to an authenticated API caller. State
// is already embedded in URL; it is exposed separately for callback tests and
// clients that need to correlate browser navigation.
type AuthorizationResult struct {
	URL       string
	State     string
	ExpiresAt time.Time
}

// CallbackRequest contains the provider callback query parameters. Provider
// error descriptions are deliberately not accepted so they cannot accidentally
// flow into logs or API errors.
type CallbackRequest struct {
	State          string
	Code           string
	Error          string
	SessionBinding string
}

type AccountIdentity struct {
	ExternalAccountID string
	Username          string
	DisplayName       string
	AvatarURL         string
}

// CallbackResult can be passed directly to ConnectionService.CompleteOAuth:
//
//	service.CompleteOAuth(ctx, result.ConnectionID, result.Credentials)
//
// Identity and SubjectID let the application layer verify and persist account
// ownership without putting identity data into token storage.
type CallbackResult struct {
	ProviderID        string
	ConnectionID      string
	SubjectID         string
	Identity          AccountIdentity
	Scopes            []string
	Credentials       integrations.TokenCredentials
	ClientRedirectURI string
}

// Flow is the application-facing contract implemented for each provider.
type Flow interface {
	ProviderID() string
	Begin(context.Context, AuthorizationRequest) (AuthorizationResult, error)
	Complete(context.Context, CallbackRequest) (CallbackResult, error)
}

// TokenRevoker is implemented by OAuth adapters. Providers without a safely
// supported official endpoint return success without making a network call so
// disconnect remains explicitly local-only.
type TokenRevoker interface {
	RevokeToken(context.Context, []byte) error
}

// ConnectionCompleter documents the exact ConnectionService boundary consumed
// by CallbackResult without coupling callers to the concrete service.
type ConnectionCompleter interface {
	CompleteOAuth(context.Context, string, integrations.TokenCredentials) (integrations.ProviderConnection, error)
}
