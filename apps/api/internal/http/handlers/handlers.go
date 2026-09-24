package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	adapteroauth "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	provideradapter "github.com/moreal/jandibat.org/apps/api/internal/adapters/provider"
	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/render"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
	"go.uber.org/zap"
)

const maxRequestBytes int64 = 1 << 20

type TimelineService interface {
	Execute(context.Context, appactivity.GetTimelineInput) (appactivity.GetTimelineOutput, error)
}

type ReadinessChecker interface {
	Check(context.Context) operations.ReadinessReport
}

type AuditRecorder interface {
	Record(context.Context, operations.AuditEvent) error
}

type AuthService interface {
	CompleteMagicLink(context.Context, string, auth.SessionMetadata) (auth.SessionGrant, error)
	AuthenticateSession(context.Context, string) (auth.User, error)
	RevokeSession(context.Context, string) error
}

type SessionService interface {
	CurrentSession(context.Context, string) (auth.Session, error)
}

type ConnectionService interface {
	BeginOAuth(context.Context, integrations.ConnectInput) (integrations.ProviderConnection, error)
	Revoke(context.Context, string) (integrations.ProviderConnection, error)
}

type CustomProviderService interface {
	Create(context.Context, integrations.CreateCustomProviderInput) (integrations.CustomProvider, error)
	Update(context.Context, integrations.UpdateCustomProviderInput) (integrations.CustomProvider, error)
	RotateIngestSecret(context.Context, string, string) error
	Get(context.Context, string) (integrations.CustomProvider, error)
	List(context.Context, string) ([]integrations.CustomProvider, error)
	Delete(context.Context, string) error
	Ingest(context.Context, integrations.IngestCustomActivitiesInput) (integrations.IngestCustomActivitiesResult, error)
}

type OAuthConnectionCompleter interface {
	CompleteOAuthConnection(context.Context, string, integrations.TokenCredentials, string, string, []string) (integrations.ProviderConnection, error)
}

// SubjectAuthorizer guards owner-only SVG reads and the OAuth callback.
type SubjectAuthorizer interface {
	OwnsSubject(context.Context, string, string) (bool, error)
}

type SubjectVisibility interface {
	AuthorizeSubjectRead(context.Context, string, string) (bool, error)
}

type SubjectResolver interface {
	ResolveSubjectID(context.Context, string) (string, error)
}

type SubjectReferenceResolver interface {
	ResolveSubjectReference(context.Context, string) (localID string, publicHandle string, err error)
}

type Dependencies struct {
	Logger            *zap.Logger
	Timeline          TimelineService
	Readiness         ReadinessChecker
	Audit             AuditRecorder
	MutationAudits    operations.MutationAuditCoordinator
	AuditSourceKey    []byte
	Auth              AuthService
	Sessions          SessionService
	Connections       ConnectionService
	OAuthConnections  OAuthConnectionCompleter
	OAuthFlows        map[string]adapteroauth.Flow
	OAuthWebURL       string
	AllowedRedirects  []string
	CustomProviders   CustomProviderService
	SubjectAuthorizer SubjectAuthorizer
	SubjectVisibility SubjectVisibility
	SubjectResolver   SubjectResolver
	AllowedOrigins    []string
	SecureCookies     bool
	TrustProxyHeaders bool
	RateLimiter       RateLimiter
	Now               func() time.Time
}

type Server struct {
	deps Dependencies
	now  func() time.Time
}

func New(deps Dependencies) *Server {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Server{deps: deps, now: now}
}

func (s *Server) Healthz(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	dependencies := map[string]string{}
	if s.deps.Readiness != nil {
		report := s.deps.Readiness.Check(r.Context())
		if !report.Ready {
			s.writeError(w, r, errUnavailable)
			return
		}
		for _, dependency := range report.Dependencies {
			dependencyStatus := "unavailable"
			if dependency.Ready {
				dependencyStatus = "ok"
			}
			dependencies[dependency.Name] = dependencyStatus
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "checkedAt": s.now().UTC(), "dependencies": dependencies})
}

type timelineQuery struct {
	From, To       *domain.Date
	Timezone       string
	Force          bool
	EnvironmentIDs []domain.EnvironmentID
}

func parseTimelineQuery(r *http.Request) (timelineQuery, error) {
	q := r.URL.Query()
	result := timelineQuery{Timezone: q.Get("timezone")}
	var err error
	if result.From, err = parseDate(q.Get("from")); err != nil {
		return result, fmt.Errorf("from: %w", err)
	}
	if result.To, err = parseDate(q.Get("to")); err != nil {
		return result, fmt.Errorf("to: %w", err)
	}
	if result.From != nil && result.To != nil && string(*result.From) > string(*result.To) {
		return result, appactivity.ErrInvalidDateRange
	}
	if result.Timezone != "" {
		if _, err := time.LoadLocation(result.Timezone); err != nil {
			return result, invalidRequest("timezone: invalid IANA timezone")
		}
	}
	if raw := q.Get("force"); raw != "" {
		result.Force, err = strconv.ParseBool(raw)
		if err != nil {
			return result, invalidRequest("force: must be true or false")
		}
	}
	if raw := q.Get("failurePolicy"); raw != "" {
		return result, invalidRequest("failurePolicy is not supported for activity reads")
	}
	if len(q["environmentId"]) > 20 {
		return result, invalidRequest("environmentId: too many values")
	}
	seenEnvironments := make(map[string]struct{}, len(q["environmentId"]))
	for _, id := range q["environmentId"] {
		if strings.TrimSpace(id) == "" {
			return result, invalidRequest("environmentId: must not be empty")
		}
		if len(id) > 128 {
			return result, invalidRequest("environmentId: must be at most 128 characters")
		}
		if _, duplicate := seenEnvironments[id]; !duplicate {
			seenEnvironments[id] = struct{}{}
			result.EnvironmentIDs = append(result.EnvironmentIDs, domain.EnvironmentID(id))
		}
	}
	return result, nil
}

func parseDate(raw string) (*domain.Date, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.DateOnly, raw)
	if err != nil || parsed.Format(time.DateOnly) != raw {
		return nil, appactivity.ErrInvalidDate
	}
	value := domain.Date(raw)
	return &value, nil
}

func (s *Server) timeline(r *http.Request) (appactivity.GetTimelineOutput, bool, error) {
	query, err := parseTimelineQuery(r)
	if err != nil {
		return appactivity.GetTimelineOutput{}, true, err
	}
	var userID string
	if query.Force {
		user, _, authErr := s.authenticate(r)
		if authErr != nil {
			return appactivity.GetTimelineOutput{}, true, authErr
		}
		userID = user.ID
		if err := s.checkOwner(r.Context(), user.ID, chi.URLParam(r, "subject")); err != nil {
			return appactivity.GetTimelineOutput{}, true, err
		}
	} else if requestHasCredentials(r) {
		user, _, authErr := s.authenticate(r)
		if authErr != nil {
			return appactivity.GetTimelineOutput{}, true, authErr
		}
		userID = user.ID
	}
	isPublic := true
	if s.deps.SubjectVisibility != nil {
		isPublic, err = s.deps.SubjectVisibility.AuthorizeSubjectRead(r.Context(), userID, chi.URLParam(r, "subject"))
		if err != nil {
			return appactivity.GetTimelineOutput{}, isPublic, err
		}
	}
	includeSubjectEnvironments := !isPublic
	if isPublic && userID != "" && s.deps.SubjectAuthorizer != nil {
		includeSubjectEnvironments, err = s.deps.SubjectAuthorizer.OwnsSubject(r.Context(), userID, chi.URLParam(r, "subject"))
		if err != nil {
			return appactivity.GetTimelineOutput{}, isPublic, err
		}
	}
	if isPublic || query.Force {
		buckets := []rateLimitBucket{{scope: rateScopePublicActivityIP, key: clientAddress(r)}}
		if userID != "" {
			buckets = append(buckets, rateLimitBucket{
				scope: rateScopePublicActivityAccount,
				key:   rateLimitIdentity("user", userID),
			})
		}
		if err := s.checkRateLimits(r, buckets...); err != nil {
			return appactivity.GetTimelineOutput{}, true, err
		}
	}
	requestedSubject := chi.URLParam(r, "subject")
	subjectID, providerSubject := requestedSubject, requestedSubject
	if resolver, ok := s.deps.SubjectResolver.(SubjectReferenceResolver); ok {
		subjectID, providerSubject, err = resolver.ResolveSubjectReference(r.Context(), requestedSubject)
		if err != nil {
			return appactivity.GetTimelineOutput{}, isPublic, err
		}
	} else if s.deps.SubjectResolver != nil {
		subjectID, err = s.deps.SubjectResolver.ResolveSubjectID(r.Context(), requestedSubject)
		if err != nil {
			return appactivity.GetTimelineOutput{}, isPublic, err
		}
	}
	if s.deps.Timeline == nil {
		return appactivity.GetTimelineOutput{}, isPublic, fmt.Errorf("%w: timeline", errUnavailable)
	}
	output, err := s.deps.Timeline.Execute(r.Context(), appactivity.GetTimelineInput{
		Subject: domain.SubjectID(subjectID), ProviderSubject: domain.SubjectID(providerSubject), Timezone: query.Timezone,
		From: query.From, To: query.To, Force: query.Force, FailurePolicy: domain.FetchFailureKeepStale,
		EnvironmentIDs: query.EnvironmentIDs, IncludeSubjectEnvironments: includeSubjectEnvironments,
	})
	return output, isPublic && !includeSubjectEnvironments, err
}

func (s *Server) RenderHeatmap(w http.ResponseWriter, r *http.Request) {
	output, isPublic, err := s.timeline(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	theme := q.Get("theme")
	switch theme {
	case "", "system", "light", "github-light":
		theme = "light"
	case "dark", "github-dark":
		theme = "dark"
	default:
		s.writeError(w, r, render.ErrUnknownTheme)
		return
	}
	cellSize := 0
	if raw := q.Get("cellSize"); raw != "" {
		cellSize, err = strconv.Atoi(raw)
		if err != nil || cellSize < 6 || cellSize > 32 {
			s.writeError(w, r, invalidRequest("cellSize must be between 6 and 32"))
			return
		}
	}
	var showLegend *bool
	if raw := q.Get("showLegend"); raw != "" {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			s.writeError(w, r, invalidRequest("showLegend must be true or false"))
			return
		}
		showLegend = &value
	}
	weekStart := q.Get("weekStart")
	if weekStart != "" && weekStart != "sunday" && weekStart != "monday" {
		s.writeError(w, r, invalidRequest("weekStart is invalid"))
		return
	}
	svg, err := render.RenderHeatmapSVG(output.Timeline, render.HeatmapOptions{From: &output.From, To: &output.To, Theme: theme, CellSize: cellSize, ShowLegend: showLegend, WeekStart: weekStart})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	sum := sha256.Sum256(svg)
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	if isPublic {
		w.Header().Set("Cache-Control", "public, max-age=300")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(svg)
}

func (s *Server) ConsumeMagicLink(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil {
		s.unavailable(w, r, "authentication")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopeMagicConsumeIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopeMagicConsumeToken, key: rateLimitIdentity("magic-token", input.Token)},
	) {
		return
	}
	grant, err := s.deps.Auth.CompleteMagicLink(r.Context(), input.Token, sessionMetadata(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, err := s.deps.Auth.AuthenticateSession(r.Context(), grant.Token)
	if err != nil {
		if revokeErr := s.deps.Auth.RevokeSession(r.Context(), grant.Token); revokeErr != nil {
			s.writeError(w, r, errUnavailable)
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, grant.Token, grant.ExpiresAt)
	setAuditActor(r, operations.AuditActor{Type: operations.AuditActorUser, ID: user.ID})
	writeJSON(w, http.StatusOK, authResult(user, grant, s.now().UTC()))
}

func exactlyAllowed(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s *Server) ProviderOAuthCallback(w http.ResponseWriter, r *http.Request) {
	providerID := chi.URLParam(r, "provider")
	flow := s.deps.OAuthFlows[providerID]
	if flow == nil || s.deps.OAuthConnections == nil {
		s.writeError(w, r, integrations.ErrInvalidProvider)
		return
	}
	query := r.URL.Query()
	user, sessionBinding, authErr := s.authenticateSessionCookie(r)
	if authErr != nil {
		s.writeError(w, r, adapteroauth.ErrInvalidState)
		return
	}
	state := query.Get("state")
	code := query.Get("code")
	providerError := query.Get("error")
	providerErrorDescription := query.Get("error_description")
	if len(state) < 32 || len(state) > 1024 || len(code) > 4096 || len(providerError) > 256 || len(providerErrorDescription) > 1024 {
		s.writeError(w, r, invalidRequest("OAuth callback query exceeds contract limits"))
		return
	}
	result, err := flow.Complete(r.Context(), adapteroauth.CallbackRequest{State: state, Code: code, Error: providerError, SessionBinding: sessionBinding})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if result.ProviderID != providerID {
		s.writeError(w, r, adapteroauth.ErrInvalidState)
		return
	}
	if err := s.checkOwner(r.Context(), user.ID, result.SubjectID); err != nil {
		revokeFreshOAuthCredential(r.Context(), flow, result.Credentials.AccessToken)
		s.writeError(w, r, adapteroauth.ErrInvalidState)
		return
	}
	connection, err := s.deps.OAuthConnections.CompleteOAuthConnection(r.Context(), result.ConnectionID, result.Credentials, result.Identity.ExternalAccountID, result.Identity.Username, result.Scopes)
	if err != nil {
		revokeFreshOAuthCredential(r.Context(), flow, result.Credentials.AccessToken)
		s.writeError(w, r, err)
		return
	}
	if connection.PrivateDataEnabled && result.Credentials.AccessToken != "" {
		token := result.Credentials.AccessToken
		operations.OnMutationRollback(r.Context(), func() {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			revokeFreshOAuthCredential(rollbackCtx, flow, token)
			cancel()
		})
	}
	if !connection.PrivateDataEnabled {
		// OAuth access was needed only to map the provider identity. Public-only
		// connections retain no local credential; revoke the fresh provider token
		// best-effort so opt-out also minimizes external secret lifetime.
		revokeFreshOAuthCredential(r.Context(), flow, result.Credentials.AccessToken)
	}
	setAuditTarget(r, operations.AuditTarget{Type: "provider_connection", ID: connection.ID})
	destination := result.ClientRedirectURI
	if destination == "" {
		destination = strings.TrimRight(s.deps.OAuthWebURL, "/") + "/settings/providers"
	}
	redirectURL, parseErr := url.Parse(destination)
	if parseErr != nil || !redirectURL.IsAbs() || !exactlyAllowed(destination, s.deps.AllowedRedirects) {
		s.writeError(w, r, errUnavailable)
		return
	}
	values := redirectURL.Query()
	values.Set("provider", providerID)
	values.Set("connectionId", connection.ID)
	values.Set("status", "connected")
	redirectURL.RawQuery = values.Encode()
	http.Redirect(w, r, redirectURL.String(), http.StatusSeeOther)
}

func revokeFreshOAuthCredential(ctx context.Context, flow adapteroauth.Flow, accessToken string) {
	revoker, ok := flow.(adapteroauth.TokenRevoker)
	if !ok || accessToken == "" {
		return
	}
	token := []byte(accessToken)
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = revoker.RevokeToken(revokeCtx, token)
	cancel()
	clear(token)
}

func (s *Server) IngestCustomActivities(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopeCustomIngestIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopeCustomIngestProvider, key: rateLimitIdentity("custom-provider", chi.URLParam(r, "customProviderId"))},
	) {
		return
	}
	if s.deps.CustomProviders == nil {
		s.unavailable(w, r, "custom providers")
		return
	}
	key := r.Header.Get("X-Jandibat-Provider-Key")
	if key == "" {
		s.writeError(w, r, integrations.ErrUnauthorized)
		return
	}
	var input struct {
		SchemaVersion string `json:"schemaVersion"`
		Events        []struct {
			EventID string `json:"eventId"`
			Date    string `json:"date"`
			Action  string `json:"action"`
			Metric  struct {
				Name  string `json:"name"`
				Value int    `json:"value"`
			} `json:"metric"`
			Metadata   map[string]string `json:"metadata,omitempty"`
			ObservedAt *time.Time        `json:"observedAt,omitempty"`
		} `json:"events"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if input.SchemaVersion != "1.0" {
		s.writeError(w, r, invalidRequest("schemaVersion must be 1.0"))
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		s.writeError(w, r, integrations.ErrInvalidIdempotencyKey)
		return
	}
	activities := make([]integrations.CustomActivity, len(input.Events))
	for index, event := range input.Events {
		activities[index] = integrations.CustomActivity{ExternalID: event.EventID, Date: event.Date, Action: event.Action, Metric: event.Metric.Name, Value: event.Metric.Value, Metadata: event.Metadata, ObservedAt: event.ObservedAt}
	}
	result, err := s.deps.CustomProviders.Ingest(r.Context(), integrations.IngestCustomActivitiesInput{ProviderID: chi.URLParam(r, "customProviderId"), IngestSecret: key, IdempotencyKey: idempotencyKey, Activities: activities})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	setAuditActor(r, operations.AuditActor{Type: operations.AuditActorCustomProvider, ID: chi.URLParam(r, "customProviderId")})
	rejections := make([]map[string]string, 0, len(result.Rejections))
	for _, rejection := range result.Rejections {
		rejections = append(rejections, map[string]string{"eventId": rejection.EventID, "code": rejection.Code, "detail": rejection.Detail})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": result.Accepted, "duplicates": result.Duplicate, "rejected": result.Rejected, "rejections": rejections})
}

func (s *Server) checkOwner(ctx context.Context, userID, subject string) error {
	if s.deps.SubjectAuthorizer == nil {
		return errAuthorizerUnavailable
	}
	ok, err := s.deps.SubjectAuthorizer.OwnsSubject(ctx, userID, subject)
	if err != nil {
		return err
	}
	if !ok {
		return errForbidden
	}
	return nil
}

func (s *Server) authenticate(r *http.Request) (auth.User, string, error) {
	if s.deps.Auth == nil {
		return auth.User{}, "", errUnavailable
	}
	token, err := sessionToken(r)
	if err != nil {
		return auth.User{}, "", err
	}
	user, err := s.deps.Auth.AuthenticateSession(r.Context(), token)
	if err != nil {
		return auth.User{}, "", err
	}
	setAuditActor(r, operations.AuditActor{Type: operations.AuditActorUser, ID: user.ID})
	return user, token, nil
}

type auditActorContextKey struct{}

// auditActorHolder is installed by the outer audit middleware before any
// middleware derives a child request. Keeping the holder shared lets an
// authenticated handler publish its actor to the outer outcome recorder even
// when timeout middleware has copied the request with a derived context.
// Each stored actor is immutable, so concurrent readers never race with the
// handler that authenticates the request.
type auditActorHolder struct {
	actor  atomic.Pointer[operations.AuditActor]
	target atomic.Pointer[operations.AuditTarget]
}

// WithAuditActorHolder prepares a request context for actor propagation across
// middleware request copies. Intent records are deliberately written before
// authentication and therefore remain anonymous; only the correlated outcome
// observes an actor published by a successful authentication boundary.
func WithAuditActorHolder(ctx context.Context) context.Context {
	if _, ok := ctx.Value(auditActorContextKey{}).(*auditActorHolder); ok {
		return ctx
	}
	return context.WithValue(ctx, auditActorContextKey{}, &auditActorHolder{})
}

func setAuditActor(r *http.Request, actor operations.AuditActor) {
	holder, ok := r.Context().Value(auditActorContextKey{}).(*auditActorHolder)
	if !ok {
		holder = &auditActorHolder{}
		*r = *r.WithContext(context.WithValue(r.Context(), auditActorContextKey{}, holder))
	}
	actorCopy := actor
	holder.actor.Store(&actorCopy)
}

// AuditActorFromContext exposes the authenticated identity to transport
// middleware without storing credentials or session tokens in the context.
func AuditActorFromContext(ctx context.Context) (operations.AuditActor, bool) {
	holder, ok := ctx.Value(auditActorContextKey{}).(*auditActorHolder)
	if !ok {
		return operations.AuditActor{}, false
	}
	actor := holder.actor.Load()
	if actor == nil {
		return operations.AuditActor{}, false
	}
	return *actor, true
}

// setAuditTarget publishes the canonical durable resource identifier only
// after a handler has resolved or created that resource. This keeps route
// aliases (subject handles and provider slugs) out of the success outcome.
func setAuditTarget(r *http.Request, target operations.AuditTarget) {
	holder, ok := r.Context().Value(auditActorContextKey{}).(*auditActorHolder)
	if !ok {
		holder = &auditActorHolder{}
		*r = *r.WithContext(context.WithValue(r.Context(), auditActorContextKey{}, holder))
	}
	targetCopy := target
	holder.target.Store(&targetCopy)
}

// AuditTargetFromContext exposes a canonical resource identity to the audit
// middleware. Only handlers that have a concrete durable target publish one;
// all other outcomes retain the route-derived target type fallback.
func AuditTargetFromContext(ctx context.Context) (operations.AuditTarget, bool) {
	holder, ok := ctx.Value(auditActorContextKey{}).(*auditActorHolder)
	if !ok {
		return operations.AuditTarget{}, false
	}
	target := holder.target.Load()
	if target == nil {
		return operations.AuditTarget{}, false
	}
	return *target, true
}

func sessionToken(r *http.Request) (string, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header != "" {
		parts := strings.Fields(header)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			return "", auth.ErrInvalidSession
		}
		return parts[1], nil
	}
	cookie, err := r.Cookie("jandibat_session")
	if err != nil || cookie.Value == "" {
		return "", auth.ErrInvalidSession
	}
	return cookie.Value, nil
}

func sessionCookieToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie("jandibat_session")
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", auth.ErrInvalidSession
	}
	return cookie.Value, nil
}

func (s *Server) authenticateSessionCookie(r *http.Request) (auth.User, string, error) {
	if s.deps.Auth == nil {
		return auth.User{}, "", errUnavailable
	}
	token, err := sessionCookieToken(r)
	if err != nil {
		return auth.User{}, "", err
	}
	user, err := s.deps.Auth.AuthenticateSession(r.Context(), token)
	if err != nil {
		return auth.User{}, "", err
	}
	setAuditActor(r, operations.AuditActor{Type: operations.AuditActorUser, ID: user.ID})
	return user, token, nil
}

func requestHasCredentials(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	cookie, err := r.Cookie("jandibat_session")
	return err == nil && cookie.Value != ""
}

func sessionMetadata(r *http.Request) auth.SessionMetadata {
	return auth.SessionMetadata{IPAddress: canonicalIPAddress(r.RemoteAddr), UserAgent: r.UserAgent()}
}

func canonicalIPAddress(remoteAddress string) string {
	value := strings.TrimSpace(remoteAddress)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return address.Unmap().String()
}
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: "jandibat_session", Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: s.deps.SecureCookies, SameSite: http.SameSiteLaxMode})
}

func userResponse(user auth.User) map[string]any {
	return map[string]any{"id": user.ID, "primaryEmail": user.PrimaryEmail, "emailVerifiedAt": user.EmailVerifiedAt, "status": user.Status, "createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt}
}
func authResult(user auth.User, grant auth.SessionGrant, created time.Time) map[string]any {
	return map[string]any{"user": userResponse(user), "session": map[string]any{"id": grant.SessionID, "userId": user.ID, "current": true, "createdAt": created, "expiresAt": grant.ExpiresAt, "lastSeenAt": created, "revokedAt": nil, "userAgent": nil, "ipAddress": nil}}
}

var errUnavailable = errors.New("service unavailable")
var errAuthorizerUnavailable = errors.New("subject authorizer unavailable")
var errForbidden = errors.New("forbidden")
var errRateLimited = errors.New("rate limit exceeded")
var errInvalidRequest = errors.New("invalid request")
var errPayloadTooLarge = errors.New("request payload is too large")

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidRequest, fmt.Sprintf(format, args...))
}

type problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"requestId"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, title, code := http.StatusInternalServerError, "Internal Server Error", "internal_error"
	switch {
	case errors.Is(err, auth.ErrMagicLinkReauthenticationRequired):
		status, title, code = http.StatusUnauthorized, "Reauthentication Required", "magic_link_reauthentication_required"
	case errors.Is(err, auth.ErrInvalidSession), errors.Is(err, auth.ErrInvalidMagicLink), errors.Is(err, auth.ErrInvalidCeremony), errors.Is(err, auth.ErrPasskeyVerification), errors.Is(err, auth.ErrInvalidSignCount), errors.Is(err, integrations.ErrUnauthorized):
		status, title, code = http.StatusUnauthorized, "Unauthorized", "unauthorized"
	case errors.Is(err, errForbidden), errors.Is(err, auth.ErrUserDisabled):
		status, title, code = http.StatusForbidden, "Forbidden", "forbidden"
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, integrations.ErrNotFound):
		status, title, code = http.StatusNotFound, "Not Found", "not_found"
	case errors.Is(err, subjects.ErrNotFound):
		status, title, code = http.StatusNotFound, "Not Found", "not_found"
	case errors.Is(err, subjects.ErrUnauthenticated):
		status, title, code = http.StatusUnauthorized, "Unauthorized", "unauthorized"
	case errors.Is(err, subjects.ErrForbidden):
		status, title, code = http.StatusForbidden, "Forbidden", "forbidden"
	case errors.Is(err, auth.ErrConflict), errors.Is(err, auth.ErrCredentialExists), errors.Is(err, integrations.ErrConflict), errors.Is(err, integrations.ErrDuplicateProviderSlug):
		status, title, code = http.StatusConflict, "Conflict", "conflict"
	case errors.Is(err, operations.ErrLegalHoldActive):
		status, title, code = http.StatusConflict, "Conflict", "legal_hold_active"
	case errors.Is(err, operations.ErrInvalidDeletionRequest):
		status, title, code = http.StatusConflict, "Conflict", "deletion_request_conflict"
	case errors.Is(err, integrations.ErrSyncAlreadyRunning), errors.Is(err, integrations.ErrProviderDisabled), errors.Is(err, integrations.ErrConnectionNotSyncable):
		status, title, code = http.StatusConflict, "Conflict", "conflict"
	case errors.Is(err, subjects.ErrConflict):
		status, title, code = http.StatusConflict, "Conflict", "conflict"
	case errors.Is(err, integrations.ErrTooManyActivities), errors.Is(err, errPayloadTooLarge):
		status, title, code = http.StatusRequestEntityTooLarge, "Content Too Large", "payload_too_large"
	case errors.Is(err, errUnavailable), errors.Is(err, errAuthorizerUnavailable):
		status, title, code = http.StatusServiceUnavailable, "Service Unavailable", "service_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		status, title, code = http.StatusServiceUnavailable, "Service Unavailable", "service_unavailable"
	case errors.Is(err, adapteroauth.ErrInvalidState), errors.Is(err, adapteroauth.ErrAuthorizationDenied):
		status, title, code = http.StatusUnauthorized, "Unauthorized", "oauth_invalid_state"
	case errors.Is(err, adapteroauth.ErrTokenExchange), errors.Is(err, adapteroauth.ErrIdentityLookup):
		status, title, code = http.StatusBadGateway, "Bad Gateway", "provider_failure"
	case errors.Is(err, errRateLimited), errors.Is(err, provideradapter.ErrRateLimited), errors.Is(err, appactivity.ErrProviderSaturated):
		status, title, code = http.StatusTooManyRequests, "Too Many Requests", "rate_limited"
	case errors.Is(err, appactivity.ErrProviderUnavailable):
		status, title, code = http.StatusBadGateway, "Bad Gateway", "provider_failure"
	case isInvalidRequestError(err):
		status, title, code = http.StatusBadRequest, "Bad Request", "invalid_request"
	}
	detail := err.Error()
	if status >= 500 {
		detail = "The service could not complete the request."
	} else if code == "magic_link_reauthentication_required" {
		detail = "Magic-link reauthentication is required before continuing."
	} else if status == http.StatusUnauthorized {
		detail = "Authentication is required or invalid."
	}
	if status == http.StatusServiceUnavailable && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", "5")
	}
	if status == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
		var providerError *provideradapter.HTTPError
		seconds := 1
		var limitedError *rateLimitError
		if errors.As(err, &limitedError) && limitedError.retryAfter > 0 {
			seconds = int(limitedError.retryAfter.Round(time.Second) / time.Second)
		} else if errors.As(err, &providerError) && providerError.RetryAfter > 0 {
			seconds = int(providerError.RetryAfter.Round(time.Second) / time.Second)
		}
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, status, problem{Type: "https://jandibat.org/problems/" + code, Title: title, Status: status, Code: code, Detail: detail, Instance: r.URL.Path, RequestID: middleware.GetReqID(r.Context())})
}

func isInvalidRequestError(err error) bool {
	invalidErrors := []error{
		errInvalidRequest, auth.ErrInvalidInput, auth.ErrInvalidSignCount,
		subjects.ErrInvalidInput, appactivity.ErrInvalidDate, appactivity.ErrInvalidDateRange,
		appactivity.ErrDateRangeTooLarge, domain.ErrEmptySubject, domain.ErrEmptyTimezone,
		domain.ErrInvalidFetchFailurePolicy, render.ErrInvalidRenderDate,
		render.ErrInvalidRenderRange, render.ErrEmptyRenderRange, render.ErrUnknownTheme,
		render.ErrInvalidCellSize, render.ErrInvalidGap,
		integrations.ErrInvalidProvider, integrations.ErrInvalidIdentifier, integrations.ErrUnsupportedAuthMethod,
		integrations.ErrInvalidConnectionStatus, integrations.ErrEmptySubjectID,
		integrations.ErrEmptyEnvironmentID, integrations.ErrEmptyConnectionID,
		integrations.ErrEmptyAccessToken, integrations.ErrAccessTokenTooLong,
		integrations.ErrTooManyScopes, integrations.ErrDuplicateScope, integrations.ErrInvalidScope,
		integrations.ErrPrivateDataUnsupported,
		integrations.ErrEmptyProviderName,
		integrations.ErrEmptyProviderSlug, integrations.ErrInvalidProviderSlug,
		integrations.ErrProviderNameTooLong, integrations.ErrProviderDescriptionTooLong,
		integrations.ErrTooManyAllowedActions, integrations.ErrInvalidAllowedAction,
		integrations.ErrDuplicateAllowedAction, integrations.ErrTooManyAllowedMetrics,
		integrations.ErrInvalidAllowedMetric, integrations.ErrDuplicateAllowedMetric,
		integrations.ErrEmptyIngestSecret, integrations.ErrIngestSecretTooShort,
		integrations.ErrEmptyActivities, integrations.ErrEmptyExternalID,
		integrations.ErrExternalIDTooLong, integrations.ErrEmptyActivityDate,
		integrations.ErrInvalidActivityDate, integrations.ErrActivityDateOutOfRange, integrations.ErrEmptyAction,
		integrations.ErrActionTooLong, integrations.ErrActionNotAllowed,
		integrations.ErrEmptyMetric, integrations.ErrMetricTooLong,
		integrations.ErrMetricNotAllowed, integrations.ErrNegativeMetricValue,
		integrations.ErrTooManyMetadataProperties, integrations.ErrMetadataValueTooLong,
		integrations.ErrInvalidIdempotencyKey,
		integrations.ErrInvalidSyncDate, integrations.ErrInvalidSyncDateRange,
		adapteroauth.ErrInvalidRequest,
	}
	for _, candidate := range invalidErrors {
		if errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

type rateLimitBucket struct {
	scope string
	key   string
}

func (s *Server) allowRequests(w http.ResponseWriter, r *http.Request, buckets ...rateLimitBucket) bool {
	err := s.checkRateLimits(r, buckets...)
	if err == nil {
		return true
	}
	s.writeError(w, r, err)
	return false
}

// checkRateLimits evaluates every applicable dimension. It deliberately does
// not stop after the first denial: otherwise a caller could keep an account
// bucket fresh by rotating IPs (or keep an IP bucket fresh by rotating
// accounts). A request is admitted only when every bucket admits it.
func (s *Server) checkRateLimits(r *http.Request, buckets ...rateLimitBucket) error {
	var retryAfter time.Duration
	var backendErrors []error
	denied := false
	for _, bucket := range buckets {
		err := s.checkRateLimit(r, bucket.scope, bucket.key)
		if err == nil {
			continue
		}
		var limited *rateLimitError
		if errors.As(err, &limited) {
			denied = true
			if limited.retryAfter > retryAfter {
				retryAfter = limited.retryAfter
			}
			continue
		}
		backendErrors = append(backendErrors, err)
	}
	if len(backendErrors) > 0 {
		return errors.Join(backendErrors...)
	}
	if denied {
		return &rateLimitError{retryAfter: retryAfter}
	}
	return nil
}

type rateLimitError struct {
	retryAfter time.Duration
}

func (err *rateLimitError) Error() string { return errRateLimited.Error() }
func (err *rateLimitError) Unwrap() error { return errRateLimited }

func (s *Server) checkRateLimit(r *http.Request, scope, key string) error {
	if s.deps.RateLimiter == nil {
		return nil
	}
	allowed, retryAfter, err := s.deps.RateLimiter.Allow(r.Context(), scope, key)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	return &rateLimitError{retryAfter: retryAfter}
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
func (s *Server) unavailable(w http.ResponseWriter, r *http.Request, name string) {
	s.writeError(w, r, fmt.Errorf("%w: %s", errUnavailable, name))
}
func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/merge-patch+json") {
		return invalidRequest("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errPayloadTooLarge
		}
		return invalidRequest("invalid JSON body")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errPayloadTooLarge
		}
		return invalidRequest("JSON body must contain one value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSONBytes(w, status, body)
}
func writeJSONBytes(w http.ResponseWriter, status int, body []byte) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// Compatibility entry points retained for packages that used the original
// stateless handlers directly.
func Healthz(w http.ResponseWriter, r *http.Request) { New(Dependencies{}).Healthz(w, r) }
