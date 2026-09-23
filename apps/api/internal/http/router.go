package apihttp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
)

// Dependencies is the HTTP composition root.  Keeping it here makes the
// transport independently testable and prevents handlers from constructing
// databases, provider clients, or other adapters.
type Dependencies = handlers.Dependencies

// NewRouter accepts an optional dependency set for backwards compatibility
// with the original scaffold. Production callers should always pass one.
func NewRouter(dependencies ...Dependencies) stdhttp.Handler {
	var deps Dependencies
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}
	return newRouter(deps, nil)
}

// NewRouterWithGraphQL mounts the domain API only when the complete trusted
// runtime graph is available. Startup must surface a missing dependency.
func NewRouterWithGraphQL(deps Dependencies, graphDeps GraphQLDependencies) (stdhttp.Handler, error) {
	if err := validateGraphQLDependencies(deps, graphDeps); err != nil {
		return nil, err
	}
	return newRouter(deps, &graphDeps), nil
}

func newRouter(deps Dependencies, graphDeps *GraphQLDependencies) stdhttp.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
		deps.Logger = logger
	}
	if deps.CustomProviders != nil {
		deps.CustomProviders = observedCustomProviders{next: deps.CustomProviders, metrics: observability.Default()}
	}
	server := handlers.New(deps)

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(canonicalRequestID)
	router.Use(requestIDHeader)
	router.Use(cachePrivateByDefault)
	router.Use(capturePeerAddress)
	router.Use(observeHTTP(observability.Default(), logger))
	if deps.TrustProxyHeaders {
		router.Use(trustedProxyHeaders)
	}
	// The timeout must own the audit transaction lifetime. Its context is
	// canceled only after auditRequests has enqueued the outcome and committed.
	router.Use(timeoutProblems(30 * time.Second))
	router.Use(securityHeaders)
	router.Use(cors(deps.AllowedOrigins))
	router.Use(csrf(deps.AllowedOrigins))
	if graphDeps != nil {
		router.Use(func(next stdhttp.Handler) stdhttp.Handler {
			return graph.PreflightHTTP(next, graph.HTTPOptions{Development: graphDeps.Development})
		})
		router.Use(func(next stdhttp.Handler) stdhttp.Handler {
			return graphQLOperationObserver(next, observability.Default(), logger)
		})
	}
	if deps.Audit != nil {
		router.Use(auditRequests(deps.Audit, deps.AuditSourceKey, deps.RateLimiter, deps.MutationAudits, logger))
	}
	router.Use(recoverProblems(logger))
	if graphDeps != nil {
		router.Use(func(next stdhttp.Handler) stdhttp.Handler {
			return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				if graphQLRateLimit(w, r, deps.RateLimiter) {
					next.ServeHTTP(w, r)
				}
			})
		})
	}

	router.Get("/healthz", server.Healthz)
	router.Handle("/metrics", loopbackMetrics(observability.Default().Handler()))
	if graphDeps != nil {
		graphqlHandler := graph.NewHTTPHandler(&graph.Resolver{}, graph.HTTPOptions{Development: graphDeps.Development})
		router.Method(stdhttp.MethodPost, "/graphql", graphQLTrustedContext(deps, *graphDeps, graphqlHandler))
	}
	router.Route("/v1", func(r chi.Router) {
		r.Get("/providers", server.ListProviders)
		r.Get("/activities/{subject}", server.GetActivities)
		r.Get("/render/{subject}.svg", server.RenderHeatmap)

		r.Post("/auth/magic-link/request", server.RequestMagicLink)
		r.Post("/auth/magic-link/consume", server.ConsumeMagicLink)
		r.Post("/auth/passkey/register/options", server.BeginPasskeyRegistration)
		r.Post("/auth/passkey/register/finish", server.FinishPasskeyRegistration)
		r.Post("/auth/passkey/sign-in/options", server.BeginPasskeySignIn)
		r.Post("/auth/passkey/sign-in/finish", server.FinishPasskeySignIn)
		r.Get("/auth/session", server.GetCurrentSession)
		r.Delete("/auth/session", server.SignOut)
		r.Get("/auth/sessions", server.ListSessions)
		r.Delete("/auth/sessions", server.RevokeOtherSessions)
		r.Delete("/auth/sessions/{sessionId}", server.RevokeSession)

		r.Get("/me", server.GetCurrentUser)
		r.Get("/me/settings", server.SubjectOperation)
		r.Patch("/me/settings", server.SubjectOperation)
		r.Get("/subjects", server.SubjectOperation)
		r.Post("/subjects", server.SubjectOperation)
		r.Get("/subjects/{subject}", server.SubjectOperation)
		r.Patch("/subjects/{subject}", server.SubjectOperation)
		r.Delete("/subjects/{subject}", server.SubjectOperation)
		r.Get("/subjects/{subject}/settings", server.SubjectOperation)
		r.Patch("/subjects/{subject}/settings", server.SubjectOperation)

		r.Get("/subjects/{subject}/provider-connections", server.ListConnections)
		r.Post("/subjects/{subject}/provider-connections", server.CreateConnection)
		r.Get("/subjects/{subject}/provider-connections/{connectionId}", server.GetConnection)
		r.Patch("/subjects/{subject}/provider-connections/{connectionId}", server.UpdateConnection)
		r.Delete("/subjects/{subject}/provider-connections/{connectionId}", server.DeleteConnection)
		r.Post("/subjects/{subject}/provider-connections/{connectionId}/sync", server.SyncConnection)
		r.Get("/integrations/{provider}/callback", server.ProviderOAuthCallback)
		r.Get("/sync-jobs/{syncJobId}", server.GetSyncJob)

		r.Get("/subjects/{subject}/custom-providers", server.ListCustomProviders)
		r.Post("/subjects/{subject}/custom-providers", server.CreateCustomProvider)
		r.Get("/subjects/{subject}/custom-providers/{customProviderId}", server.GetCustomProvider)
		r.Patch("/subjects/{subject}/custom-providers/{customProviderId}", server.UpdateCustomProvider)
		r.Delete("/subjects/{subject}/custom-providers/{customProviderId}", server.DeleteCustomProvider)
		r.Post("/subjects/{subject}/custom-providers/{customProviderId}/rotate-key", server.RotateCustomProviderKey)
		r.Post("/custom-providers/{customProviderId}/activities:ingest", server.IngestCustomActivities)
	})

	return router
}

// trustedProxyHeaders accepts exactly one canonical client address from a
// deployment proxy that removes inbound forwarding headers before rewriting
// them. Ambiguous chains or conflicting header families leave the direct peer
// unchanged so attacker-controlled entries cannot become rate-limit keys.
func trustedProxyHeaders(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var candidate string
		for _, name := range []string{"True-Client-IP", "X-Real-IP", "X-Forwarded-For"} {
			values := r.Header.Values(name)
			if len(values) > 1 {
				candidate = ""
				break
			}
			value := ""
			if len(values) == 1 {
				value = strings.TrimSpace(values[0])
			}
			if value == "" {
				continue
			}
			if candidate != "" || strings.Contains(value, ",") {
				candidate = ""
				break
			}
			candidate = value
		}
		if address := net.ParseIP(candidate); address != nil {
			r.RemoteAddr = address.String()
		}
		next.ServeHTTP(w, r)
	})
}

// recoverProblems preserves the router's RFC 9457 error contract for panics.
// Once an application handler has committed a response it is too late to
// replace it, so the middleware only writes a problem before the first byte.
func recoverProblems(logger *zap.Logger) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == stdhttp.ErrAbortHandler {
					panic(recovered)
				}
				// Panic values may contain upstream bodies, tokens, or database error
				// text. Keep the operational signal and correlation ID without
				// serializing the recovered value into process logs.
				observability.Log(logger, "http.panic_recovered",
					observability.SafeString("request_id", middleware.GetReqID(r.Context())))
				if r.Header.Get("Connection") != "Upgrade" && wrapped.Status() == 0 {
					writeFrameworkProblem(wrapped, r, stdhttp.StatusInternalServerError, "Internal Server Error", "internal_error", "The service could not complete the request.")
				}
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}

// timeoutProblems follows chi's cooperative timeout behavior while ensuring
// that a handler which returns on context cancellation produces a complete
// 503 problem document instead of an empty framework response.
func timeoutProblems(timeout time.Duration) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(wrapped, r.WithContext(ctx))
			if ctx.Err() == context.DeadlineExceeded && wrapped.Status() == 0 {
				wrapped.Header().Set("Retry-After", "5")
				writeFrameworkProblem(wrapped, r, stdhttp.StatusServiceUnavailable, "Service Unavailable", "service_unavailable", "The service could not complete the request before its deadline.")
			}
		})
	}
}

func writeFrameworkProblem(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, title, code, detail string) {
	body, err := json.Marshal(struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		Status    int    `json:"status"`
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		Instance  string `json:"instance"`
		RequestID string `json:"requestId"`
	}{
		Type:      "https://jandibat.org/problems/" + code,
		Title:     title,
		Status:    status,
		Code:      code,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: middleware.GetReqID(r.Context()),
	})
	if err != nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	w.Header().Del("ETag")
	w.Header().Set("Content-Type", "application/problem+json")
	if requestID := middleware.GetReqID(r.Context()); requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

type auditResponseWriter struct {
	destination stdhttp.ResponseWriter
	status      int
}

func newAuditResponseWriter(destination stdhttp.ResponseWriter) *auditResponseWriter {
	return &auditResponseWriter{destination: destination}
}

func (writer *auditResponseWriter) Header() stdhttp.Header { return writer.destination.Header() }

func (writer *auditResponseWriter) Unwrap() stdhttp.ResponseWriter { return writer.destination }

func (writer *auditResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.destination.WriteHeader(status)
}

func (writer *auditResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = stdhttp.StatusOK
	}
	return writer.destination.Write(value)
}

func auditRequests(recorder handlers.AuditRecorder, sourceKey []byte, dependencies ...any) func(stdhttp.Handler) stdhttp.Handler {
	sourceKey = append([]byte(nil), sourceKey...)
	var limiter handlers.RateLimiter
	var coordinator operations.MutationAuditCoordinator
	logger := zap.NewNop()
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case handlers.RateLimiter:
			limiter = typed
		case operations.MutationAuditCoordinator:
			coordinator = typed
		case *zap.Logger:
			if typed != nil {
				logger = typed
			}
		}
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			// The timeout middleware below derives a child request. Install one
			// shared actor holder on the outer request so the authenticated handler
			// can publish its stable user ID to this middleware's outcome record.
			*r = *r.WithContext(withGraphQLAuditActorHolder(handlers.WithAuditActorHolder(r.Context())))
			targetType, mutation := mutationRequestTargetForRequest(r)
			if mutation {
				auditRequestID, err := operations.NewAuditEventID()
				if err != nil {
					writeAuditUnavailable(w, r)
					return
				}
				*r = *r.WithContext(context.WithValue(r.Context(), auditRequestIDContextKey{}, auditRequestID))
				allowed, retryAfter, err := allowMutationAudit(r, limiter)
				if err != nil {
					writeAuditUnavailable(w, r)
					return
				}
				if !allowed {
					w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
					writeFrameworkProblem(w, r, stdhttp.StatusTooManyRequests, "Too Many Requests", "rate_limited", "Too many mutation requests were received. Try again later.")
					return
				}
				if err := ensureAuditRequestID(r); err != nil {
					writeAuditUnavailable(w, r)
					return
				}
				auditCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				err = recordHTTPMutationIntent(auditCtx, recorder, sourceKey, targetType, r)
				cancel()
				if err != nil {
					observability.Log(logger, "http.audit_intent_failed",
						observability.SafeString("request_id", middleware.GetReqID(r.Context())),
						zap.String("method", r.Method))
					writeAuditUnavailable(w, r)
					return
				}
			}

			var transaction operations.MutationAuditTransaction
			destination := stdhttp.ResponseWriter(w)
			var buffered *bufferedMutationResponse
			if mutation && coordinator != nil {
				transactionCtx, begun, err := coordinator.BeginMutation(r.Context())
				if err != nil {
					writeAuditUnavailable(w, r)
					return
				}
				transaction = begun
				defer func() { _ = transaction.Rollback() }()
				*r = *r.WithContext(transactionCtx)
				buffered = newBufferedMutationResponse()
				destination = buffered
			}
			observed := newAuditResponseWriter(destination)
			next.ServeHTTP(observed, r)
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				// A cooperative handler may have changed state in the lazy request
				// transaction before noticing the deadline. Never turn that canceled
				// request into a committed 2xx merely because its buffered response is
				// still empty. Roll back first, record the failed outcome outside the
				// canceled context, and publish a fresh problem response without any
				// buffered cookie or body.
				if transaction != nil {
					_ = transaction.Rollback()
				}
				if mutation {
					// A rolled-back request context still carries its closed lazy
					// transaction. A failure outcome is a separate durable write.
					auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					event, eventErr := httpRequestAuditEvent(sourceKey, r, stdhttp.StatusServiceUnavailable)
					if eventErr == nil {
						eventErr = recorder.Record(auditCtx, event)
					}
					cancel()
					if eventErr != nil {
						observability.Log(logger, "http.audit_persist_failed",
							observability.SafeString("request_id", middleware.GetReqID(r.Context())),
							observability.SafeString("action", auditAction(r.Method, chi.RouteContext(r.Context()).RoutePattern())))
					}
				}
				setSecurityHeaders(w.Header())
				w.Header().Set("Retry-After", "5")
				writeFrameworkProblem(w, r, stdhttp.StatusServiceUnavailable, "Service Unavailable", "service_unavailable", "The service could not complete the request before its deadline.")
				return
			}
			status := observed.status
			if status == 0 {
				status = stdhttp.StatusOK
			}
			pattern := chi.RouteContext(r.Context()).RoutePattern()
			if shouldAuditRequest(r, pattern, status) {
				auditCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
				event, eventErr := httpRequestAuditEvent(sourceKey, r, status)
				if r.URL.Path == "/graphql" && mutation && graphQLMutationOutcomeFailed(r.Context()) {
					event.Outcome = operations.AuditFailed
				}
				var err error
				if eventErr != nil {
					err = eventErr
				} else if transaction != nil && ((status < 400 && !(r.URL.Path == "/graphql" && graphQLMutationOutcomeFailed(r.Context()))) || (transaction.Active() && transaction.CommitFailure())) {
					err = transaction.Enqueue(auditCtx, event)
					if err == nil {
						err = transaction.Commit()
					}
					if err != nil {
						_ = transaction.Rollback()
						cancel()
						writeAuditUnavailable(w, r)
						return
					}
				} else {
					if transaction != nil {
						_ = transaction.Rollback()
					}
					standaloneCtx, standaloneCancel := context.WithTimeout(context.Background(), 2*time.Second)
					err = recorder.Record(standaloneCtx, event)
					standaloneCancel()
				}
				cancel()
				if err != nil {
					// The durable intent remains available for reconciliation when an
					// outcome cannot be appended after the response was committed.
					observability.Log(logger, "http.audit_persist_failed",
						observability.SafeString("request_id", middleware.GetReqID(r.Context())),
						observability.SafeString("action", auditAction(r.Method, chi.RouteContext(r.Context()).RoutePattern())))
				}
			}
			if buffered != nil {
				buffered.Flush(w)
			}
		})
	}
}

type bufferedMutationResponse struct {
	header stdhttp.Header
	status int
	body   bytes.Buffer
}

func newBufferedMutationResponse() *bufferedMutationResponse {
	return &bufferedMutationResponse{header: make(stdhttp.Header)}
}

func (response *bufferedMutationResponse) Header() stdhttp.Header { return response.header }
func (response *bufferedMutationResponse) WriteHeader(status int) {
	if response.status == 0 {
		response.status = status
	}
}
func (response *bufferedMutationResponse) Write(value []byte) (int, error) {
	if response.status == 0 {
		response.status = stdhttp.StatusOK
	}
	return response.body.Write(value)
}
func (response *bufferedMutationResponse) Flush(destination stdhttp.ResponseWriter) {
	for key, values := range response.header {
		destination.Header()[key] = append([]string(nil), values...)
	}
	status := response.status
	if status == 0 {
		status = stdhttp.StatusOK
	}
	destination.WriteHeader(status)
	_, _ = destination.Write(response.body.Bytes())
}

func writeAuditUnavailable(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	w.Header().Set("Retry-After", "5")
	writeFrameworkProblem(w, r, stdhttp.StatusServiceUnavailable, "Service Unavailable", "service_unavailable", "The service cannot safely process this mutation right now.")
}

type auditRequestIDContextKey struct{}

func auditRequestID(r *stdhttp.Request) string {
	requestID, _ := r.Context().Value(auditRequestIDContextKey{}).(string)
	return requestID
}

func ensureAuditRequestID(r *stdhttp.Request) error {
	if middleware.GetReqID(r.Context()) != "" {
		return nil
	}
	requestID, err := operations.NewAuditEventID()
	if err != nil {
		return err
	}
	*r = *r.WithContext(context.WithValue(r.Context(), middleware.RequestIDKey, requestID))
	return nil
}

func isMutationRequest(method, path string) bool {
	_, ok := mutationRequestTarget(method, path)
	return ok
}

func mutationRequestTargetForRequest(r *stdhttp.Request) (string, bool) {
	if r.URL.Path == "/graphql" {
		metadata, ok := graph.OperationMetadataFromContext(r.Context())
		if !ok || metadata.Type != ast.Mutation {
			return "", false
		}
		return "graphql", true
	}
	return mutationRequestTarget(r.Method, r.URL.Path)
}

func mutationRequestTarget(method, path string) (string, bool) {
	patterns := mutationRoutePatterns[method]
	for _, pattern := range patterns {
		if matchRoutePath(pattern.path, path) {
			return pattern.target, true
		}
	}
	return "", false
}

type mutationRoutePattern struct {
	path   string
	target string
}

var mutationRoutePatterns = map[string][]mutationRoutePattern{
	stdhttp.MethodPost: {
		{"/v1/auth/magic-link/request", "authentication"}, {"/v1/auth/magic-link/consume", "authentication"},
		{"/v1/auth/passkey/register/options", "authentication"}, {"/v1/auth/passkey/register/finish", "authentication"},
		{"/v1/auth/passkey/sign-in/options", "authentication"}, {"/v1/auth/passkey/sign-in/finish", "authentication"},
		{"/v1/subjects", "subject"}, {"/v1/subjects/{subject}/provider-connections", "provider_connection"},
		{"/v1/subjects/{subject}/provider-connections/{connectionId}/sync", "provider_connection"},
		{"/v1/subjects/{subject}/custom-providers", "custom_provider"},
		{"/v1/subjects/{subject}/custom-providers/{customProviderId}/rotate-key", "custom_provider"},
		{"/v1/custom-providers/{customProviderId}/activities:ingest", "custom_provider"},
	},
	stdhttp.MethodPatch: {
		{"/v1/me/settings", "subject"}, {"/v1/subjects/{subject}", "subject"},
		{"/v1/subjects/{subject}/settings", "subject"},
		{"/v1/subjects/{subject}/provider-connections/{connectionId}", "provider_connection"},
		{"/v1/subjects/{subject}/custom-providers/{customProviderId}", "custom_provider"},
	},
	stdhttp.MethodDelete: {
		{"/v1/auth/session", "authentication"}, {"/v1/auth/sessions", "authentication"},
		{"/v1/auth/sessions/{sessionId}", "authentication"}, {"/v1/subjects/{subject}", "subject"},
		{"/v1/subjects/{subject}/provider-connections/{connectionId}", "provider_connection"},
		{"/v1/subjects/{subject}/custom-providers/{customProviderId}", "custom_provider"},
	},
	stdhttp.MethodGet: {{"/v1/integrations/{provider}/callback", "provider_connection"}},
}

func matchRoutePath(pattern, path string) bool {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if strings.HasPrefix(want[index], "{") && strings.HasSuffix(want[index], "}") {
			if got[index] == "" {
				return false
			}
			continue
		}
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

func allowMutationAudit(r *stdhttp.Request, limiter handlers.RateLimiter) (bool, time.Duration, error) {
	if limiter == nil {
		return true, 0, nil
	}
	keys := []struct{ scope, key string }{{"mutation_intent_ip", opaqueMutationIdentity("ip", normalizedRemoteIP(r.RemoteAddr))}}
	if token := requestSessionToken(r); token != "" {
		keys = append(keys, struct{ scope, key string }{"mutation_intent_session", opaqueMutationIdentity("session", token)})
	}
	var longest time.Duration
	for _, bucket := range keys {
		allowed, retryAfter, err := limiter.Allow(r.Context(), bucket.scope, bucket.key)
		if err != nil {
			return false, 0, err
		}
		if !allowed {
			if retryAfter > longest {
				longest = retryAfter
			}
			return false, longest, nil
		}
	}
	return true, 0, nil
}

func requestSessionToken(r *stdhttp.Request) string {
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return strings.TrimSpace(authorization[len("Bearer "):])
	}
	if cookie, err := r.Cookie("jandibat_session"); err == nil {
		return cookie.Value
	}
	return ""
}

func opaqueMutationIdentity(kind, value string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + value))
	return kind + ":sha256:" + hex.EncodeToString(digest[:])
}

func normalizedRemoteIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	if parsed := net.ParseIP(strings.TrimSpace(host)); parsed != nil {
		return parsed.String()
	}
	return "unknown"
}

func recordHTTPMutationIntent(ctx context.Context, recorder handlers.AuditRecorder, sourceKey []byte, targetType string, r *stdhttp.Request) error {
	id, err := operations.NewAuditEventID()
	if err != nil {
		return err
	}
	requestID := auditRequestID(r)
	if requestID == "" {
		return operations.ErrInvalidAuditEvent
	}
	return recorder.Record(ctx, operations.AuditEvent{
		ID:         id,
		OccurredAt: time.Now().UTC(),
		Actor:      operations.AuditActor{Type: operations.AuditActorAnonymous},
		Action:     "http.mutation.intent",
		Target:     operations.AuditTarget{Type: targetType},
		Outcome:    operations.AuditSucceeded,
		RequestID:  requestID,
		SourceIP:   auditSourceIP(r.RemoteAddr, sourceKey),
		Metadata: map[string]any{
			"method": r.Method,
			"phase":  "intent",
		},
	})
}

func shouldAuditRequest(r *stdhttp.Request, pattern string, _ int) bool {
	// Every durable HTTP outcome must have passed the shared mutation-intent
	// limiter and produced a correlated intent first. Persisting denials for
	// ordinary reads would otherwise let repeated GET 401/403/429 responses grow
	// audit_events without any distributed pre-gate. Authentication ceremonies
	// and the OAuth callback are included in mutationRoutePatterns.
	_, knownMutation := mutationRequestTargetForRequest(r)
	return knownMutation
}

// gqlgen's root-field hook reports typed payload errors before field
// selection. The client cannot hide them by omitting or aliasing `errors`.
func graphQLMutationOutcomeFailed(ctx context.Context) bool {
	outcome, ok := graph.OperationOutcomeFromContext(ctx)
	return !ok || !outcome.Executed || outcome.Failed
}

func httpRequestAuditEvent(sourceKey []byte, r *stdhttp.Request, status int) (operations.AuditEvent, error) {
	id, err := operations.NewAuditEventID()
	if err != nil {
		return operations.AuditEvent{}, err
	}
	pattern := chi.RouteContext(r.Context()).RoutePattern()
	if pattern == "" {
		pattern = "unmatched"
	}
	action := auditAction(r.Method, pattern)
	actor, ok := handlers.AuditActorFromContext(r.Context())
	if !ok {
		actor, ok = graphqlAuditActorFromContext(r.Context())
	}
	if !ok {
		actor = operations.AuditActor{Type: operations.AuditActorAnonymous}
	}
	target := operations.AuditTarget{Type: auditTargetType(pattern)}
	if canonical, ok := handlers.AuditTargetFromContext(r.Context()); ok && canonical.ID != "" {
		target = canonical
	} else {
		params := chi.RouteContext(r.Context()).URLParams.Values
		if len(params) > 0 {
			target.ID = params[len(params)-1]
		}
	}
	outcome := operations.AuditSucceeded
	if status == stdhttp.StatusUnauthorized || status == stdhttp.StatusForbidden || status == stdhttp.StatusTooManyRequests {
		outcome = operations.AuditDenied
	} else if status >= 400 {
		outcome = operations.AuditFailed
	}
	requestID := auditRequestID(r)
	if requestID == "" {
		requestID = middleware.GetReqID(r.Context())
		if requestID == "" {
			requestID = id
		}
	}
	return operations.AuditEvent{
		ID: id, OccurredAt: time.Now().UTC(), Actor: actor,
		Action: action, Target: target,
		Outcome: outcome, RequestID: requestID, SourceIP: auditSourceIP(r.RemoteAddr, sourceKey),
		Metadata: map[string]any{"method": r.Method, "route": pattern, "status": status, "phase": "outcome"},
	}, nil
}

func auditAction(method, pattern string) string {
	value := strings.ToLower(method) + "." + strings.Trim(pattern, "/")
	replacer := strings.NewReplacer("/", ".", "{", "", "}", "", ":", ".")
	value = replacer.Replace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}

func auditTargetType(pattern string) string {
	switch {
	case strings.Contains(pattern, "/auth/"):
		return "authentication"
	case strings.Contains(pattern, "provider-connections"):
		return "provider_connection"
	case strings.Contains(pattern, "custom-providers"):
		return "custom_provider"
	case strings.Contains(pattern, "/subjects"):
		return "subject"
	case strings.Contains(pattern, "sync-jobs"):
		return "sync_job"
	case pattern == "/graphql":
		return "graphql"
	default:
		return "http_resource"
	}
}

func auditSourceIP(remoteAddress string, key []byte) string {
	if len(key) == 0 {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	parsed := net.ParseIP(strings.TrimSpace(host))
	if parsed == nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parsed.String()))
	return "ip_" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func securityHeaders(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		setSecurityHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(header stdhttp.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
}

func requestIDHeader(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if requestID := middleware.GetReqID(r.Context()); requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}
		next.ServeHTTP(w, r)
	})
}

const maxRequestIDLength = 128

// canonicalRequestID prevents attacker-controlled correlation IDs from
// reaching response JSON, logs, or the bounded audit schema. Chi deliberately
// trusts inbound X-Request-ID, so validation must immediately follow its
// RequestID middleware and precede every response-producing middleware.
func canonicalRequestID(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		requestID := middleware.GetReqID(r.Context())
		if !validRequestID(requestID) {
			var err error
			requestID, err = operations.NewAuditEventID()
			if err != nil {
				requestID = "req-" + strconv.FormatUint(middleware.NextRequestID(), 10)
			}
		}
		r.Header.Set(middleware.RequestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), middleware.RequestIDKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-._/:", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func cors(allowed []string) func(stdhttp.Handler) stdhttp.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		set[strings.TrimSpace(origin)] = struct{}{}
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			origin := r.Header.Get("Origin")
			if _, ok := set[origin]; ok && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
				if r.Method == stdhttp.MethodOptions {
					w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,Idempotency-Key,X-Jandibat-Provider-Key")
					w.WriteHeader(stdhttp.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func csrf(allowed []string) func(stdhttp.Handler) stdhttp.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		if normalized := strings.TrimSpace(origin); normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r.Method == stdhttp.MethodGet || r.Method == stdhttp.MethodHead || r.Method == stdhttp.MethodOptions || (r.URL.Path != "/graphql" && r.Header.Get("Authorization") != "") {
				next.ServeHTTP(w, r)
				return
			}
			if _, err := r.Cookie("jandibat_session"); err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := set[r.Header.Get("Origin")]; !ok {
				writeFrameworkProblem(w, r, stdhttp.StatusForbidden, "Forbidden", "csrf", "The request origin is not allowed for cookie authentication.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
