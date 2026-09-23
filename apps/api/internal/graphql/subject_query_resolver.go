package graphql

import (
	"context"
	"errors"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

// SubjectQueryServices are trusted application ports installed by the GraphQL
// transport. They are intentionally separate from the Node visibility ports.
type SubjectQueryServices struct {
	Pages interface {
		ListSubjectsPage(context.Context, string, *subjects.SubjectCursor, int) (subjects.SubjectPage, error)
	}
	UserSettings interface {
		GetUserSettings(context.Context, string) (subjects.UserSettings, error)
	}
	SubjectSettings interface {
		GetSubjectSettings(context.Context, string, string) (subjects.SubjectSettings, error)
	}
}

type subjectQueryServicesContextKey struct{}

func ContextWithSubjectQueryServices(ctx context.Context, services SubjectQueryServices) context.Context {
	return context.WithValue(ctx, subjectQueryServicesContextKey{}, services)
}

func projectSubject(subject subjects.Subject) (*model.Subject, error) {
	if subject.ID == "" {
		return nil, errNodeLookup
	}
	return &model.Subject{
		ID:     relayid.Encode(relayid.Subject, subject.ID),
		Handle: subject.Handle, DisplayName: subject.DisplayName,
		Timezone: scalar.TimeZone(subject.Timezone), IsPublic: subject.IsPublic,
		CreatedAt: scalar.DateTime(subject.CreatedAt), UpdatedAt: scalar.DateTime(subject.UpdatedAt),
	}, nil
}

func projectUserSettings(settings subjects.UserSettings) (*model.UserSettings, error) {
	var theme model.HeatmapTheme
	switch settings.Theme {
	case subjects.ThemeSystem:
		theme = model.HeatmapThemeSystem
	case subjects.ThemeLight:
		theme = model.HeatmapThemeLight
	case subjects.ThemeDark:
		theme = model.HeatmapThemeDark
	case subjects.ThemeGitHubLight:
		theme = model.HeatmapThemeGithubLight
	case subjects.ThemeGitHubDark:
		theme = model.HeatmapThemeGithubDark
	default:
		return nil, errNodeLookup
	}
	if settings.Locale == "" || !validStoredTimeZone(settings.Timezone) || settings.UpdatedAt.IsZero() {
		return nil, errNodeLookup
	}
	return &model.UserSettings{Locale: settings.Locale, Timezone: scalar.TimeZone(settings.Timezone), Theme: theme, UpdatedAt: scalar.DateTime(settings.UpdatedAt)}, nil
}

func projectSubjectSettings(settings subjects.SubjectSettings) (*model.SubjectSettings, error) {
	var theme model.HeatmapTheme
	switch settings.DefaultTheme {
	case subjects.ThemeSystem:
		theme = model.HeatmapThemeSystem
	case subjects.ThemeLight:
		theme = model.HeatmapThemeLight
	case subjects.ThemeDark:
		theme = model.HeatmapThemeDark
	case subjects.ThemeGitHubLight:
		theme = model.HeatmapThemeGithubLight
	case subjects.ThemeGitHubDark:
		theme = model.HeatmapThemeGithubDark
	default:
		return nil, errNodeLookup
	}
	var week model.WeekStart
	switch settings.WeekStart {
	case subjects.WeekStartSunday:
		week = model.WeekStartSunday
	case subjects.WeekStartMonday:
		week = model.WeekStartMonday
	default:
		return nil, errNodeLookup
	}
	var policy model.FetchFailurePolicy
	switch settings.FailurePolicy {
	case subjects.FailureKeepStale:
		policy = model.FetchFailurePolicyKeepStale
	case subjects.FailurePurge:
		policy = model.FetchFailurePolicyPurge
	default:
		return nil, errNodeLookup
	}
	if settings.SubjectID == "" || !validStoredTimeZone(settings.Timezone) || settings.SyncIntervalMinutes < 1 || settings.UpdatedAt.IsZero() {
		return nil, errNodeLookup
	}
	return &model.SubjectSettings{
		Timezone: scalar.TimeZone(settings.Timezone), IsPublic: settings.IsPublic,
		DefaultTheme: theme, WeekStart: week, SyncEnabled: settings.SyncEnabled,
		SyncIntervalMinutes: settings.SyncIntervalMinutes, FailurePolicy: policy,
		UpdatedAt: scalar.DateTime(settings.UpdatedAt),
	}, nil
}

func validStoredTimeZone(zone string) bool {
	if zone == "" || zone == "Local" || zone[0] == '/' {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}

func resolveViewerSettings(ctx context.Context, viewer *model.Viewer) (*model.UserSettings, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" || viewer == nil || viewer.User == nil || viewer.User.ID != identity.userID {
		return nil, errNodeAuthentication
	}
	services, _ := ctx.Value(subjectQueryServicesContextKey{}).(SubjectQueryServices)
	if services.UserSettings == nil {
		return nil, errNodeLookup
	}
	settings, err := services.UserSettings.GetUserSettings(ctx, identity.userID)
	if err != nil {
		return nil, errNodeLookup
	}
	return projectUserSettings(settings)
}

func resolveSubjectSettings(ctx context.Context, parent *model.Subject) (*model.SubjectSettings, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return nil, errNodeAuthentication
	}
	if identity.userID == "" || parent == nil {
		return nil, nil
	}
	id, err := relayid.DecodeAs(relayid.Subject, parent.ID)
	if err != nil {
		return nil, errInvalidNodeID
	}
	nodes, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	services, _ := ctx.Value(subjectQueryServicesContextKey{}).(SubjectQueryServices)
	if nodes.Subjects == nil || services.SubjectSettings == nil {
		return nil, errNodeLookup
	}
	owned, err := ownsSubject(ctx, nodes.Subjects, identity.userID, id)
	if err != nil {
		return nil, errNodeLookup
	}
	if !owned {
		return nil, nil
	}
	settings, err := services.SubjectSettings.GetSubjectSettings(ctx, identity.userID, id)
	if errors.Is(err, subjects.ErrNotFound) || errors.Is(err, subjects.ErrForbidden) {
		return nil, nil
	}
	if err != nil || settings.SubjectID != id {
		return nil, errNodeLookup
	}
	return projectSubjectSettings(settings)
}

func resolveViewerSubjects(ctx context.Context, viewer *model.Viewer, first *int, after *scalar.Cursor) (*model.SubjectConnection, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" || viewer == nil || viewer.User == nil || viewer.User.ID != identity.userID {
		return nil, errNodeAuthentication
	}
	limit := 25
	if first != nil {
		limit = *first
	}
	if limit < 1 || limit > 100 {
		return nil, snapshotUserError("BAD_USER_INPUT", "first must be between 1 and 100")
	}
	var position *subjects.SubjectCursor
	if after != nil {
		decoded, err := cursor.DecodeAs(cursor.Subject, string(*after))
		if err != nil {
			return nil, snapshotUserError("BAD_USER_INPUT", "invalid subject cursor")
		}
		position = &subjects.SubjectCursor{CreatedAt: decoded.Timestamp, ID: decoded.ID}
	}
	services, _ := ctx.Value(subjectQueryServicesContextKey{}).(SubjectQueryServices)
	if services.Pages == nil {
		return nil, errNodeLookup
	}
	page, err := services.Pages.ListSubjectsPage(ctx, identity.userID, position, limit)
	if errors.Is(err, subjects.ErrInvalidInput) {
		return nil, snapshotUserError("BAD_USER_INPUT", "invalid subject page")
	}
	if err != nil || len(page.Subjects) > limit {
		return nil, errNodeLookup
	}
	result := &model.SubjectConnection{Edges: make([]*model.SubjectEdge, 0, len(page.Subjects)), PageInfo: &model.PageInfo{HasNextPage: page.HasNextPage, HasPreviousPage: after != nil}}
	for _, subject := range page.Subjects {
		if subject.OwnerUserID != identity.userID || subject.CreatedAt.IsZero() {
			return nil, errNodeLookup
		}
		encoded, err := cursor.Encode(cursor.Subject, cursor.Position{Timestamp: subject.CreatedAt, ID: subject.ID})
		if err != nil {
			return nil, errNodeLookup
		}
		projected, err := projectSubject(subject)
		if err != nil {
			return nil, err
		}
		result.Edges = append(result.Edges, &model.SubjectEdge{Cursor: scalar.Cursor(encoded), Node: projected})
	}
	if len(result.Edges) > 0 {
		start := result.Edges[0].Cursor
		end := result.Edges[len(result.Edges)-1].Cursor
		result.PageInfo.StartCursor, result.PageInfo.EndCursor = &start, &end
	}
	return result, nil
}
