package graphql

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/identity"
)

// AuthAccountService is the application port for account authentication
// mutations. No bearer token or repository adapter crosses this boundary.
type AuthAccountService interface {
	RequestMagicLink(context.Context, string, string) error
	GetSessionByID(context.Context, string, string) (auth.Session, error)
	RevokeSessionByID(context.Context, string, string) error
	RevokeOtherSessionsExceptID(context.Context, string, string) error
}

// AuthTransport is installed only by the trusted HTTP boundary. A session
// token may be handed to SetSessionCookie, but must never enter GraphQL data.
// SetSessionCookie must be failure-atomic: on error it must not write or flush
// a Set-Cookie header, response body, or any other successful HTTP response.
// The caller will revoke the issued session and may best-effort clear cookies.
type AuthTransport interface {
	SetSessionCookie(token string, expiresAt time.Time) error
	ClearSessionCookie() error
}

// AuthMutationFailureReporter records a fixed failure event without access to
// the recipient, redirect URI, token, or underlying service error.
type AuthMutationFailureReporter interface {
	ReportMagicLinkRequestFailure(context.Context)
	ReportSessionCompensationFailure(context.Context)
}

type authAccountServiceContextKey struct{}
type authTransportContextKey struct{}
type verifiedSessionIDContextKey struct{}
type magicLinkRedirectsContextKey struct{}
type authMutationFailureReporterContextKey struct{}

func ContextWithAuthAccountService(ctx context.Context, service AuthAccountService) context.Context {
	return context.WithValue(ctx, authAccountServiceContextKey{}, service)
}

func ContextWithAuthTransport(ctx context.Context, transport AuthTransport) context.Context {
	return context.WithValue(ctx, authTransportContextKey{}, transport)
}

func authTransportFromContext(ctx context.Context) (AuthTransport, bool) {
	transport, ok := ctx.Value(authTransportContextKey{}).(AuthTransport)
	return transport, ok && transport != nil
}

// ContextWithVerifiedSessionID accepts only the current session ID obtained
// by server-side bearer verification, never a GraphQL input value.
func ContextWithVerifiedSessionID(ctx context.Context, sessionID string) context.Context {
	parsed, err := uuid.Parse(sessionID)
	if err != nil {
		return context.WithValue(ctx, verifiedSessionIDContextKey{}, "")
	}
	return context.WithValue(ctx, verifiedSessionIDContextKey{}, parsed.String())
}

// ContextWithMagicLinkRedirects installs the configured exact-match allowlist.
// Without it, only requests with no redirect URI are accepted.
func ContextWithMagicLinkRedirects(ctx context.Context, redirects []string) context.Context {
	return context.WithValue(ctx, magicLinkRedirectsContextKey{}, append([]string(nil), redirects...))
}

func ContextWithAuthMutationFailureReporter(ctx context.Context, reporter AuthMutationFailureReporter) context.Context {
	return context.WithValue(ctx, authMutationFailureReporterContextKey{}, reporter)
}

func authMutationFailureReporterFromContext(ctx context.Context) (AuthMutationFailureReporter, bool) {
	reporter, ok := ctx.Value(authMutationFailureReporterContextKey{}).(AuthMutationFailureReporter)
	return reporter, ok && reporter != nil
}

func authMutationError(code, message, field string) []*model.MutationError {
	item := &model.MutationError{Code: code, Message: message}
	if field != "" {
		item.Field = &field
	}
	return []*model.MutationError{item}
}

func resolveRequestMagicLink(ctx context.Context, email string, redirectURI *string) (*model.RequestMagicLinkPayload, error) {
	viewer, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if viewer.failed {
		return nil, errNodeAuthentication
	}
	canonical, valid := identity.CanonicalEmail(email)
	if !valid {
		return &model.RequestMagicLinkPayload{Errors: authMutationError("BAD_USER_INPUT", "Enter a valid email address.", "email")}, nil
	}
	redirect := ""
	if redirectURI != nil {
		redirect = *redirectURI
	}
	if len(redirect) > 2048 || redirect != "" && !allowedMagicLinkRedirect(ctx, redirect) {
		return &model.RequestMagicLinkPayload{Errors: authMutationError("BAD_USER_INPUT", "Redirect URI is not allowed.", "redirectURI")}, nil
	}
	service, _ := ctx.Value(authAccountServiceContextKey{}).(AuthAccountService)
	if service == nil {
		return nil, errNodeLookup
	}
	reporter, ok := authMutationFailureReporterFromContext(ctx)
	if !ok {
		return nil, errNodeLookup
	}
	// Delivery and persistence errors can depend on recipient/account state.
	// The transport records a safe, correlation-only failure metric; the public
	// result remains identical to a successful request.
	if err := service.RequestMagicLink(ctx, canonical, redirect); err != nil {
		reporter.ReportMagicLinkRequestFailure(ctx)
	}
	return &model.RequestMagicLinkPayload{Errors: []*model.MutationError{}, Accepted: true}, nil
}

func allowedMagicLinkRedirect(ctx context.Context, redirect string) bool {
	allowed, _ := ctx.Value(magicLinkRedirectsContextKey{}).([]string)
	for _, candidate := range allowed {
		if redirect == candidate {
			return true
		}
	}
	return false
}

func resolveSignOut(ctx context.Context) (*model.SignOutPayload, error) {
	actor, currentID, service, err := trustedAccountMutation(ctx)
	if err != nil {
		return nil, err
	}
	transport, ok := authTransportFromContext(ctx)
	if !ok {
		return nil, errNodeLookup
	}
	if _, err := currentAccountSession(ctx, service, actor, currentID); err != nil {
		return nil, err
	}
	if err := service.RevokeSessionByID(ctx, actor, currentID); err != nil {
		return nil, redactCurrentSessionMutationError(err)
	}
	PublishMutationAuditTarget(ctx, "session", currentID)
	// Once server-side revocation succeeds, the cookie is cleared even if a
	// follow-up metadata lookup fails. A failed clear returns only a generic
	// transport error; the session remains revoked.
	clearErr := transport.ClearSessionCookie()
	session, lookupErr := currentAccountSession(ctx, service, actor, currentID)
	if clearErr != nil || lookupErr != nil {
		return nil, errNodeLookup
	}
	return &model.SignOutPayload{Errors: []*model.MutationError{}, Session: projectSession(session)}, nil
}

func resolveRevokeSession(ctx context.Context, globalID string) (*model.RevokeSessionPayload, error) {
	actor, currentID, service, err := trustedAccountMutation(ctx)
	if err != nil {
		return nil, err
	}
	rawID, err := relayid.DecodeAs(relayid.Session, globalID)
	if err != nil {
		return &model.RevokeSessionPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid session ID.", "id")}, nil
	}
	parsed, err := uuid.Parse(rawID)
	if err != nil {
		return &model.RevokeSessionPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid session ID.", "id")}, nil
	}
	targetID := parsed.String()
	session, err := service.GetSessionByID(ctx, actor, targetID)
	if errors.Is(err, auth.ErrNotFound) {
		return &model.RevokeSessionPayload{Errors: authMutationError("NOT_FOUND", "Session not found.", "id")}, nil
	}
	if err != nil {
		return nil, errNodeLookup
	}
	if !ownedAccountSession(session, actor, targetID) {
		return &model.RevokeSessionPayload{Errors: authMutationError("NOT_FOUND", "Session not found.", "id")}, nil
	}
	if session.RevokedAt != nil {
		return &model.RevokeSessionPayload{Errors: authMutationError("CONFLICT", "Session is already revoked.", "id")}, nil
	}
	var transport AuthTransport
	if targetID == currentID {
		var ok bool
		transport, ok = authTransportFromContext(ctx)
		if !ok {
			return nil, errNodeLookup
		}
	}
	if err := service.RevokeSessionByID(ctx, actor, targetID); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return &model.RevokeSessionPayload{Errors: authMutationError("NOT_FOUND", "Session not found.", "id")}, nil
		}
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "session", targetID)
	if transport != nil && transport.ClearSessionCookie() != nil {
		return nil, errNodeLookup
	}
	session, err = service.GetSessionByID(ctx, actor, targetID)
	if err != nil || !ownedAccountSession(session, actor, targetID) {
		return nil, errNodeLookup
	}
	return &model.RevokeSessionPayload{Errors: []*model.MutationError{}, Session: projectSession(session)}, nil
}

func resolveRevokeOtherSessions(ctx context.Context) (*model.RevokeOtherSessionsPayload, error) {
	actor, currentID, service, err := trustedAccountMutation(ctx)
	if err != nil {
		return nil, err
	}
	session, err := currentAccountSession(ctx, service, actor, currentID)
	if err != nil {
		return nil, err
	}
	if err := service.RevokeOtherSessionsExceptID(ctx, actor, currentID); err != nil {
		return nil, redactCurrentSessionMutationError(err)
	}
	return &model.RevokeOtherSessionsPayload{Errors: []*model.MutationError{}, CurrentSession: projectSession(session)}, nil
}

func trustedAccountMutation(ctx context.Context) (actor, currentID string, service AuthAccountService, err error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	currentID, _ = ctx.Value(verifiedSessionIDContextKey{}).(string)
	if identity.failed || identity.userID == "" || currentID == "" {
		return "", "", nil, errNodeAuthentication
	}
	service, _ = ctx.Value(authAccountServiceContextKey{}).(AuthAccountService)
	if service == nil {
		return "", "", nil, errNodeLookup
	}
	return identity.userID, currentID, service, nil
}

func currentAccountSession(ctx context.Context, service AuthAccountService, actor, sessionID string) (auth.Session, error) {
	session, err := service.GetSessionByID(ctx, actor, sessionID)
	if err != nil {
		return auth.Session{}, redactCurrentSessionMutationError(err)
	}
	if !ownedAccountSession(session, actor, sessionID) {
		return auth.Session{}, errNodeAuthentication
	}
	return session, nil
}

func ownedAccountSession(session auth.Session, actor, sessionID string) bool {
	actual, err := uuid.Parse(session.ID)
	return err == nil && session.UserID == actor && actual.String() == sessionID
}

func redactCurrentSessionMutationError(err error) error {
	if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrInvalidSession) || errors.Is(err, auth.ErrUserDisabled) {
		return errNodeAuthentication
	}
	return errNodeLookup
}
