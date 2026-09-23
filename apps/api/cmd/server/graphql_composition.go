package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	"github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
	"go.uber.org/zap"
)

// graphqlFailureReporter intentionally accepts only a fixed event and no
// service error or user-controlled input. This keeps compensation diagnostics
// useful without making credentials, recipients, or redirects loggable.
type graphqlFailureReporter struct{ logger *zap.Logger }

func (reporter graphqlFailureReporter) ReportMagicLinkRequestFailure(context.Context) {
	reporter.logger.Error("graphql.auth.magic_link_request_failed")
}

func (reporter graphqlFailureReporter) ReportSessionCompensationFailure(context.Context) {
	reporter.logger.Error("graphql.auth.session_compensation_failed")
}

func (reporter graphqlFailureReporter) ReportOAuthCompensationFailure(context.Context) {
	reporter.logger.Error("graphql.integration.oauth_compensation_failed")
}

type unavailableDeletionRequester struct{}

func (unavailableDeletionRequester) Request(context.Context, string, operations.DeletionTargetType, string) (operations.DeletionRequest, error) {
	return operations.DeletionRequest{}, errors.New("durable deletion requests are unavailable")
}

func buildGraphQLDependencies(settings config.Config, logger *zap.Logger, subjectService *subjects.Service, authService *auth.Service, timeline *activity.GetTimeline, connections *integrations.ConnectionService, customProviders *integrations.CustomProviderService, syncService *integrations.SyncService, deletions *operations.DeletionRequester, oauthFlows map[string]oauth.Flow) (apihttp.GraphQLDependencies, error) {
	if settings.Environment == config.EnvironmentProduction && deletions == nil {
		return apihttp.GraphQLDependencies{}, fmt.Errorf("runtime: durable GraphQL deletion requester is required in production")
	}
	var deletionPort graphql.SubjectMutationServices
	deletionPort.Subjects = subjectService
	if deletions != nil {
		deletionPort.Deletions = deletions
	} else {
		deletionPort.Deletions = unavailableDeletionRequester{}
	}
	reporter := graphqlFailureReporter{logger: logger}
	return apihttp.GraphQLDependencies{
		NodeServices: graphql.NodeServices{
			ViewerUsers: subjectService, SessionPages: authService, Subjects: subjectService,
			Connections: connections, CustomProviders: customProviders, SyncJobs: syncService,
			Sessions: authService, Activity: timeline,
		},
		SubjectQueries: graphql.SubjectQueryServices{
			Pages: subjectService, UserSettings: subjectService, SubjectSettings: subjectService,
		},
		SubjectMutations:        deletionPort,
		IntegrationQueries:      graphql.IntegrationQueryServices{Connections: connections, CustomProviders: customProviders},
		ConnectionMutations:     graphql.ConnectionMutationServices{Connections: connections, Sync: syncService, Failures: reporter},
		CustomProviderMutations: graphql.CustomProviderMutationServices{Providers: customProviders},
		SyncJobs:                syncService,
		AuthAccounts:            authService,
		Passkeys:                authService,
		AuthFailureReporter:     reporter,
		OAuthFlows:              oauthFlows,
		Development:             settings.Environment == config.EnvironmentDevelopment,
	}, nil
}

func newProcessHandlerWithGraphQL(app *application, liveness operations.Liveness) (http.Handler, error) {
	if app == nil {
		return nil, errors.New("runtime: application is unavailable")
	}
	router, err := apihttp.NewRouterWithGraphQL(app.dependencies, app.graphql)
	if err != nil {
		return nil, err
	}
	health := processruntime.NewHealthHandler(liveness, app.dependencies.Readiness)
	mux := http.NewServeMux()
	mux.Handle("/livez", health)
	mux.Handle("/readyz", health)
	mux.Handle("/", router)
	return mux, nil
}
