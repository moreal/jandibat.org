package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	authstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach"
	webauthnadapter "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/webauthn"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	oauthstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/syncer"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	provideradapter "github.com/moreal/jandibat.org/apps/api/internal/adapters/provider"
	ratelimitstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/ratelimit/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	activitymemory "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/memory"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	activityapp "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
	"go.uber.org/zap"
)

var (
	errDatabaseRequired   = errors.New("runtime: DATABASE_URL is required in production")
	errOAuthRequired      = errors.New("runtime: complete OAuth configuration is required in production")
	errHTTPSRequired      = errors.New("runtime: production public and web URLs must use HTTPS")
	errAllInOneProduction = errors.New("runtime: development all-in-one mode is forbidden in production")
)

const (
	providerHTTPTimeout = 15 * time.Second
)

// application owns the process-level dependency graph and every resource with
// an explicit lifetime. HTTP handlers receive only Dependencies; background
// work and cleanup stay at the process boundary.
type application struct {
	dependencies apihttp.Dependencies
	scheduler    *integrations.Scheduler
	background   []namedBackgroundRunner
	oauth        *oauthRegistry
	close        func() error
}

func (app *application) Close() error {
	if app == nil || app.close == nil {
		return nil
	}
	return app.close()
}

type oauthRegistry struct {
	flows map[string]oauth.Flow
}

func newOAuthRegistry(flows ...oauth.Flow) (*oauthRegistry, error) {
	registry := &oauthRegistry{flows: make(map[string]oauth.Flow, len(flows))}
	for _, flow := range flows {
		if flow == nil || strings.TrimSpace(flow.ProviderID()) == "" {
			return nil, fmt.Errorf("runtime: invalid OAuth flow")
		}
		if _, exists := registry.flows[flow.ProviderID()]; exists {
			return nil, fmt.Errorf("runtime: duplicate OAuth flow %q", flow.ProviderID())
		}
		registry.flows[flow.ProviderID()] = flow
	}
	return registry, nil
}

func (registry *oauthRegistry) Lookup(providerID string) (oauth.Flow, bool) {
	if registry == nil {
		return nil, false
	}
	flow, ok := registry.flows[providerID]
	return flow, ok
}

func (registry *oauthRegistry) RevokeOAuthToken(ctx context.Context, providerID string, token []byte) error {
	flow, ok := registry.Lookup(providerID)
	if !ok {
		// A provider without a configured OAuth application cannot safely make
		// an authenticated revocation request. Local deletion still proceeds.
		return nil
	}
	revoker, ok := flow.(oauth.TokenRevoker)
	if !ok {
		return nil
	}
	return revoker.RevokeToken(ctx, token)
}

type databaseStores struct {
	pool         *pgxpool.Pool
	activity     activityapp.Store
	integrations *integrationstore.Store
	operations   *operationsstore.Store
	close        func() error
}

func buildApplication(ctx context.Context, settings config.Config, logger *zap.Logger) (*application, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if err := validateRuntimeConfig(settings); err != nil {
		return nil, err
	}

	stores, err := buildStores(ctx, settings)
	if err != nil {
		return nil, err
	}
	cleanup := stores.close
	success := false
	defer func() {
		if !success && cleanup != nil {
			_ = cleanup()
		}
	}()
	readiness, err := buildReadinessChecker(stores)
	if err != nil {
		return nil, err
	}
	subjectRepository := subjects.Repository(subjects.NewMemoryRepository())
	if stores.pool != nil {
		subjectRepository, err = subjectstore.New(stores.pool)
		if err != nil {
			return nil, fmt.Errorf("runtime: construct subject store: %w", err)
		}
	}
	subjectService, err := subjects.NewService(subjectRepository, subjects.Config{})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct subject service: %w", err)
	}
	subjectSettings := runtimeSubjectSettings{repository: subjectRepository}

	client := newProviderHTTPClient()
	providers, environments, err := buildTimelineProviders(client)
	if err != nil {
		return nil, err
	}
	if err := stores.activity.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: environments}); err != nil {
		return nil, fmt.Errorf("runtime: seed provider environments: %w", err)
	}
	timeline, err := activityapp.NewGetTimeline(stores.activity, providers, activityapp.GetTimelineOptions{
		DefaultTimezone: "UTC",
		FailurePolicy:   activity.FetchFailureKeepStale,
		SubjectSettings: subjectSettings,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct timeline service: %w", err)
	}

	var cipher operations.RotatingSecretCipher
	if settings.DevelopmentAllInOne {
		cipher, err = buildCredentialKeyring(settings)
	} else {
		cipher, err = processruntime.BuildCredentialEncryptor(settings)
	}
	if err != nil {
		return nil, err
	}
	cipher = observability.InstrumentCredentialCipher(cipher, observability.Default())
	operational, err := buildOperationalRuntime(settings, stores, cipher, logger)
	if err != nil {
		return nil, err
	}
	var subjectDeletions *operations.DeletionRequester
	if stores.operations != nil {
		subjectDeletions, err = operations.NewDeletionRequester(stores.operations, runtimeClock{})
		if err != nil {
			return nil, fmt.Errorf("runtime: construct subject deletion workflow: %w", err)
		}
	}
	rateLimiter := handlers.RateLimiter(handlers.DefaultRateLimiter())
	if stores.pool != nil {
		rateLimiter, err = ratelimitstore.New(stores.pool, handlers.DefaultRateLimitPolicies(), nil)
		if err != nil {
			return nil, fmt.Errorf("runtime: construct distributed rate limiter: %w", err)
		}
	}

	integrationPersistence := integrations.ConnectionStore(nil)
	customPersistence := integrations.CustomProviderStore(nil)
	jobPersistence := integrations.SyncJobStore(nil)
	if stores.integrations != nil {
		integrationPersistence = stores.integrations
		customPersistence = stores.integrations
		jobPersistence = stores.integrations
	} else {
		memory := integrations.NewMemoryStore()
		integrationPersistence = memory
		customPersistence = memory
		jobPersistence = memory
	}

	customProviders, err := integrations.NewCustomProviderService(customPersistence, cipher, stores.activity, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct custom-provider service: %w", err)
	}
	catalog := integrations.NewCatalogService(customPersistence)

	syncRegistry, err := integrations.NewSyncRegistry()
	if err != nil {
		return nil, fmt.Errorf("runtime: construct enqueue-only sync registry: %w", err)
	}
	if settings.DevelopmentAllInOne {
		syncRegistry, err = buildSyncRegistry(client)
		if err != nil {
			return nil, err
		}
	}
	syncService, err := integrations.NewSyncService(integrationPersistence, jobPersistence, stores.activity, cipher, syncRegistry, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct sync service: %w", err)
	}
	var scheduler *integrations.Scheduler
	if settings.DevelopmentAllInOne {
		scheduler, err = integrations.NewScheduler(integrationPersistence, syncService, nil, integrations.SchedulerConfig{
			SyncInterval:    settings.SchedulerInterval,
			PollInterval:    settings.SchedulerInterval,
			MaxConcurrent:   4,
			SubjectSettings: subjectSettings,
		})
		if err != nil {
			return nil, fmt.Errorf("runtime: construct scheduler: %w", err)
		}
	}

	authRepository := auth.Repository(auth.NewMemoryStore())
	if stores.pool != nil {
		authRepository, err = authstore.NewWithDeletedIdentityHMACKeys(stores.pool, settings.DeletedIdentityHMACKeys)
		if err != nil {
			return nil, fmt.Errorf("runtime: construct auth store: %w", err)
		}
	}
	authService, err := buildAuthService(settings, authRepository, authSecurityAuditRecorder{recorder: operational.audit})
	if err != nil {
		return nil, err
	}
	oauthState := oauth.StateStore(oauth.NewMemoryStateStore(nil))
	if stores.pool != nil {
		oauthState, err = oauthstore.New(stores.pool)
		if err != nil {
			return nil, fmt.Errorf("runtime: construct OAuth state store: %w", err)
		}
	}
	oauthFlows, err := buildOAuthRegistry(settings, client, oauthState)
	if err != nil {
		return nil, err
	}
	var tokenRevoker integrations.OAuthTokenRevoker
	if settings.DevelopmentAllInOne {
		tokenRevoker = oauthFlows
	}
	connections, err := integrations.NewConnectionServiceWithRevoker(integrationPersistence, cipher, nil, nil, tokenRevoker, stores.activity)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct connection service: %w", err)
	}

	background := append([]namedBackgroundRunner(nil), operational.background...)
	if scheduler != nil {
		background = append([]namedBackgroundRunner{{name: "provider scheduler", runner: scheduler}}, background...)
	}
	app := &application{
		dependencies: apihttp.Dependencies{
			Logger:            logger,
			Timeline:          timeline,
			Readiness:         readiness,
			Audit:             operational.audit,
			MutationAudits:    stores.operations,
			AuditSourceKey:    append([]byte(nil), settings.SessionSigningKey...),
			Catalog:           catalog,
			Auth:              authService,
			Sessions:          authService,
			Connections:       connections,
			OAuthConnections:  connections,
			OAuthFlows:        oauthFlows.flows,
			OAuthWebURL:       settings.WebURL.String(),
			AllowedRedirects:  allowedRedirects(settings.WebURL),
			CustomProviders:   customProviders,
			Sync:              syncService,
			SubjectAuthorizer: subjectService,
			SubjectVisibility: subjectService,
			SubjectResolver:   subjectService,
			Subjects:          subjectService,
			SubjectDeletions:  subjectDeletions,
			AllowedOrigins:    []string{origin(settings.WebURL)},
			SecureCookies:     settings.WebURL.Scheme == "https",
			TrustProxyHeaders: settings.TrustProxyHeaders,
			RateLimiter:       rateLimiter,
		},
		scheduler:  scheduler,
		background: background,
		oauth:      oauthFlows,
		close: func() error {
			client.CloseIdleConnections()
			return cleanup()
		},
	}
	success = true
	return app, nil
}

func buildReadinessChecker(stores databaseStores) (*operations.ReadinessChecker, error) {
	name := "local-storage"
	var probe operations.DependencyProbe = operations.DependencyProbeFunc(func(context.Context) error { return nil })
	dependencies := make([]operations.ReadinessDependency, 0, 2)
	if stores.pool != nil {
		name = "database"
		probe = stores.operations
		dependencies = append(dependencies, operations.ReadinessDependency{
			Name: "database-pool", Probe: operations.DependencyProbeFunc(stores.pool.Ping),
		})
	}
	dependencies = append(dependencies, operations.ReadinessDependency{Name: name, Probe: probe})
	if stores.integrations != nil {
		dependencies = append(dependencies, operations.ReadinessDependency{
			Name:  "provider-revocation-schema",
			Probe: operations.DependencyProbeFunc(stores.integrations.CheckRevocationSchema),
		})
	}
	checker, err := operations.NewReadinessChecker(2*time.Second, dependencies...)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct readiness checker: %w", err)
	}
	return checker, nil
}

func validateRuntimeConfig(settings config.Config) error {
	if settings.Environment != config.EnvironmentProduction {
		return nil
	}
	var missing []error
	if settings.DevelopmentAllInOne {
		missing = append(missing, errAllInOneProduction)
	}
	if strings.TrimSpace(settings.DatabaseURL) == "" {
		missing = append(missing, errDatabaseRequired)
	}
	if settings.PublicURL == nil || settings.WebURL == nil || settings.PublicURL.Scheme != "https" || settings.WebURL.Scheme != "https" {
		missing = append(missing, errHTTPSRequired)
	}
	activeCredentialKey, activeKeyExists := settings.CredentialEncryptionPublicKeys[settings.CredentialActiveKeyID]
	if len(settings.SessionSigningKey) < 32 || strings.TrimSpace(settings.CredentialActiveKeyID) == "" || !activeKeyExists || len(activeCredentialKey) == 0 {
		missing = append(missing, config.ErrMissingSecret)
	}
	if len(settings.CredentialEncryptionPrivateKeys) != 0 || len(settings.CredentialEncryptionKeys) != 0 || len(settings.CredentialCipherKey) != 0 {
		missing = append(missing, config.ErrForbiddenSecret)
	}
	if !allBlank(settings.SMTPAddress, settings.SMTPUsername, settings.SMTPPassword, settings.SMTPFrom) {
		missing = append(missing, config.ErrForbiddenSecret)
	}
	if anyBlank(
		settings.GitHubClientID, settings.GitHubClientSecret,
		settings.GitLabClientID, settings.GitLabClientSecret,
		settings.CodebergClientID, settings.CodebergSecret,
	) {
		missing = append(missing, errOAuthRequired)
	}
	return errors.Join(missing...)
}

func anyBlank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func allBlank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func buildStores(ctx context.Context, settings config.Config) (databaseStores, error) {
	if settings.Environment == config.EnvironmentDevelopment && strings.TrimSpace(settings.DatabaseURL) == "" {
		return databaseStores{activity: activitymemory.New(), close: func() error { return nil }}, nil
	}

	databaseURL, err := processruntime.DatabaseURL(settings, config.ProcessAPI)
	if err != nil {
		return databaseStores{}, err
	}
	database, err := processruntime.OpenDatabase(ctx, databaseURL)
	if err != nil {
		return databaseStores{}, err
	}
	activityPersistence, err := activitystore.New(database.Pool)
	if err != nil {
		_ = database.Close()
		return databaseStores{}, fmt.Errorf("runtime: construct activity store: %w", err)
	}
	integrationPersistence, err := integrationstore.New(database.Pool)
	if err != nil {
		_ = database.Close()
		return databaseStores{}, fmt.Errorf("runtime: construct integration store: %w", err)
	}
	operationalPersistence, err := operationsstore.New(database.Pool)
	if err != nil {
		_ = database.Close()
		return databaseStores{}, fmt.Errorf("runtime: construct operations store: %w", err)
	}
	if err := operationalPersistence.Check(ctx); err != nil {
		_ = database.Close()
		return databaseStores{}, fmt.Errorf("runtime: validate operations schema: %w", err)
	}
	return databaseStores{
		pool:         database.Pool,
		activity:     activityPersistence,
		integrations: integrationPersistence,
		operations:   operationalPersistence,
		close:        database.Close,
	}, nil
}

func newProviderHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{
		Transport: observability.InstrumentRoundTripper(transport, observability.Default()),
		Timeout:   providerHTTPTimeout,
		// Provider and OAuth endpoints are fixed at construction. Refusing
		// redirects prevents an upstream from turning those clients into a
		// server-side request primitive for another origin.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func buildTimelineProviders(client *http.Client) ([]activityapp.Provider, []activity.Environment, error) {
	github, err := provideradapter.NewGitHub(provideradapter.GitHubConfig{HTTPClient: client})
	if err != nil {
		return nil, nil, fmt.Errorf("runtime: construct GitHub provider: %w", err)
	}
	gitlab, err := provideradapter.NewGitLab(provideradapter.GitLabConfig{HTTPClient: client})
	if err != nil {
		return nil, nil, fmt.Errorf("runtime: construct GitLab provider: %w", err)
	}
	codeberg, err := provideradapter.NewCodeberg(provideradapter.CodebergConfig{HTTPClient: client})
	if err != nil {
		return nil, nil, fmt.Errorf("runtime: construct Codeberg provider: %w", err)
	}
	providers := []activityapp.Provider{github, gitlab, codeberg}
	environments := make([]activity.Environment, len(providers))
	for index, provider := range providers {
		environments[index] = provider.Environment()
	}
	return providers, environments, nil
}

func buildSyncRegistry(client *http.Client) (*integrations.SyncRegistry, error) {
	github, err := syncer.NewGitHub(syncer.Config{HTTPClient: client, Timezone: "UTC"})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct GitHub syncer: %w", err)
	}
	gitlab, err := syncer.NewGitLab(syncer.Config{HTTPClient: client, Timezone: "UTC"})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct GitLab syncer: %w", err)
	}
	codeberg, err := syncer.NewCodeberg(syncer.Config{HTTPClient: client, Timezone: "UTC"})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct Codeberg syncer: %w", err)
	}
	registry, err := integrations.NewSyncRegistry(github, gitlab, codeberg)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct sync registry: %w", err)
	}
	return registry, nil
}

func buildAuthService(settings config.Config, repository auth.Repository, securityEvents auth.SecurityEventRecorder) (*auth.Service, error) {
	verifier, err := webauthnadapter.New(webauthnadapter.Config{
		RPID:          settings.WebURL.Hostname(),
		RPDisplayName: "jandibat.org",
		RPOrigins:     []string{origin(settings.WebURL)},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct WebAuthn verifier: %w", err)
	}

	deps := auth.Dependencies{Repository: repository, Verifier: verifier, SecurityEvents: securityEvents}
	if deliveries, ok := repository.(auth.MagicLinkDeliveryIssuer); ok {
		deps.Deliveries = deliveries
	} else {
		mailer, mailerErr := processruntime.BuildMagicLinkMailer(settings)
		if mailerErr != nil {
			return nil, mailerErr
		}
		deps.Mailer = mailer
	}

	service, err := auth.NewService(deps, auth.Config{})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct auth service: %w", err)
	}
	return service, nil
}

func buildOAuthRegistry(settings config.Config, client *http.Client, stateStore oauth.StateStore) (*oauthRegistry, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("runtime: OAuth state store is required")
	}
	type definition struct {
		id           string
		clientID     string
		clientSecret string
		construct    func(oauth.Config) (*oauth.Adapter, error)
	}
	definitions := []definition{
		{id: "github", clientID: settings.GitHubClientID, clientSecret: settings.GitHubClientSecret, construct: oauth.NewGitHub},
		{id: "gitlab", clientID: settings.GitLabClientID, clientSecret: settings.GitLabClientSecret, construct: oauth.NewGitLab},
		{id: "codeberg", clientID: settings.CodebergClientID, clientSecret: settings.CodebergSecret, construct: oauth.NewCodeberg},
	}
	flows := make([]oauth.Flow, 0, len(definitions))
	for _, item := range definitions {
		if strings.TrimSpace(item.clientID) == "" && strings.TrimSpace(item.clientSecret) == "" {
			continue
		}
		redirect := cloneURL(settings.PublicURL)
		redirect.Path = strings.TrimRight(redirect.Path, "/") + "/v1/integrations/" + item.id + "/callback"
		redirect.RawQuery = ""
		redirect.Fragment = ""
		flow, err := item.construct(oauth.Config{
			ClientID:              item.clientID,
			ClientSecret:          item.clientSecret,
			RedirectURI:           redirect.String(),
			HTTPClient:            client,
			StateStore:            stateStore,
			RequireSessionBinding: true,
		})
		if err != nil {
			return nil, fmt.Errorf("runtime: construct %s OAuth flow: %w", item.id, err)
		}
		flows = append(flows, flow)
	}
	return newOAuthRegistry(flows...)
}

func origin(value *url.URL) string {
	if value == nil {
		return ""
	}
	return value.Scheme + "://" + value.Host
}

func allowedRedirects(webURL *url.URL) []string {
	base := cloneURL(webURL)
	base.RawQuery = ""
	base.Fragment = ""
	plain := base.String()

	magic := cloneURL(base)
	if magic.Path == "" {
		magic.Path = "/"
	}
	query := magic.Query()
	query.Set("auth", "magic")
	magic.RawQuery = query.Encode()
	magic.Fragment = "auth"
	connections := cloneURL(base)
	if connections.Path == "" {
		connections.Path = "/"
	}
	connections.Fragment = "connections"
	providers := cloneURL(base)
	providers.Path = strings.TrimRight(providers.Path, "/") + "/settings/providers"
	return uniqueStrings(plain, magic.String(), connections.String(), providers.String())
}

func uniqueStrings(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; value == "" || exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return &url.URL{}
	}
	cloned := *value
	return &cloned
}
