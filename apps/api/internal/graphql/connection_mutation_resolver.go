package graphql

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

// CookieBoundOAuthStarter is installed per request by the trusted HTTP edge.
// Start must authenticate the same session cookie as the verified viewer, bind
// the authorization state to it, enforce the configured exact redirect
// allowlist, and revoke a pending connection if state creation fails. It must
// return no authorization URL on failure. No cookie or bearer token enters a
// GraphQL argument, context value, or response.
type CookieBoundOAuthStarter interface {
	Start(context.Context, integrations.ConnectInput, string) (integrations.ProviderConnection, string, error)
}

// OAuthCompensationFailureReporter receives a fixed event only. It must never
// receive a connection ID, credential, redirect, or persistence error.
type OAuthCompensationFailureReporter interface {
	ReportOAuthCompensationFailure(context.Context)
}

type ConnectionMutationServices struct {
	Connections interface {
		ConnectPublic(context.Context, integrations.ConnectInput) (integrations.ProviderConnection, error)
		ConnectToken(context.Context, integrations.ConnectTokenInput) (integrations.ProviderConnection, error)
		Update(context.Context, integrations.UpdateConnectionInput) (integrations.ProviderConnection, error)
		Revoke(context.Context, string) (integrations.ProviderConnection, error)
	}
	Sync interface {
		EnqueueManualSync(context.Context, integrations.ManualSyncInput) (integrations.SyncJob, error)
	}
	OAuth    CookieBoundOAuthStarter
	Failures OAuthCompensationFailureReporter
}

type connectionMutationServicesContextKey struct{}
type verifiedOAuthCookieSessionContextKey struct{}
type providerOAuthRedirectsContextKey struct{}

func ContextWithConnectionMutationServices(ctx context.Context, services ConnectionMutationServices) context.Context {
	return context.WithValue(ctx, connectionMutationServicesContextKey{}, services)
}

// ContextWithVerifiedOAuthCookieSession marks a request after the transport
// verified a browser session cookie, in addition to the viewer identity.
func ContextWithVerifiedOAuthCookieSession(ctx context.Context) context.Context {
	return context.WithValue(ctx, verifiedOAuthCookieSessionContextKey{}, true)
}

func ContextWithProviderOAuthRedirects(ctx context.Context, redirects []string) context.Context {
	return context.WithValue(ctx, providerOAuthRedirectsContextKey{}, append([]string(nil), redirects...))
}

func trustedConnectionMutation(ctx context.Context) (string, ConnectionMutationServices, NodeServices, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" {
		return "", ConnectionMutationServices{}, NodeServices{}, errNodeAuthentication
	}
	services, _ := ctx.Value(connectionMutationServicesContextKey{}).(ConnectionMutationServices)
	nodes, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	if services.Connections == nil || nodes.Subjects == nil || nodes.Connections == nil {
		return "", ConnectionMutationServices{}, NodeServices{}, errNodeLookup
	}
	return identity.userID, services, nodes, nil
}

func connectionMutationError(err error, field string) ([]*model.MutationError, error) {
	switch {
	case errors.Is(err, integrations.ErrNotFound), errors.Is(err, integrations.ErrUnauthorized):
		return authMutationError("NOT_FOUND", "Connection not found.", field), nil
	case errors.Is(err, integrations.ErrConflict), errors.Is(err, integrations.ErrIdempotencyConflict), errors.Is(err, integrations.ErrIdempotencyInProgress), errors.Is(err, integrations.ErrInvalidConnectionStatus), errors.Is(err, integrations.ErrConnectionNotSyncable), errors.Is(err, integrations.ErrSyncAlreadyRunning):
		return authMutationError("CONFLICT", "The operation conflicts with the current state.", field), nil
	case errors.Is(err, integrations.ErrInvalidProvider), errors.Is(err, integrations.ErrInvalidIdentifier), errors.Is(err, integrations.ErrUnsupportedAuthMethod), errors.Is(err, integrations.ErrEmptySubjectID), errors.Is(err, integrations.ErrEmptyEnvironmentID), errors.Is(err, integrations.ErrEmptyConnectionID), errors.Is(err, integrations.ErrEmptyAccessToken), errors.Is(err, integrations.ErrAccessTokenTooLong), errors.Is(err, integrations.ErrTooManyScopes), errors.Is(err, integrations.ErrDuplicateScope), errors.Is(err, integrations.ErrInvalidScope), errors.Is(err, integrations.ErrPrivateDataUnsupported), errors.Is(err, integrations.ErrInvalidIdempotencyKey), errors.Is(err, integrations.ErrInvalidSyncDate), errors.Is(err, integrations.ErrInvalidSyncDateRange), errors.Is(err, activity.ErrInvalidFetchFailurePolicy):
		return authMutationError("BAD_USER_INPUT", "Invalid connection input.", field), nil
	default:
		return nil, errNodeLookup
	}
}

func decodeConnectionMutationID(global string) (string, []*model.MutationError) {
	raw, err := relayid.DecodeAs(relayid.ProviderConnection, global)
	if err != nil {
		return "", authMutationError("BAD_USER_INPUT", "Invalid connection ID.", "id")
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return "", authMutationError("BAD_USER_INPUT", "Invalid connection ID.", "id")
	}
	return raw, nil
}

func ownedMutationConnection(ctx context.Context, actor, raw string, nodes NodeServices) (integrations.ProviderConnection, []*model.MutationError, error) {
	connection, err := nodes.Connections.Get(ctx, raw)
	if err != nil {
		validation, publicErr := connectionMutationError(err, "id")
		return integrations.ProviderConnection{}, validation, publicErr
	}
	if connection.ID != raw || connection.SubjectID == "" || connection.Status == integrations.ConnectionRevoked {
		return integrations.ProviderConnection{}, authMutationError("NOT_FOUND", "Connection not found.", "id"), nil
	}
	owned, err := ownsSubject(ctx, nodes.Subjects, actor, connection.SubjectID)
	if err != nil {
		return integrations.ProviderConnection{}, nil, errNodeLookup
	}
	if !owned {
		return integrations.ProviderConnection{}, authMutationError("NOT_FOUND", "Connection not found.", "id"), nil
	}
	return connection, nil, nil
}

func resolveConnectProvider(ctx context.Context, input model.ConnectProviderInput) (*model.ConnectProviderPayload, error) {
	actor, services, nodes, err := trustedConnectionMutation(ctx)
	if err != nil {
		return nil, err
	}
	subjectID, err := relayid.DecodeAs(relayid.Subject, input.SubjectID)
	if err != nil {
		return &model.ConnectProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid subject ID.", "subjectID")}, nil
	}
	owned, err := ownsSubject(ctx, nodes.Subjects, actor, subjectID)
	if err != nil {
		return nil, errNodeLookup
	}
	if !owned {
		return &model.ConnectProviderPayload{Errors: authMutationError("NOT_FOUND", "Subject not found.", "subjectID")}, nil
	}
	subject, err := nodes.Subjects.GetSubject(ctx, actor, subjectID)
	if err != nil || subject.ID != subjectID || subject.OwnerUserID != actor || subject.Handle == "" {
		return nil, errNodeLookup
	}
	includePrivate := input.IncludePrivate != nil && *input.IncludePrivate
	base := integrations.ConnectInput{SubjectID: subjectID, ProviderID: input.ProviderID, EnvironmentID: input.ProviderID, ExternalAccountLogin: subject.Handle, Scopes: input.Scopes, IncludePrivate: includePrivate}
	var connection integrations.ProviderConnection
	var authorizationURL *string
	switch input.AuthMethod {
	case model.ProviderAuthMethodPublic:
		if input.Token != nil || input.RedirectURI != nil {
			return &model.ConnectProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Unsupported field for public connection.", "input")}, nil
		}
		base.ExternalAccountID = subject.Handle
		connection, err = services.Connections.ConnectPublic(ctx, base)
	case model.ProviderAuthMethodToken:
		if input.Token == nil || input.RedirectURI != nil {
			return &model.ConnectProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Token is required.", "token")}, nil
		}
		base.ExternalAccountID = subject.Handle
		connection, err = services.Connections.ConnectToken(ctx, integrations.ConnectTokenInput{ConnectInput: base, Credentials: integrations.TokenCredentials{AccessToken: *input.Token}})
	case model.ProviderAuthMethodOauth2:
		if input.Token != nil {
			return &model.ConnectProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Token is not valid for OAuth.", "token")}, nil
		}
		if ctx.Value(verifiedOAuthCookieSessionContextKey{}) != true {
			return nil, errNodeAuthentication
		}
		if services.OAuth == nil || services.Failures == nil {
			return nil, errNodeLookup
		}
		redirect := ""
		if input.RedirectURI != nil {
			redirect = *input.RedirectURI
		}
		if len(redirect) > 2048 || redirect != "" && !allowedProviderOAuthRedirect(ctx, redirect) {
			return &model.ConnectProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Redirect URI is not allowed.", "redirectURI")}, nil
		}
		var started string
		connection, started, err = services.OAuth.Start(ctx, base, redirect)
		if err == nil {
			if !matchesPendingOAuthStart(connection, base) {
				reportOAuthCompensationFailure(ctx, services.Failures)
				return nil, errNodeLookup
			}
			if started == "" {
				compensateIncompleteOAuthStart(ctx, services, connection, base)
				return nil, errNodeLookup
			}
			authorizationURL = &started
		}
	default:
		return &model.ConnectProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid auth method.", "authMethod")}, nil
	}
	if err != nil {
		validation, publicErr := connectionMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.ConnectProviderPayload{Errors: validation}, nil
	}
	if connection.ID == "" || connection.SubjectID != subjectID || connection.Status == integrations.ConnectionRevoked {
		return nil, errNodeLookup
	}
	projected := projectProviderConnection(connection)
	if projected.ID == "" {
		return nil, errNodeLookup
	}
	return &model.ConnectProviderPayload{Errors: []*model.MutationError{}, Connection: projected, AuthorizationURL: authorizationURL}, nil
}

func matchesPendingOAuthStart(connection integrations.ProviderConnection, input integrations.ConnectInput) bool {
	parsed, err := uuid.Parse(connection.ID)
	return err == nil && parsed.String() == connection.ID &&
		connection.SubjectID == input.SubjectID && connection.ProviderID == input.ProviderID &&
		connection.EnvironmentID == input.EnvironmentID && connection.AuthMethod == integrations.AuthOAuth2 &&
		connection.Status == integrations.ConnectionPending
}

func compensateIncompleteOAuthStart(ctx context.Context, services ConnectionMutationServices, connection integrations.ProviderConnection, input integrations.ConnectInput) {
	// Browser cancellation must not turn a missing authorization URL into a
	// permanently orphaned pending row. The deadline bounds recovery work.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if !matchesPendingOAuthStart(connection, input) {
		reportOAuthCompensationFailure(ctx, services.Failures)
		return
	}
	revoked, err := services.Connections.Revoke(cleanupCtx, connection.ID)
	if err != nil || revoked.ID != connection.ID || revoked.Status != integrations.ConnectionRevoked {
		reportOAuthCompensationFailure(ctx, services.Failures)
	}
}

func reportOAuthCompensationFailure(ctx context.Context, reporter OAuthCompensationFailureReporter) {
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	reporter.ReportOAuthCompensationFailure(reportCtx)
}

func allowedProviderOAuthRedirect(ctx context.Context, redirect string) bool {
	allowed, _ := ctx.Value(providerOAuthRedirectsContextKey{}).([]string)
	for _, candidate := range allowed {
		if candidate == redirect {
			return true
		}
	}
	return false
}

func resolveUpdateProviderConnection(ctx context.Context, input model.UpdateProviderConnectionInput) (*model.UpdateProviderConnectionPayload, error) {
	actor, services, nodes, err := trustedConnectionMutation(ctx)
	if err != nil {
		return nil, err
	}
	raw, invalid := decodeConnectionMutationID(input.ID)
	if invalid != nil {
		return &model.UpdateProviderConnectionPayload{Errors: invalid}, nil
	}
	previous, denied, err := ownedMutationConnection(ctx, actor, raw, nodes)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return &model.UpdateProviderConnectionPayload{Errors: denied}, nil
	}
	var scopes *[]string
	if input.Scopes != nil {
		value := append([]string{}, input.Scopes...)
		scopes = &value
	}
	connection, err := services.Connections.Update(ctx, integrations.UpdateConnectionInput{ID: raw, Token: input.Token, Scopes: scopes, Enabled: input.Enabled})
	if err != nil {
		validation, publicErr := connectionMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.UpdateProviderConnectionPayload{Errors: validation}, nil
	}
	if connection.ID != raw || connection.SubjectID != previous.SubjectID || connection.Status == integrations.ConnectionRevoked {
		return nil, errNodeLookup
	}
	return &model.UpdateProviderConnectionPayload{Errors: []*model.MutationError{}, Connection: projectProviderConnection(connection)}, nil
}

func resolveRevokeProviderConnection(ctx context.Context, input model.RevokeProviderConnectionInput) (*model.RevokeProviderConnectionPayload, error) {
	actor, services, nodes, err := trustedConnectionMutation(ctx)
	if err != nil {
		return nil, err
	}
	raw, invalid := decodeConnectionMutationID(input.ID)
	if invalid != nil {
		return &model.RevokeProviderConnectionPayload{Errors: invalid}, nil
	}
	_, denied, err := ownedMutationConnection(ctx, actor, raw, nodes)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return &model.RevokeProviderConnectionPayload{Errors: denied}, nil
	}
	revoked, err := services.Connections.Revoke(ctx, raw)
	if err != nil {
		validation, publicErr := connectionMutationError(err, "id")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.RevokeProviderConnectionPayload{Errors: validation}, nil
	}
	if revoked.ID != raw || revoked.Status != integrations.ConnectionRevoked {
		return nil, errNodeLookup
	}
	return &model.RevokeProviderConnectionPayload{Errors: []*model.MutationError{}, RevokedConnectionID: &input.ID}, nil
}

func resolveEnqueueManualSync(ctx context.Context, input model.EnqueueManualSyncInput) (*model.EnqueueManualSyncPayload, error) {
	actor, services, nodes, err := trustedConnectionMutation(ctx)
	if err != nil {
		return nil, err
	}
	raw, invalid := decodeConnectionMutationID(input.ConnectionID)
	if invalid != nil {
		return &model.EnqueueManualSyncPayload{Errors: invalid}, nil
	}
	_, denied, err := ownedMutationConnection(ctx, actor, raw, nodes)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return &model.EnqueueManualSyncPayload{Errors: denied}, nil
	}
	if services.Sync == nil {
		return nil, errNodeLookup
	}
	if n := utf8.RuneCountInString(input.IdempotencyKey); n < 8 || n > 255 {
		return &model.EnqueueManualSyncPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid idempotency key.", "idempotencyKey")}, nil
	}
	request := integrations.ManualSyncInput{ConnectionID: raw, IdempotencyKey: input.IdempotencyKey, Force: input.Force != nil && *input.Force}
	if input.From != nil {
		from := activity.Date(*input.From)
		request.From = &from
	}
	if input.To != nil {
		to := activity.Date(*input.To)
		request.To = &to
	}
	if input.FailurePolicy != nil {
		switch *input.FailurePolicy {
		case model.FetchFailurePolicyKeepStale:
			request.FailurePolicy = activity.FetchFailureKeepStale
		case model.FetchFailurePolicyPurge:
			request.FailurePolicy = activity.FetchFailurePurge
		default:
			return &model.EnqueueManualSyncPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid failure policy.", "failurePolicy")}, nil
		}
	}
	job, err := services.Sync.EnqueueManualSync(ctx, request)
	if err != nil {
		validation, publicErr := connectionMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.EnqueueManualSyncPayload{Errors: validation}, nil
	}
	globalID := relayid.Encode(relayid.SyncJob, job.ID)
	if globalID == "" || job.ConnectionID != raw || job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.Attempt < 0 {
		return nil, errNodeLookup
	}
	projected := &model.SyncJob{ID: globalID, Status: string(job.Status), Attempt: job.Attempt, CreatedAt: scalar.DateTime(job.CreatedAt), UpdatedAt: scalar.DateTime(job.UpdatedAt)}
	return &model.EnqueueManualSyncPayload{Errors: []*model.MutationError{}, Job: projected}, nil
}
