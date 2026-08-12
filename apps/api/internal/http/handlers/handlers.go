package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/render"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
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

type CatalogService interface {
	List(context.Context, string) ([]integrations.ProviderCatalogItem, error)
}

type AuthService interface {
	RequestMagicLink(context.Context, string, string) error
	CompleteMagicLink(context.Context, string, auth.SessionMetadata) (auth.SessionGrant, error)
	AuthenticateSession(context.Context, string) (auth.User, error)
	RevokeSession(context.Context, string) error
	BeginPasskeyRegistration(context.Context, string) (auth.PasskeyOptions, error)
	CompletePasskeyRegistration(context.Context, string, json.RawMessage, string) (auth.PasskeyCredential, error)
	BeginPasskeyLogin(context.Context, string) (auth.PasskeyOptions, error)
	CompletePasskeyLogin(context.Context, string, []byte, json.RawMessage, auth.SessionMetadata) (auth.SessionGrant, error)
}

type SessionService interface {
	CurrentSession(context.Context, string) (auth.Session, error)
	ListSessions(context.Context, string) ([]auth.Session, error)
	RevokeOtherSessions(context.Context, string, string) error
	RevokeSessionByID(context.Context, string, string) error
}

type PasskeyRegistrationOwnerService interface {
	CompletePasskeyRegistrationForUser(context.Context, string, string, json.RawMessage, string) (auth.PasskeyCredential, error)
}

type ConnectionService interface {
	ConnectToken(context.Context, integrations.ConnectTokenInput) (integrations.ProviderConnection, error)
	ConnectPublic(context.Context, integrations.ConnectInput) (integrations.ProviderConnection, error)
	BeginOAuth(context.Context, integrations.ConnectInput) (integrations.ProviderConnection, error)
	Update(context.Context, integrations.UpdateConnectionInput) (integrations.ProviderConnection, error)
	Revoke(context.Context, string) (integrations.ProviderConnection, error)
	Get(context.Context, string) (integrations.ProviderConnection, error)
	List(context.Context, string) ([]integrations.ProviderConnection, error)
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

type SyncService interface {
	EnqueueManualSync(context.Context, integrations.ManualSyncInput) (integrations.SyncJob, error)
	GetJob(context.Context, string) (integrations.SyncJob, error)
}

type OAuthConnectionCompleter interface {
	CompleteOAuthConnection(context.Context, string, integrations.TokenCredentials, string, string, []string) (integrations.ProviderConnection, error)
}

// SubjectAuthorizer is deliberately a narrow boundary because the subject
// package is independently owned. Every owner-only nested resource passes
// through it before its service is called.
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

type SubjectService interface {
	ProvisionUser(context.Context, subjects.User) error
	GetUserSettings(context.Context, string) (subjects.UserSettings, error)
	UpdateUserSettings(context.Context, string, subjects.UpdateUserSettingsInput) (subjects.UserSettings, error)
	CreateSubject(context.Context, string, subjects.CreateSubjectInput) (subjects.Subject, error)
	ListSubjects(context.Context, string, subjects.ListSubjectsInput) (subjects.SubjectList, error)
	GetSubject(context.Context, string, string) (subjects.Subject, error)
	UpdateSubject(context.Context, string, string, subjects.UpdateSubjectInput) (subjects.Subject, error)
	GetSubjectSettings(context.Context, string, string) (subjects.SubjectSettings, error)
	UpdateSubjectSettings(context.Context, string, string, subjects.UpdateSubjectSettingsInput) (subjects.SubjectSettings, error)
}

type SubjectDeletionAuthorizer interface {
	AuthorizeSubjectDeletion(context.Context, string, string) (subjects.Subject, error)
}

type SubjectDeletionWorkflow interface {
	Request(context.Context, string, operations.DeletionTargetType, string) (operations.DeletionRequest, error)
}

type Dependencies struct {
	Timeline          TimelineService
	Readiness         ReadinessChecker
	Audit             AuditRecorder
	MutationAudits    operations.MutationAuditCoordinator
	AuditSourceKey    []byte
	Catalog           CatalogService
	Auth              AuthService
	Sessions          SessionService
	Connections       ConnectionService
	OAuthConnections  OAuthConnectionCompleter
	OAuthFlows        map[string]adapteroauth.Flow
	OAuthWebURL       string
	AllowedRedirects  []string
	CustomProviders   CustomProviderService
	Sync              SyncService
	SubjectAuthorizer SubjectAuthorizer
	SubjectVisibility SubjectVisibility
	SubjectResolver   SubjectResolver
	Subjects          SubjectService
	SubjectDeletions  SubjectDeletionWorkflow
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

func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	var items []integrations.ProviderCatalogItem
	var err error
	if s.deps.Catalog != nil {
		items, err = s.deps.Catalog.List(r.Context(), "")
	} else {
		for _, id := range []string{"github", "gitlab", "codeberg"} {
			item, _ := integrations.BuiltInProvider(id)
			items = append(items, item)
		}
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	providers := make([]map[string]any, 0, len(items))
	for _, item := range items {
		methods := make([]string, 0, 3)
		if item.SupportsOAuth {
			methods = append(methods, "oauth2")
		}
		if item.SupportsToken {
			methods = append(methods, "token")
		}
		methods = append(methods, "none")
		providers = append(providers, map[string]any{
			"id": item.ID, "name": item.Name, "category": item.Category,
			"authMethods": methods, "supportsPrivateData": item.SupportsPrivateData,
			"supportsScheduledSync": item.Kind != integrations.ProviderCustom,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
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

func (s *Server) GetActivities(w http.ResponseWriter, r *http.Request) {
	output, isPublic, err := s.timeline(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	payload := timelineResponse(output, s.now().UTC())
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256(body)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	if isPublic {
		w.Header().Set("Cache-Control", "public, max-age=300")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	writeJSONBytes(w, http.StatusOK, body)
}

func timelineResponse(output appactivity.GetTimelineOutput, generatedAt time.Time) map[string]any {
	environments := make([]map[string]any, 0, len(output.Timeline.Environments))
	for _, environment := range output.Timeline.Environments {
		var owner any
		if environment.OwnerSubject != nil {
			owner = string(*environment.OwnerSubject)
		}
		metadata := environment.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		environments = append(environments, map[string]any{"id": environment.ID, "key": environment.Key, "name": environment.Name, "scope": environment.Scope, "ownerSubject": owner, "metadata": metadata})
	}
	days := make([]map[string]any, 0, len(output.Timeline.Days))
	for _, day := range output.Timeline.Days {
		entries := make([]map[string]any, 0, len(day.Entries))
		for _, entry := range day.Entries {
			metadata := entry.Metadata
			if metadata == nil {
				metadata = map[string]string{}
			}
			entries = append(entries, map[string]any{"environmentId": entry.EnvironmentID, "action": entry.Action, "metric": map[string]any{"name": entry.Metric.Name, "value": entry.Metric.Value}, "metadata": metadata})
		}
		days = append(days, map[string]any{"date": day.Date, "count": day.Count, "level": day.Level, "entries": entries})
	}
	return map[string]any{"subject": output.Timeline.Subject, "timezone": output.Timeline.Timezone, "from": output.From, "to": output.To, "generatedAt": generatedAt, "stale": output.Stale, "environments": environments, "days": days}
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

func (s *Server) RequestMagicLink(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil {
		s.unavailable(w, r, "authentication")
		return
	}
	var input struct {
		Email       string `json:"email"`
		RedirectURI string `json:"redirectUri,omitempty"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(input.RedirectURI) > 2048 || (input.RedirectURI != "" && !exactlyAllowed(input.RedirectURI, s.deps.AllowedRedirects)) {
		s.writeError(w, r, invalidRequest("redirectUri is not allowed"))
		return
	}
	addressHash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(input.Email))))
	clientKey := clientAddress(r)
	accountKey := hex.EncodeToString(addressHash[:])
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopeMagicLinkIP, key: clientKey},
		rateLimitBucket{scope: rateScopeMagicLinkAccount, key: accountKey},
		rateLimitBucket{scope: rateScopeMagicLinkIPDaily, key: clientKey},
		rateLimitBucket{scope: rateScopeMagicLinkAccountDaily, key: accountKey},
	) {
		return
	}
	if err := s.deps.Auth.RequestMagicLink(r.Context(), input.Email, input.RedirectURI); err != nil {
		// A provider can reject individual recipients synchronously. Exposing
		// that distinction would turn this endpoint into a mailbox oracle. Keep
		// an internal, correlation-only signal and return the same public result.
		observability.Logf("auth.magic_link_delivery_failed", "request_id=%s", middleware.GetReqID(r.Context()))
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
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

func (s *Server) BeginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopePasskeyIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopePasskeyAccount, key: rateLimitIdentity("user", user.ID)},
	) {
		return
	}
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Label string `json:"label,omitempty"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	options, err := s.deps.Auth.BeginPasskeyRegistration(r.Context(), user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, optionsResponse(options))
}

func (s *Server) FinishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var input struct {
		CeremonyID string          `json:"ceremonyId"`
		Label      string          `json:"label,omitempty"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopePasskeyIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopePasskeyAccount, key: rateLimitIdentity("user", user.ID)},
		rateLimitBucket{scope: rateScopePasskeyCeremony, key: rateLimitIdentity("passkey-ceremony", input.CeremonyID)},
	) {
		return
	}
	if input.Label != "" && (len(input.Label) > 100 || strings.TrimSpace(input.Label) == "") {
		s.writeError(w, r, invalidRequest("label exceeds contract limits"))
		return
	}
	if _, err := validateWebAuthnCredential(input.Credential); err != nil {
		s.writeError(w, r, err)
		return
	}
	var credential auth.PasskeyCredential
	if ownerService, ok := s.deps.Auth.(PasskeyRegistrationOwnerService); ok {
		credential, err = ownerService.CompletePasskeyRegistrationForUser(r.Context(), user.ID, input.CeremonyID, input.Credential, input.Label)
	} else {
		credential, err = s.deps.Auth.CompletePasskeyRegistration(r.Context(), input.CeremonyID, input.Credential, input.Label)
	}
	if err != nil {
		markPasskeyFailureCommit(r.Context(), err)
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, passkeyResponse(credential))
}

func (s *Server) BeginPasskeySignIn(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil {
		s.unavailable(w, r, "authentication")
		return
	}
	var input struct {
		Email string `json:"email,omitempty"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	buckets := []rateLimitBucket{{scope: rateScopePasskeyIP, key: clientAddress(r)}}
	if email := strings.ToLower(strings.TrimSpace(input.Email)); email != "" {
		buckets = append(buckets, rateLimitBucket{scope: rateScopePasskeyEmail, key: rateLimitIdentity("email", email)})
	}
	if !s.allowRequests(w, r, buckets...) {
		return
	}
	// Treat the optional email as an advisory UX hint and always use
	// discoverable credentials. This preserves the contract without exposing
	// whether an account or credential exists through allowCredentials.
	options, err := s.deps.Auth.BeginPasskeyLogin(r.Context(), "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, optionsResponse(options))
}

func exactlyAllowed(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s *Server) FinishPasskeySignIn(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil {
		s.unavailable(w, r, "authentication")
		return
	}
	var input struct {
		CeremonyID string          `json:"ceremonyId"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopePasskeyIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopePasskeyCeremony, key: rateLimitIdentity("passkey-ceremony", input.CeremonyID)},
	) {
		return
	}
	credentialID, err := validateWebAuthnCredential(input.Credential)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	grant, err := s.deps.Auth.CompletePasskeyLogin(r.Context(), input.CeremonyID, credentialID, input.Credential, sessionMetadata(r))
	if err != nil {
		markPasskeyFailureCommit(r.Context(), err)
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

// markPasskeyFailureCommit opts in only the deliberate, post-consumption
// security rejection outcomes. The middleware also requires an active shared
// transaction, so malformed/replayed ceremonies that changed no durable state
// continue down the ordinary rollback path. Persistence failures and
// credential conflicts are intentionally excluded.
func markPasskeyFailureCommit(ctx context.Context, err error) {
	if errors.Is(err, auth.ErrInvalidCeremony) ||
		errors.Is(err, auth.ErrPasskeyVerification) ||
		errors.Is(err, auth.ErrInvalidSignCount) ||
		errors.Is(err, auth.ErrMagicLinkReauthenticationRequired) ||
		errors.Is(err, auth.ErrUserDisabled) {
		operations.MarkMutationFailureCommit(ctx)
	}
}

func validateWebAuthnCredential(raw json.RawMessage) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return nil, invalidRequest("credential must be a JSON object")
	}
	allowed := map[string]bool{
		"id": true, "rawId": true, "type": true, "authenticatorAttachment": true,
		"response": true, "clientExtensionResults": true,
	}
	for key := range envelope {
		if !allowed[key] {
			return nil, invalidRequest("credential contains unknown field %q", key)
		}
	}
	readString := func(key string, minimum, maximum int) (string, error) {
		value, ok := envelope[key]
		if !ok {
			return "", invalidRequest("credential.%s is required", key)
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil || len(decoded) < minimum || len(decoded) > maximum {
			return "", invalidRequest("credential.%s exceeds contract limits", key)
		}
		return decoded, nil
	}
	if _, err := readString("id", 1, 2048); err != nil {
		return nil, err
	}
	rawID, err := readString("rawId", 1, 2048)
	if err != nil {
		return nil, err
	}
	typeName, err := readString("type", 1, len("public-key"))
	if err != nil || typeName != "public-key" {
		return nil, invalidRequest("credential.type must be public-key")
	}
	if attachment, ok := envelope["authenticatorAttachment"]; ok && string(attachment) != "null" {
		var value string
		if err := json.Unmarshal(attachment, &value); err != nil || (value != "platform" && value != "cross-platform") {
			return nil, invalidRequest("credential.authenticatorAttachment is invalid")
		}
	}
	for _, key := range []string{"response", "clientExtensionResults"} {
		value, ok := envelope[key]
		if !ok {
			return nil, invalidRequest("credential.%s is required", key)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil || object == nil || len(object) > 20 {
			return nil, invalidRequest("credential.%s exceeds contract limits", key)
		}
	}
	credentialID, err := base64.RawURLEncoding.DecodeString(rawID)
	if err != nil || len(credentialID) == 0 {
		return nil, invalidRequest("credential.rawId is invalid base64url")
	}
	return credentialID, nil
}

func (s *Server) GetCurrentSession(w http.ResponseWriter, r *http.Request) {
	user, token, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.Sessions != nil {
		session, sessionErr := s.deps.Sessions.CurrentSession(r.Context(), token)
		if sessionErr != nil {
			s.writeError(w, r, sessionErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": userResponse(user), "session": sessionResponse(session, true)})
		return
	}
	grant := auth.SessionGrant{Token: token, SessionID: "current", UserID: user.ID, ExpiresAt: s.now().UTC().Add(24 * time.Hour)}
	writeJSON(w, http.StatusOK, authResult(user, grant, s.now().UTC()))
}

func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request) {
	user, token, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.Sessions == nil {
		s.unavailable(w, r, "sessions")
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if limit == 0 {
		limit = 25
	}
	current, err := s.deps.Sessions.CurrentSession(r.Context(), token)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.deps.Sessions.ListSessions(r.Context(), user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	start := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		for index, item := range items {
			if item.ID == cursor {
				start = index + 1
				break
			}
			if index == len(items)-1 {
				s.writeError(w, r, invalidRequest("cursor is invalid"))
				return
			}
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	payload := make([]any, 0, end-start)
	for _, item := range items[start:end] {
		payload = append(payload, sessionResponse(item, item.ID == current.ID))
	}
	page := pageInfo()
	if end < len(items) {
		page = map[string]any{"nextCursor": items[end-1].ID, "hasNextPage": true}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": payload, "pageInfo": page})
}

func (s *Server) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	user, token, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.Sessions == nil {
		s.unavailable(w, r, "sessions")
		return
	}
	if err := s.deps.Sessions.RevokeOtherSessions(r.Context(), user.ID, token); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RevokeSession(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.Sessions == nil {
		s.unavailable(w, r, "sessions")
		return
	}
	if err := s.deps.Sessions.RevokeSessionByID(r.Context(), user.ID, chi.URLParam(r, "sessionId")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse(user))
}

func (s *Server) SignOut(w http.ResponseWriter, r *http.Request) {
	_, token, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.deps.Auth.RevokeSession(r.Context(), token); err != nil {
		s.writeError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "jandibat_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.deps.SecureCookies, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SubjectOperation(w http.ResponseWriter, r *http.Request) {
	if s.deps.Subjects == nil {
		s.unavailable(w, r, "subjects")
		return
	}
	var user auth.User
	var err error
	publicRead := r.Method == http.MethodGet && chi.URLParam(r, "subject") != "" && !strings.HasSuffix(r.URL.Path, "/settings")
	if publicRead {
		if token, tokenErr := sessionToken(r); tokenErr == nil && s.deps.Auth != nil {
			user, err = s.deps.Auth.AuthenticateSession(r.Context(), token)
		}
	} else {
		user, _, err = s.authenticate(r)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if user.ID != "" {
		if err := s.deps.Subjects.ProvisionUser(r.Context(), subjectUser(user)); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	s.serveSubjectOperation(w, r, user.ID)
}

func subjectUser(user auth.User) subjects.User {
	return subjects.User{
		ID: user.ID, PrimaryEmail: user.PrimaryEmail, EmailVerifiedAt: user.EmailVerifiedAt,
		Status: subjects.UserStatus(user.Status), CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt,
	}
}

func (s *Server) serveSubjectOperation(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	identifier := chi.URLParam(r, "subject")
	switch {
	case r.URL.Path == "/v1/me/settings" && r.Method == http.MethodGet:
		settings, err := s.deps.Subjects.GetUserSettings(ctx, userID)
		s.writeSubjectResult(w, r, http.StatusOK, settings, err)
	case r.URL.Path == "/v1/me/settings" && r.Method == http.MethodPatch:
		var input struct {
			Locale   *string                `json:"locale,omitempty"`
			Timezone *string                `json:"timezone,omitempty"`
			Theme    *subjects.HeatmapTheme `json:"theme,omitempty"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
		settings, err := s.deps.Subjects.UpdateUserSettings(ctx, userID, subjects.UpdateUserSettingsInput{Locale: input.Locale, Timezone: input.Timezone, Theme: input.Theme})
		s.writeSubjectResult(w, r, http.StatusOK, settings, err)
	case r.URL.Path == "/v1/subjects" && r.Method == http.MethodGet:
		limit, err := parseLimit(r.URL.Query().Get("limit"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		result, err := s.deps.Subjects.ListSubjects(ctx, userID, subjects.ListSubjectsInput{Cursor: r.URL.Query().Get("cursor"), Limit: limit})
		s.writeSubjectResult(w, r, http.StatusOK, result, err)
	case r.URL.Path == "/v1/subjects" && r.Method == http.MethodPost:
		var input struct {
			Handle      string  `json:"handle"`
			DisplayName *string `json:"displayName,omitempty"`
			Timezone    string  `json:"timezone"`
			IsPublic    *bool   `json:"isPublic,omitempty"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
		item, err := s.deps.Subjects.CreateSubject(ctx, userID, subjects.CreateSubjectInput{Handle: input.Handle, DisplayName: input.DisplayName, Timezone: input.Timezone, IsPublic: input.IsPublic})
		if err == nil {
			w.Header().Set("Location", "/v1/subjects/"+item.Handle)
		}
		s.writeSubjectResult(w, r, http.StatusCreated, item, err)
	case strings.HasSuffix(r.URL.Path, "/settings") && r.Method == http.MethodGet:
		settings, err := s.deps.Subjects.GetSubjectSettings(ctx, userID, identifier)
		s.writeSubjectResult(w, r, http.StatusOK, settings, err)
	case strings.HasSuffix(r.URL.Path, "/settings") && r.Method == http.MethodPatch:
		var input struct {
			Timezone            *string                 `json:"timezone,omitempty"`
			IsPublic            *bool                   `json:"isPublic,omitempty"`
			DefaultTheme        *subjects.HeatmapTheme  `json:"defaultTheme,omitempty"`
			WeekStart           *subjects.WeekStart     `json:"weekStart,omitempty"`
			SyncEnabled         *bool                   `json:"syncEnabled,omitempty"`
			SyncIntervalMinutes *int                    `json:"syncIntervalMinutes,omitempty"`
			FailurePolicy       *subjects.FailurePolicy `json:"failurePolicy,omitempty"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
		settings, err := s.deps.Subjects.UpdateSubjectSettings(ctx, userID, identifier, subjects.UpdateSubjectSettingsInput{
			Timezone: input.Timezone, IsPublic: input.IsPublic, DefaultTheme: input.DefaultTheme,
			WeekStart: input.WeekStart, SyncEnabled: input.SyncEnabled,
			SyncIntervalMinutes: input.SyncIntervalMinutes, FailurePolicy: input.FailurePolicy,
		})
		s.writeSubjectResult(w, r, http.StatusOK, settings, err)
	case identifier != "" && r.Method == http.MethodGet:
		item, err := s.deps.Subjects.GetSubject(ctx, userID, identifier)
		s.writeSubjectResult(w, r, http.StatusOK, item, err)
	case identifier != "" && r.Method == http.MethodPatch:
		var raw map[string]json.RawMessage
		if err := decodeJSON(w, r, &raw); err != nil {
			s.writeError(w, r, err)
			return
		}
		for key := range raw {
			if key != "handle" && key != "displayName" {
				s.writeError(w, r, invalidRequest("unknown field %q", key))
				return
			}
		}
		input := subjects.UpdateSubjectInput{}
		if value, ok := raw["handle"]; ok {
			if err := json.Unmarshal(value, &input.Handle); err != nil || input.Handle == nil {
				s.writeError(w, r, invalidRequest("handle must be a string"))
				return
			}
		}
		if value, ok := raw["displayName"]; ok {
			input.DisplayNameSet = true
			if string(value) != "null" && json.Unmarshal(value, &input.DisplayName) != nil {
				s.writeError(w, r, invalidRequest("displayName must be a string or null"))
				return
			}
		}
		item, err := s.deps.Subjects.UpdateSubject(ctx, userID, identifier, input)
		s.writeSubjectResult(w, r, http.StatusOK, item, err)
	case identifier != "" && r.Method == http.MethodDelete:
		authorizer, ok := s.deps.Subjects.(SubjectDeletionAuthorizer)
		if !ok || s.deps.SubjectDeletions == nil {
			s.unavailable(w, r, "subject deletion")
			return
		}
		subject, err := authorizer.AuthorizeSubjectDeletion(ctx, userID, identifier)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		deletionRequestID, err := operations.NewAuditEventID()
		if err != nil {
			s.writeError(w, r, errUnavailable)
			return
		}
		deletion, err := s.deps.SubjectDeletions.Request(ctx, deletionRequestID, operations.DeletionTargetSubject, subject.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		// A repeated request for the same active target returns the already-durable
		// server request ID. The store independently rejects a caller-generated ID
		// bound to another target; the transport rechecks the target and shape.
		if strings.TrimSpace(deletion.RequestID) == "" || deletion.TargetType != operations.DeletionTargetSubject || deletion.TargetID != subject.ID ||
			deletion.Status != operations.DeletionRequested {
			s.writeError(w, r, operations.ErrInvalidDeletionRequest)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"requestId": deletion.RequestID, "status": deletion.Status})
	default:
		s.methodNotAllowed(w, r)
	}
}

func (s *Server) writeSubjectResult(w http.ResponseWriter, r *http.Request, status int, payload any, err error) {
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, status, payload)
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 100 {
		return 0, invalidRequest("limit must be between 1 and 100")
	}
	return limit, nil
}

func pageBounds(r *http.Request, total int, idAt func(int) string) (int, int, map[string]any, error) {
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return 0, 0, nil, err
	}
	if limit == 0 {
		limit = 25
	}
	start := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if len(cursor) > 1024 {
			return 0, 0, nil, invalidRequest("cursor is too long")
		}
		found := false
		for index := 0; index < total; index++ {
			if idAt(index) == cursor {
				start, found = index+1, true
				break
			}
		}
		if !found {
			return 0, 0, nil, invalidRequest("cursor is invalid")
		}
	}
	end := start + limit
	if end > total {
		end = total
	}
	page := pageInfo()
	if end < total && end > start {
		page = map[string]any{"nextCursor": idAt(end - 1), "hasNextPage": true}
	}
	return start, end, page, nil
}

func (s *Server) ListConnections(w http.ResponseWriter, r *http.Request) {
	user, err := s.requireOwner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.Connections == nil {
		s.unavailable(w, r, "provider connections")
		return
	}
	subjectID, err := s.resolveSubjectID(r, user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.deps.Connections.List(r.Context(), subjectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	start, end, page, err := pageBounds(r, len(items), func(index int) string { return items[index].ID })
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	connections := make([]any, 0, end-start)
	for _, item := range items[start:end] {
		connections = append(connections, connectionResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": connections, "pageInfo": page})
}

func (s *Server) CreateConnection(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProviderID     string                  `json:"providerId"`
		AuthMethod     integrations.AuthMethod `json:"authMethod"`
		Token          string                  `json:"token,omitempty"`
		Scopes         []string                `json:"scopes,omitempty"`
		IncludePrivate bool                    `json:"includePrivate,omitempty"`
		RedirectURI    string                  `json:"redirectUri,omitempty"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(input.Token) > 4096 || len(input.Scopes) > 50 || hasDuplicateStrings(input.Scopes) {
		s.writeError(w, r, invalidRequest("token or scopes exceed contract limits"))
		return
	}
	if len(input.RedirectURI) > 2048 || (input.RedirectURI != "" && !exactlyAllowed(input.RedirectURI, s.deps.AllowedRedirects)) {
		s.writeError(w, r, invalidRequest("redirectUri is not allowed"))
		return
	}
	var (
		user           auth.User
		sessionBinding string
		err            error
	)
	if input.AuthMethod == integrations.AuthOAuth2 {
		user, sessionBinding, err = s.authenticateSessionCookie(r)
		if err == nil {
			err = s.checkOwner(r.Context(), user.ID, chi.URLParam(r, "subject"))
		}
	} else {
		user, err = s.requireOwner(r)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopeConnectionIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopeConnectionAccount, key: rateLimitIdentity("user", user.ID)},
	) {
		return
	}
	if s.deps.Connections == nil {
		s.unavailable(w, r, "provider connections")
		return
	}
	subjectID, err := s.resolveSubjectID(r, user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	_, subjectHandle, resolveErr := s.resolveSubjectReference(r, user.ID)
	if resolveErr != nil {
		s.writeError(w, r, resolveErr)
		return
	}
	base := integrations.ConnectInput{SubjectID: subjectID, ProviderID: input.ProviderID, EnvironmentID: input.ProviderID, ExternalAccountLogin: subjectHandle, Scopes: input.Scopes, IncludePrivate: input.IncludePrivate}
	if input.AuthMethod != integrations.AuthOAuth2 {
		base.ExternalAccountID = subjectHandle
	}
	var connection integrations.ProviderConnection
	var authorizationURL string
	switch input.AuthMethod {
	case integrations.AuthNone:
		connection, err = s.deps.Connections.ConnectPublic(r.Context(), base)
	case integrations.AuthToken:
		connection, err = s.deps.Connections.ConnectToken(r.Context(), integrations.ConnectTokenInput{ConnectInput: base, Credentials: integrations.TokenCredentials{AccessToken: input.Token}})
	case integrations.AuthOAuth2:
		connection, err = s.deps.Connections.BeginOAuth(r.Context(), base)
		if err == nil {
			flow := s.deps.OAuthFlows[input.ProviderID]
			if flow == nil {
				err = fmt.Errorf("%w: OAuth provider %s", errUnavailable, input.ProviderID)
			} else {
				started, beginErr := flow.Begin(r.Context(), adapteroauth.AuthorizationRequest{ConnectionID: connection.ID, SubjectID: connection.SubjectID, Scopes: input.Scopes, SessionBinding: sessionBinding, ClientRedirectURI: input.RedirectURI})
				if beginErr != nil {
					_, _ = s.deps.Connections.Revoke(r.Context(), connection.ID)
					err = beginErr
				} else {
					authorizationURL = started.URL
				}
			}
		}
	default:
		err = integrations.ErrUnsupportedAuthMethod
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	setAuditTarget(r, operations.AuditTarget{Type: "provider_connection", ID: connection.ID})
	w.Header().Set("Location", "/v1/subjects/"+chi.URLParam(r, "subject")+"/provider-connections/"+connection.ID)
	payload := map[string]any{"connection": connectionResponse(connection)}
	if authorizationURL != "" {
		payload["authorizationUrl"] = authorizationURL
	}
	writeJSON(w, http.StatusCreated, payload)
}

func (s *Server) ownedConnection(r *http.Request) (integrations.ProviderConnection, error) {
	user, err := s.requireOwner(r)
	if err != nil {
		return integrations.ProviderConnection{}, err
	}
	if s.deps.Connections == nil {
		return integrations.ProviderConnection{}, errUnavailable
	}
	subjectID, err := s.resolveSubjectID(r, user.ID)
	if err != nil {
		return integrations.ProviderConnection{}, err
	}
	item, err := s.deps.Connections.Get(r.Context(), chi.URLParam(r, "connectionId"))
	if err != nil {
		return item, err
	}
	if item.SubjectID != subjectID {
		return integrations.ProviderConnection{}, integrations.ErrNotFound
	}
	return item, nil
}

func (s *Server) GetConnection(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedConnection(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionResponse(item))
}
func (s *Server) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedConnection(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var input struct {
		Token   *string   `json:"token,omitempty"`
		Scopes  *[]string `json:"scopes,omitempty"`
		Enabled *bool     `json:"enabled,omitempty"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if input.Token != nil && len(*input.Token) > 4096 || input.Scopes != nil && (len(*input.Scopes) > 50 || hasDuplicateStrings(*input.Scopes)) {
		s.writeError(w, r, invalidRequest("token or scopes exceed contract limits"))
		return
	}
	updated, err := s.deps.Connections.Update(r.Context(), integrations.UpdateConnectionInput{ID: item.ID, Token: input.Token, Scopes: input.Scopes, Enabled: input.Enabled})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionResponse(updated))
}
func (s *Server) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedConnection(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err = s.deps.Connections.Revoke(r.Context(), item.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SyncConnection(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedConnection(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, _ := AuditActorFromContext(r.Context())
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopeSyncIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopeSyncAccount, key: rateLimitIdentity("user-subject", actor.ID+"\x00"+item.SubjectID)},
	) {
		return
	}
	if s.deps.Sync == nil {
		s.unavailable(w, r, "synchronization")
		return
	}
	var input struct {
		From, To      string
		Force         bool   `json:"force"`
		FailurePolicy string `json:"failurePolicy"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		s.writeError(w, r, integrations.ErrInvalidIdempotencyKey)
		return
	}
	var from, to *domain.Date
	if input.From != "" {
		value := domain.Date(input.From)
		from = &value
	}
	if input.To != "" {
		value := domain.Date(input.To)
		to = &value
	}
	job, err := s.deps.Sync.EnqueueManualSync(r.Context(), integrations.ManualSyncInput{
		ConnectionID: item.ID, IdempotencyKey: idempotencyKey, From: from, To: to,
		Force: input.Force, FailurePolicy: domain.FetchFailurePolicy(input.FailurePolicy),
	})
	if err != nil && job.ID == "" {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/sync-jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, syncJobResponse(job, item))
}

func (s *Server) GetSyncJob(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.Sync == nil || s.deps.Connections == nil {
		s.unavailable(w, r, "synchronization")
		return
	}
	job, err := s.deps.Sync.GetJob(r.Context(), chi.URLParam(r, "syncJobId"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	connection, err := s.deps.Connections.Get(r.Context(), job.ConnectionID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.checkOwner(r.Context(), user.ID, connection.SubjectID); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, syncJobResponse(job, connection))
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

func (s *Server) ListCustomProviders(w http.ResponseWriter, r *http.Request) {
	user, err := s.requireOwner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.CustomProviders == nil {
		s.unavailable(w, r, "custom providers")
		return
	}
	subjectID, err := s.resolveSubjectID(r, user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.deps.CustomProviders.List(r.Context(), subjectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	start, end, page, err := pageBounds(r, len(items), func(index int) string { return items[index].ID })
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	providers := make([]any, 0, end-start)
	for _, item := range items[start:end] {
		providers = append(providers, customProviderResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers, "pageInfo": page})
}

func newSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Server) CreateCustomProvider(w http.ResponseWriter, r *http.Request) {
	user, err := s.requireOwner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.deps.CustomProviders == nil {
		s.unavailable(w, r, "custom providers")
		return
	}
	var input struct {
		Key            string   `json:"key"`
		Name           string   `json:"name"`
		Description    string   `json:"description,omitempty"`
		AllowedActions []string `json:"allowedActions,omitempty"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	subjectID, err := s.resolveSubjectID(r, user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	secret, err := newSecret()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	provider, err := s.deps.CustomProviders.Create(r.Context(), integrations.CreateCustomProviderInput{SubjectID: subjectID, Slug: input.Key, Name: input.Name, Description: input.Description, AllowedActions: input.AllowedActions, AllowedMetrics: []string{"count"}, IngestSecret: secret})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	setAuditTarget(r, operations.AuditTarget{Type: "custom_provider", ID: provider.ID})
	w.Header().Set("Location", "/v1/subjects/"+chi.URLParam(r, "subject")+"/custom-providers/"+provider.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"provider": customProviderResponse(provider), "ingestionKey": secret})
}

func (s *Server) ownedCustomProvider(r *http.Request) (integrations.CustomProvider, error) {
	user, err := s.requireOwner(r)
	if err != nil {
		return integrations.CustomProvider{}, err
	}
	if s.deps.CustomProviders == nil {
		return integrations.CustomProvider{}, errUnavailable
	}
	subjectID, err := s.resolveSubjectID(r, user.ID)
	if err != nil {
		return integrations.CustomProvider{}, err
	}
	item, err := s.deps.CustomProviders.Get(r.Context(), chi.URLParam(r, "customProviderId"))
	if err != nil {
		return item, err
	}
	if item.SubjectID != subjectID {
		return integrations.CustomProvider{}, integrations.ErrNotFound
	}
	return item, nil
}

func (s *Server) GetCustomProvider(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedCustomProvider(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, customProviderResponse(item))
}

func (s *Server) UpdateCustomProvider(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedCustomProvider(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var input map[string]json.RawMessage
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(input) == 0 {
		s.writeError(w, r, invalidRequest("at least one custom provider property is required"))
		return
	}
	for key, value := range input {
		switch key {
		case "name":
			if err := json.Unmarshal(value, &item.Name); err != nil {
				s.writeError(w, r, invalidRequest("name must be a string"))
				return
			}
		case "description":
			if string(value) == "null" {
				item.Description = ""
			} else if err := json.Unmarshal(value, &item.Description); err != nil {
				s.writeError(w, r, invalidRequest("description must be a string or null"))
				return
			}
		case "status":
			if err := json.Unmarshal(value, &item.Status); err != nil {
				s.writeError(w, r, invalidRequest("status must be a string"))
				return
			}
		case "allowedActions":
			if err := json.Unmarshal(value, &item.AllowedActions); err != nil {
				s.writeError(w, r, invalidRequest("allowedActions must be an array"))
				return
			}
		default:
			s.writeError(w, r, invalidRequest("unknown field %q", key))
			return
		}
	}
	updated, err := s.deps.CustomProviders.Update(r.Context(), integrations.UpdateCustomProviderInput{ID: item.ID, Name: item.Name, Description: item.Description, Status: item.Status, AllowedActions: item.AllowedActions, AllowedMetrics: item.AllowedMetrics})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, customProviderResponse(updated))
}

func (s *Server) DeleteCustomProvider(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedCustomProvider(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.deps.CustomProviders.Delete(r.Context(), item.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RotateCustomProviderKey(w http.ResponseWriter, r *http.Request) {
	item, err := s.ownedCustomProvider(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, _ := AuditActorFromContext(r.Context())
	if !s.allowRequests(w, r,
		rateLimitBucket{scope: rateScopeRotateKeyIP, key: clientAddress(r)},
		rateLimitBucket{scope: rateScopeRotateKeyAccount, key: rateLimitIdentity("user-subject", actor.ID+"\x00"+item.SubjectID)},
	) {
		return
	}
	secret, err := newSecret()
	if err == nil {
		err = s.deps.CustomProviders.RotateIngestSecret(r.Context(), item.ID, secret)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingestionKey": secret, "createdAt": s.now().UTC()})
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

func (s *Server) requireOwner(r *http.Request) (auth.User, error) {
	user, _, err := s.authenticate(r)
	if err != nil {
		return user, err
	}
	return user, s.checkOwner(r.Context(), user.ID, chi.URLParam(r, "subject"))
}
func (s *Server) resolveSubjectID(r *http.Request, _ string) (string, error) {
	id, _, err := s.resolveSubjectReference(r, "")
	return id, err
}
func (s *Server) resolveSubjectReference(r *http.Request, _ string) (string, string, error) {
	identifier := chi.URLParam(r, "subject")
	if s.deps.SubjectResolver == nil {
		return identifier, identifier, nil
	}
	if resolver, ok := s.deps.SubjectResolver.(SubjectReferenceResolver); ok {
		return resolver.ResolveSubjectReference(r.Context(), identifier)
	}
	id, err := s.deps.SubjectResolver.ResolveSubjectID(r.Context(), identifier)
	return id, identifier, err
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

func optionsResponse(options auth.PasskeyOptions) map[string]any {
	var publicKey any
	_ = json.Unmarshal(options.PublicKey, &publicKey)
	return map[string]any{"ceremonyId": options.CeremonyID, "expiresAt": options.ExpiresAt, "publicKey": publicKey}
}
func passkeyResponse(item auth.PasskeyCredential) map[string]any {
	return map[string]any{"id": item.ID, "label": item.Label, "transports": nonNilStrings(item.Transports), "createdAt": item.CreatedAt, "lastUsedAt": item.LastUsedAt}
}
func userResponse(user auth.User) map[string]any {
	return map[string]any{"id": user.ID, "primaryEmail": user.PrimaryEmail, "emailVerifiedAt": user.EmailVerifiedAt, "status": user.Status, "createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt}
}
func authResult(user auth.User, grant auth.SessionGrant, created time.Time) map[string]any {
	return map[string]any{"user": userResponse(user), "session": map[string]any{"id": grant.SessionID, "userId": user.ID, "current": true, "createdAt": created, "expiresAt": grant.ExpiresAt, "lastSeenAt": created, "revokedAt": nil, "userAgent": nil, "ipAddress": nil}}
}
func sessionResponse(item auth.Session, current bool) map[string]any {
	var userAgent any
	if item.UserAgent != "" {
		userAgent = item.UserAgent
	}
	var ipAddress any
	if item.IPAddress != "" {
		ipAddress = item.IPAddress
	}
	return map[string]any{
		"id": item.ID, "userId": item.UserID, "current": current,
		"createdAt": item.CreatedAt, "expiresAt": item.ExpiresAt,
		"lastSeenAt": item.LastSeenAt, "revokedAt": item.RevokedAt,
		"userAgent": userAgent, "ipAddress": ipAddress,
	}
}
func connectionResponse(item integrations.ProviderConnection) map[string]any {
	var external any
	if item.ExternalAccountID != "" {
		external = item.ExternalAccountID
	}
	var lastError any
	if item.LastError != "" {
		lastError = "The last synchronization attempt failed."
	}
	return map[string]any{"id": item.ID, "subjectId": item.SubjectID, "providerId": item.ProviderID, "environmentId": item.EnvironmentID, "authMethod": item.AuthMethod, "status": item.Status, "externalAccountId": external, "externalAccountName": nil, "scopes": nonNilStrings(item.Scopes), "privateDataEnabled": item.PrivateDataEnabled, "tokenExpiresAt": item.TokenExpiresAt, "lastSyncedAt": item.LastSyncedAt, "lastSyncStatus": nil, "lastError": lastError, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt}
}
func syncJobResponse(job integrations.SyncJob, connection integrations.ProviderConnection) map[string]any {
	status := string(job.Status)
	if status == "pending" {
		status = "queued"
	}
	var problem any
	if job.LastError != "" {
		problem = map[string]any{"type": "about:blank", "title": "Synchronization failed", "status": 500, "code": "sync_failed", "requestId": "", "detail": "The provider synchronization failed."}
	}
	return map[string]any{"id": job.ID, "subjectId": connection.SubjectID, "connectionId": job.ConnectionID, "providerId": connection.ProviderID, "status": status, "requestedAt": job.CreatedAt, "startedAt": job.StartedAt, "completedAt": job.FinishedAt, "acceptedFacts": job.FactsWritten, "rejectedFacts": 0, "error": problem}
}
func customProviderResponse(item integrations.CustomProvider) map[string]any {
	var description any
	if item.Description != "" {
		description = item.Description
	}
	return map[string]any{"id": item.ID, "subjectId": item.SubjectID, "environmentId": item.EnvironmentID, "key": item.Slug, "name": item.Name, "description": description, "status": item.Status, "allowedActions": nonNilStrings(item.AllowedActions), "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt}
}
func pageInfo() map[string]any { return map[string]any{"nextCursor": nil, "hasNextPage": false} }
func nonNilStrings(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

func hasDuplicateStrings(items []string) bool {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if _, exists := seen[item]; exists {
			return true
		}
		seen[item] = struct{}{}
	}
	return false
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

func (s *Server) allowRequest(w http.ResponseWriter, r *http.Request, scope, key string) bool {
	return s.allowRequests(w, r, rateLimitBucket{scope: scope, key: key})
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
func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, http.StatusMethodNotAllowed, problem{Type: "https://jandibat.org/problems/method_not_allowed", Title: "Method Not Allowed", Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Detail: "method is not allowed for this resource", Instance: r.URL.Path, RequestID: middleware.GetReqID(r.Context())})
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
func Healthz(w http.ResponseWriter, r *http.Request)       { New(Dependencies{}).Healthz(w, r) }
func ListProviders(w http.ResponseWriter, r *http.Request) { New(Dependencies{}).ListProviders(w, r) }
func GetActivities(w http.ResponseWriter, r *http.Request) { New(Dependencies{}).GetActivities(w, r) }
