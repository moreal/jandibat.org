package apihttp

import (
	"context"
	"errors"
	stdhttp "net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	adapteroauth "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"go.uber.org/zap"
)

var errGraphQLDependencies = errors.New("graphql: required trusted runtime dependencies are missing")

// GraphQLDependencies holds application ports constructed at process startup.
// Authentication, cookies, OAuth state, audit and rate limiting remain owned
// by the existing HTTP dependency graph; no client input populates these ports.
type GraphQLDependencies struct {
	NodeServices            graph.NodeServices
	SubjectQueries          graph.SubjectQueryServices
	SubjectMutations        graph.SubjectMutationServices
	IntegrationQueries      graph.IntegrationQueryServices
	ConnectionMutations     graph.ConnectionMutationServices
	CustomProviderMutations graph.CustomProviderMutationServices
	SyncJobs                graph.SyncJobPageService
	AuthAccounts            graph.AuthAccountService
	Passkeys                graph.PasskeyMutationService
	AuthFailureReporter     graph.AuthMutationFailureReporter
	OAuthFlows              map[string]adapteroauth.Flow
	Development             bool
}

type graphQLAuditActorKey struct{}
type graphQLAuditActorHolder struct {
	actor atomic.Pointer[operations.AuditActor]
}

func withGraphQLAuditActorHolder(ctx context.Context) context.Context {
	return context.WithValue(ctx, graphQLAuditActorKey{}, &graphQLAuditActorHolder{})
}

func publishGraphQLAuditActor(ctx context.Context, userID string) {
	if holder, ok := ctx.Value(graphQLAuditActorKey{}).(*graphQLAuditActorHolder); ok {
		actor := operations.AuditActor{Type: operations.AuditActorUser, ID: userID}
		holder.actor.Store(&actor)
	}
}

func graphqlAuditActorFromContext(ctx context.Context) (operations.AuditActor, bool) {
	if holder, ok := ctx.Value(graphQLAuditActorKey{}).(*graphQLAuditActorHolder); ok {
		if actor := holder.actor.Load(); actor != nil {
			return *actor, true
		}
	}
	return operations.AuditActor{}, false
}

func validateGraphQLDependencies(deps Dependencies, graphDeps GraphQLDependencies) error {
	if missingGraphQLPort(deps.Auth) || missingGraphQLPort(deps.Sessions) || missingGraphQLPort(deps.Audit) || (!graphDeps.Development && missingGraphQLPort(deps.MutationAudits)) || missingGraphQLPort(deps.RateLimiter) || (!graphDeps.Development && len(deps.AuditSourceKey) == 0) ||
		missingGraphQLPort(graphDeps.NodeServices.ViewerUsers) || missingGraphQLPort(graphDeps.NodeServices.SessionPages) || missingGraphQLPort(graphDeps.NodeServices.Subjects) || missingGraphQLPort(graphDeps.NodeServices.Connections) || missingGraphQLPort(graphDeps.NodeServices.CustomProviders) || missingGraphQLPort(graphDeps.NodeServices.SyncJobs) || missingGraphQLPort(graphDeps.NodeServices.Sessions) || missingGraphQLPort(graphDeps.NodeServices.Activity) ||
		missingGraphQLPort(graphDeps.SubjectQueries.Pages) || missingGraphQLPort(graphDeps.SubjectQueries.UserSettings) || missingGraphQLPort(graphDeps.SubjectQueries.SubjectSettings) || missingGraphQLPort(graphDeps.SubjectMutations.Subjects) || missingGraphQLPort(graphDeps.SubjectMutations.Deletions) ||
		missingGraphQLPort(graphDeps.IntegrationQueries.Connections) || missingGraphQLPort(graphDeps.IntegrationQueries.CustomProviders) || missingGraphQLPort(graphDeps.ConnectionMutations.Connections) || missingGraphQLPort(graphDeps.ConnectionMutations.Sync) ||
		missingGraphQLPort(graphDeps.CustomProviderMutations.Providers) || missingGraphQLPort(graphDeps.SyncJobs) || missingGraphQLPort(graphDeps.AuthAccounts) || missingGraphQLPort(graphDeps.Passkeys) || missingGraphQLPort(graphDeps.AuthFailureReporter) {
		return errGraphQLDependencies
	}
	return nil
}

// Interface equality misses typed nil pointers. Reflection is restricted to
// startup validation and never examines or logs a port's contents.
func missingGraphQLPort(port any) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	kind := value.Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && value.IsNil()
}

// GraphQLCookieTransport is request-scoped. Cookie construction validates the
// entire value before writing headers, preserving the resolver's failure-atomic
// session issuance contract.
type GraphQLCookieTransport struct {
	Writer stdhttp.ResponseWriter
	Secure bool
}

func (sink GraphQLCookieTransport) SetSessionCookie(token string, expiresAt time.Time) error {
	if sink.Writer == nil || token == "" || expiresAt.IsZero() {
		return errGraphQLDependencies
	}
	cookie := &stdhttp.Cookie{Name: "jandibat_session", Value: token, Path: "/", Expires: expiresAt, MaxAge: int(time.Until(expiresAt).Seconds()), HttpOnly: true, Secure: sink.Secure, SameSite: stdhttp.SameSiteLaxMode}
	if err := cookie.Valid(); err != nil {
		return errGraphQLDependencies
	}
	stdhttp.SetCookie(sink.Writer, cookie)
	return nil
}

func (sink GraphQLCookieTransport) ClearSessionCookie() error {
	if sink.Writer == nil {
		return errGraphQLDependencies
	}
	cookie := &stdhttp.Cookie{Name: "jandibat_session", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), HttpOnly: true, Secure: sink.Secure, SameSite: stdhttp.SameSiteLaxMode}
	stdhttp.SetCookie(sink.Writer, cookie)
	return nil
}

func graphQLSessionMetadata(r *stdhttp.Request) auth.SessionMetadata {
	return auth.SessionMetadata{IPAddress: normalizedRemoteIP(r.RemoteAddr), UserAgent: r.UserAgent()}
}

func graphQLTrustedContext(deps Dependencies, graphDeps GraphQLDependencies, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		ctx := r.Context()
		ctx = graph.ContextWithNodeServices(ctx, graphDeps.NodeServices)
		ctx = graph.ContextWithSubjectQueryServices(ctx, graphDeps.SubjectQueries)
		ctx = graph.ContextWithSubjectMutationServices(ctx, graphDeps.SubjectMutations)
		ctx = graph.ContextWithIntegrationQueryServices(ctx, graphDeps.IntegrationQueries)
		ctx = graph.ContextWithCustomProviderMutationServices(ctx, graphDeps.CustomProviderMutations)
		ctx = graph.ContextWithSyncJobPageService(ctx, graphDeps.SyncJobs)
		ctx = graph.ContextWithAuthAccountService(ctx, graphDeps.AuthAccounts)
		ctx = graph.ContextWithPasskeyMutationService(ctx, graphDeps.Passkeys)
		ctx = graph.ContextWithPasskeySessionMetadata(ctx, graphQLSessionMetadata(r))
		ctx = graph.ContextWithAuthMutationFailureReporter(ctx, graphDeps.AuthFailureReporter)
		ctx = graph.ContextWithMagicLinkRedirects(ctx, deps.AllowedRedirects)
		ctx = graph.ContextWithProviderOAuthRedirects(ctx, deps.AllowedRedirects)
		ctx = graph.ContextWithAuthTransport(ctx, GraphQLCookieTransport{Writer: w, Secure: deps.SecureCookies})
		token, fromCookie, present, malformed := graphQLCredential(r)
		if malformed {
			ctx = graph.ContextWithFailedAuthentication(ctx)
		} else if present {
			if deps.Auth == nil || deps.Sessions == nil {
				ctx = graph.ContextWithFailedAuthentication(ctx)
			} else {
				user, userErr := deps.Auth.AuthenticateSession(ctx, token)
				session, sessionErr := deps.Sessions.CurrentSession(ctx, token)
				if userErr != nil || sessionErr != nil || user.ID == "" || session.UserID != user.ID || session.ID == "" {
					ctx = graph.ContextWithFailedAuthentication(ctx)
				} else {
					ctx = graph.ContextWithVerifiedViewer(ctx, user.ID)
					publishGraphQLAuditActor(ctx, user.ID)
					if !graphQLVerifiedAccountRateLimit(w, r, deps.RateLimiter, user.ID) {
						return
					}
					ctx = graph.ContextWithVerifiedSessionID(ctx, session.ID)
					if fromCookie {
						ctx = graph.ContextWithVerifiedOAuthCookieSession(ctx)
						connectionServices := graphDeps.ConnectionMutations
						connectionServices.OAuth = cookieBoundOAuthStarter{deps: deps, flows: graphDeps.OAuthFlows, token: token, userID: user.ID, sessionID: session.ID}
						connectionServices.Failures = fixedOAuthFailureReporter{logger: deps.Logger}
						ctx = graph.ContextWithConnectionMutationServices(ctx, connectionServices)
					}
				}
			}
		}
		if !fromCookie || !present || malformed {
			connectionServices := graphDeps.ConnectionMutations
			connectionServices.OAuth = nil
			connectionServices.Failures = fixedOAuthFailureReporter{logger: deps.Logger}
			ctx = graph.ContextWithConnectionMutationServices(ctx, connectionServices)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func graphQLCredential(r *stdhttp.Request) (token string, fromCookie, present, malformed bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 0 {
		if len(headers) != 1 {
			return "", false, true, true
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			return "", false, true, true
		}
		return parts[1], false, true, false
	}
	if cookie, err := r.Cookie("jandibat_session"); err == nil {
		if strings.TrimSpace(cookie.Value) == "" {
			return "", true, true, true
		}
		return cookie.Value, true, true, false
	}
	return "", false, false, false
}

type cookieBoundOAuthStarter struct {
	deps      Dependencies
	flows     map[string]adapteroauth.Flow
	token     string
	userID    string
	sessionID string
}

func (starter cookieBoundOAuthStarter) Start(ctx context.Context, input integrations.ConnectInput, redirect string) (integrations.ProviderConnection, string, error) {
	if starter.deps.Auth == nil || starter.deps.Sessions == nil || starter.deps.Connections == nil || starter.token == "" || starter.userID == "" || starter.sessionID == "" || !allowedExactRedirect(starter.deps.AllowedRedirects, redirect) {
		return integrations.ProviderConnection{}, "", errGraphQLDependencies
	}
	user, userErr := starter.deps.Auth.AuthenticateSession(ctx, starter.token)
	session, sessionErr := starter.deps.Sessions.CurrentSession(ctx, starter.token)
	if userErr != nil || sessionErr != nil || user.ID != starter.userID || session.UserID != user.ID || session.ID != starter.sessionID {
		return integrations.ProviderConnection{}, "", auth.ErrInvalidSession
	}
	flow := starter.flows[input.ProviderID]
	if flow == nil {
		return integrations.ProviderConnection{}, "", errGraphQLDependencies
	}
	connection, err := starter.deps.Connections.BeginOAuth(ctx, input)
	if err != nil {
		return integrations.ProviderConnection{}, "", err
	}
	started, beginErr := flow.Begin(ctx, adapteroauth.AuthorizationRequest{ConnectionID: connection.ID, SubjectID: connection.SubjectID, Scopes: input.Scopes, SessionBinding: starter.token, ClientRedirectURI: redirect})
	if beginErr != nil || started.URL == "" {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, revokeErr := starter.deps.Connections.Revoke(rollbackCtx, connection.ID)
		cancel()
		if revokeErr != nil {
			fixedOAuthFailureReporter{logger: starter.deps.Logger}.ReportOAuthCompensationFailure(ctx)
		}
		return integrations.ProviderConnection{}, "", errGraphQLDependencies
	}
	return connection, started.URL, nil
}

func allowedExactRedirect(allowlist []string, value string) bool {
	if value == "" {
		return true
	}
	for _, allowed := range allowlist {
		if value == allowed {
			return true
		}
	}
	return false
}

type fixedOAuthFailureReporter struct{ logger *zap.Logger }

func (reporter fixedOAuthFailureReporter) ReportOAuthCompensationFailure(context.Context) {
	observability.Log(reporter.logger, "graphql.oauth_compensation_failed")
}

var _ graph.AuthTransport = GraphQLCookieTransport{}
var _ graph.CookieBoundOAuthStarter = cookieBoundOAuthStarter{}
