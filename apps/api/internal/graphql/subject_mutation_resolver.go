package graphql

import (
	"context"
	"errors"

	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

// SubjectMutationServices contains only application ports. The trusted HTTP
// boundary installs it; no GraphQL argument may choose a repository or actor.
type SubjectMutationServices struct {
	Subjects interface {
		UpdateUserSettings(context.Context, string, subjects.UpdateUserSettingsInput) (subjects.UserSettings, error)
		CreateSubject(context.Context, string, subjects.CreateSubjectInput) (subjects.Subject, error)
		UpdateSubject(context.Context, string, string, subjects.UpdateSubjectInput) (subjects.Subject, error)
		UpdateSubjectSettings(context.Context, string, string, subjects.UpdateSubjectSettingsInput) (subjects.SubjectSettings, error)
		AuthorizeSubjectDeletion(context.Context, string, string) (subjects.Subject, error)
		GetSubject(context.Context, string, string) (subjects.Subject, error)
	}
	Deletions interface {
		Request(context.Context, string, operations.DeletionTargetType, string) (operations.DeletionRequest, error)
	}
}

type subjectMutationServicesContextKey struct{}

func ContextWithSubjectMutationServices(ctx context.Context, services SubjectMutationServices) context.Context {
	return context.WithValue(ctx, subjectMutationServicesContextKey{}, services)
}

func trustedSubjectMutation(ctx context.Context) (string, SubjectMutationServices, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" {
		return "", SubjectMutationServices{}, errNodeAuthentication
	}
	services, _ := ctx.Value(subjectMutationServicesContextKey{}).(SubjectMutationServices)
	if services.Subjects == nil {
		return "", SubjectMutationServices{}, errNodeLookup
	}
	return identity.userID, services, nil
}

func subjectMutationError(err error, field string) ([]*model.MutationError, error) {
	switch {
	case errors.Is(err, subjects.ErrInvalidInput), errors.Is(err, operations.ErrInvalidDeletionRequest):
		return authMutationError("BAD_USER_INPUT", "Invalid input.", field), nil
	case errors.Is(err, subjects.ErrNotFound), errors.Is(err, subjects.ErrForbidden):
		return authMutationError("NOT_FOUND", "Subject not found.", field), nil
	case errors.Is(err, subjects.ErrConflict):
		return authMutationError("CONFLICT", "Subject conflicts with an existing record.", field), nil
	case errors.Is(err, subjects.ErrUnauthenticated):
		return nil, errNodeAuthentication
	default:
		return nil, errNodeLookup
	}
}

func decodeSubjectMutationID(globalID, field string) (string, []*model.MutationError) {
	rawID, err := relayid.DecodeAs(relayid.Subject, globalID)
	if err != nil {
		return "", authMutationError("BAD_USER_INPUT", "Invalid subject ID.", field)
	}
	return rawID, nil
}

func domainTheme(value model.HeatmapTheme) (subjects.HeatmapTheme, bool) {
	switch value {
	case model.HeatmapThemeSystem:
		return subjects.ThemeSystem, true
	case model.HeatmapThemeLight:
		return subjects.ThemeLight, true
	case model.HeatmapThemeDark:
		return subjects.ThemeDark, true
	case model.HeatmapThemeGithubLight:
		return subjects.ThemeGitHubLight, true
	case model.HeatmapThemeGithubDark:
		return subjects.ThemeGitHubDark, true
	default:
		return "", false
	}
}

func domainWeekStart(value model.WeekStart) (subjects.WeekStart, bool) {
	switch value {
	case model.WeekStartSunday:
		return subjects.WeekStartSunday, true
	case model.WeekStartMonday:
		return subjects.WeekStartMonday, true
	default:
		return "", false
	}
}

func domainFailurePolicy(value model.FetchFailurePolicy) (subjects.FailurePolicy, bool) {
	switch value {
	case model.FetchFailurePolicyKeepStale:
		return subjects.FailureKeepStale, true
	case model.FetchFailurePolicyPurge:
		return subjects.FailurePurge, true
	default:
		return "", false
	}
}

func resolveUpdateUserSettings(ctx context.Context, input model.UpdateUserSettingsInput) (*model.UpdateUserSettingsPayload, error) {
	actor, services, err := trustedSubjectMutation(ctx)
	if err != nil {
		return nil, err
	}
	var timezone *string
	if input.Timezone != nil {
		value := string(*input.Timezone)
		timezone = &value
	}
	var theme *subjects.HeatmapTheme
	if input.Theme != nil {
		value, ok := domainTheme(*input.Theme)
		if !ok {
			return &model.UpdateUserSettingsPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid theme.", "theme")}, nil
		}
		theme = &value
	}
	settings, err := services.Subjects.UpdateUserSettings(ctx, actor, subjects.UpdateUserSettingsInput{Locale: input.Locale, Timezone: timezone, Theme: theme})
	if err != nil {
		validation, publicErr := subjectMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.UpdateUserSettingsPayload{Errors: validation}, nil
	}
	projected, err := projectUserSettings(settings)
	if err != nil {
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "account", actor)
	return &model.UpdateUserSettingsPayload{Errors: []*model.MutationError{}, Settings: projected}, nil
}

func resolveCreateSubject(ctx context.Context, input model.CreateSubjectInput) (*model.CreateSubjectPayload, error) {
	actor, services, err := trustedSubjectMutation(ctx)
	if err != nil {
		return nil, err
	}
	subject, err := services.Subjects.CreateSubject(ctx, actor, subjects.CreateSubjectInput{
		Handle: input.Handle, DisplayName: input.DisplayName, Timezone: string(input.Timezone), IsPublic: input.IsPublic,
	})
	if err != nil {
		validation, publicErr := subjectMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.CreateSubjectPayload{Errors: validation}, nil
	}
	if subject.OwnerUserID != actor {
		return nil, errNodeLookup
	}
	projected, err := projectSubject(subject)
	if err != nil {
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "subject", subject.ID)
	return &model.CreateSubjectPayload{Errors: []*model.MutationError{}, Subject: projected}, nil
}

func resolveUpdateSubject(ctx context.Context, input model.UpdateSubjectInput) (*model.UpdateSubjectPayload, error) {
	actor, services, err := trustedSubjectMutation(ctx)
	if err != nil {
		return nil, err
	}
	rawID, idErrors := decodeSubjectMutationID(input.ID, "id")
	if idErrors != nil {
		return &model.UpdateSubjectPayload{Errors: idErrors}, nil
	}
	if input.ClearDisplayName != nil && *input.ClearDisplayName && input.DisplayName != nil {
		return &model.UpdateSubjectPayload{Errors: authMutationError("BAD_USER_INPUT", "displayName and clearDisplayName are mutually exclusive.", "clearDisplayName")}, nil
	}
	update := subjects.UpdateSubjectInput{Handle: input.Handle, DisplayNameSet: input.DisplayName != nil, DisplayName: input.DisplayName}
	if input.ClearDisplayName != nil && *input.ClearDisplayName {
		update.DisplayNameSet = true
		update.DisplayName = nil
	}
	subject, err := services.Subjects.UpdateSubject(ctx, actor, rawID, update)
	if err != nil {
		validation, publicErr := subjectMutationError(err, "id")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.UpdateSubjectPayload{Errors: validation}, nil
	}
	if subject.ID != rawID || subject.OwnerUserID != actor {
		return nil, errNodeLookup
	}
	projected, err := projectSubject(subject)
	if err != nil {
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "subject", subject.ID)
	return &model.UpdateSubjectPayload{Errors: []*model.MutationError{}, Subject: projected}, nil
}

func resolveUpdateSubjectSettings(ctx context.Context, input model.UpdateSubjectSettingsInput) (*model.UpdateSubjectSettingsPayload, error) {
	actor, services, err := trustedSubjectMutation(ctx)
	if err != nil {
		return nil, err
	}
	rawID, idErrors := decodeSubjectMutationID(input.SubjectID, "id")
	if idErrors != nil {
		return &model.UpdateSubjectSettingsPayload{Errors: idErrors}, nil
	}
	var timezone *string
	if input.Timezone != nil {
		value := string(*input.Timezone)
		timezone = &value
	}
	var theme *subjects.HeatmapTheme
	if input.DefaultTheme != nil {
		value, ok := domainTheme(*input.DefaultTheme)
		if !ok {
			return &model.UpdateSubjectSettingsPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid theme.", "defaultTheme")}, nil
		}
		theme = &value
	}
	var weekStart *subjects.WeekStart
	if input.WeekStart != nil {
		value, ok := domainWeekStart(*input.WeekStart)
		if !ok {
			return &model.UpdateSubjectSettingsPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid week start.", "weekStart")}, nil
		}
		weekStart = &value
	}
	var failurePolicy *subjects.FailurePolicy
	if input.FailurePolicy != nil {
		value, ok := domainFailurePolicy(*input.FailurePolicy)
		if !ok {
			return &model.UpdateSubjectSettingsPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid failure policy.", "failurePolicy")}, nil
		}
		failurePolicy = &value
	}
	settings, err := services.Subjects.UpdateSubjectSettings(ctx, actor, rawID, subjects.UpdateSubjectSettingsInput{
		Timezone: timezone, IsPublic: input.IsPublic, DefaultTheme: theme, WeekStart: weekStart,
		SyncEnabled: input.SyncEnabled, SyncIntervalMinutes: input.SyncIntervalMinutes, FailurePolicy: failurePolicy,
	})
	if err != nil {
		validation, publicErr := subjectMutationError(err, "subjectID")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.UpdateSubjectSettingsPayload{Errors: validation}, nil
	}
	if settings.SubjectID != rawID {
		return nil, errNodeLookup
	}
	subject, err := services.Subjects.GetSubject(ctx, actor, rawID)
	if err != nil || subject.ID != rawID || subject.OwnerUserID != actor {
		return nil, errNodeLookup
	}
	projectedSettings, err := projectSubjectSettings(settings)
	if err != nil {
		return nil, errNodeLookup
	}
	projectedSubject, err := projectSubject(subject)
	if err != nil {
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "subject", subject.ID)
	return &model.UpdateSubjectSettingsPayload{Errors: []*model.MutationError{}, Settings: projectedSettings, Subject: projectedSubject}, nil
}

func resolveRequestSubjectDeletion(ctx context.Context, input model.RequestSubjectDeletionInput) (*model.RequestSubjectDeletionPayload, error) {
	actor, services, err := trustedSubjectMutation(ctx)
	if err != nil {
		return nil, err
	}
	rawID, idErrors := decodeSubjectMutationID(input.SubjectID, "subjectID")
	if idErrors != nil {
		return &model.RequestSubjectDeletionPayload{Errors: idErrors}, nil
	}
	if services.Deletions == nil {
		return nil, errNodeLookup
	}
	subject, err := services.Subjects.AuthorizeSubjectDeletion(ctx, actor, rawID)
	if err != nil {
		validation, publicErr := subjectMutationError(err, "subjectID")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.RequestSubjectDeletionPayload{Errors: validation}, nil
	}
	if subject.ID != rawID || subject.OwnerUserID != actor {
		return nil, errNodeLookup
	}
	requestID, err := operations.NewAuditEventID()
	if err != nil {
		return nil, errNodeLookup
	}
	request, err := services.Deletions.Request(ctx, requestID, operations.DeletionTargetSubject, subject.ID)
	if err != nil {
		if errors.Is(err, operations.ErrLegalHoldActive) {
			return &model.RequestSubjectDeletionPayload{Errors: authMutationError("LEGAL_HOLD_ACTIVE", "An active legal hold prevents deletion.", "subjectID")}, nil
		}
		if errors.Is(err, operations.ErrInvalidDeletionRequest) {
			return &model.RequestSubjectDeletionPayload{Errors: authMutationError("DELETION_REQUEST_CONFLICT", "Deletion request conflicts with an existing request.", "subjectID")}, nil
		}
		validation, publicErr := subjectMutationError(err, "subjectID")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.RequestSubjectDeletionPayload{Errors: validation}, nil
	}
	if request.RequestID == "" || request.TargetType != operations.DeletionTargetSubject || request.TargetID != subject.ID || request.Status != operations.DeletionRequested {
		return &model.RequestSubjectDeletionPayload{Errors: authMutationError("DELETION_REQUEST_CONFLICT", "Deletion request conflicts with an existing request.", "subjectID")}, nil
	}
	PublishMutationAuditTarget(ctx, "subject", subject.ID)
	return &model.RequestSubjectDeletionPayload{Errors: []*model.MutationError{}, Request: &model.DeletionRequestResult{
		RequestID: request.RequestID, Status: string(request.Status),
	}}, nil
}
