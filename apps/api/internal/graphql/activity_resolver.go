package graphql

import (
	"context"
	"errors"
	"sort"
	"time"

	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func (r *subjectResolver) resolveActivitySnapshot(ctx context.Context, parent *model.Subject, rangeArg model.DateRangeInput, timezone scalar.TimeZone, environmentIDs []string) (*model.ActivitySnapshot, error) {
	if parent == nil {
		return nil, errInvalidNodeID
	}
	rawID, err := relayid.DecodeAs(relayid.Subject, parent.ID)
	if err != nil {
		return nil, errInvalidNodeID
	}
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return nil, errNodeAuthentication
	}
	services, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	if services.Subjects == nil || services.Activity == nil {
		return nil, errNodeLookup
	}
	subject, err := services.Subjects.GetSubject(ctx, identity.userID, rawID)
	if err != nil {
		return nil, snapshotSubjectError(err)
	}
	if subject.ID != rawID || subject.Handle == "" {
		return nil, errNodeLookup
	}
	audience := appactivity.AudienceAnonymous
	if identity.userID != "" {
		owned, err := ownsSubject(ctx, services.Subjects, identity.userID, rawID)
		if err != nil {
			return nil, err
		}
		audience = appactivity.AudienceAuthenticatedNonOwner
		if owned {
			audience = appactivity.AudienceOwner
		}
	}
	from, to := domain.Date(rangeArg.From), domain.Date(rangeArg.To)
	environments := make([]domain.EnvironmentID, 0, len(environmentIDs))
	for _, id := range environmentIDs {
		environments = append(environments, domain.EnvironmentID(id))
	}
	output, err := services.Activity.ExecuteSnapshot(ctx, appactivity.SnapshotInput{
		GetTimelineInput: appactivity.GetTimelineInput{
			Subject: domain.SubjectID(rawID), ProviderSubject: domain.SubjectID(subject.Handle),
			From: &from, To: &to, Timezone: string(timezone), EnvironmentIDs: environments,
		},
		Audience: audience,
	})
	if err != nil {
		return nil, snapshotActivityError(err)
	}
	if output.Timeline.Subject != domain.SubjectID(rawID) {
		return nil, errNodeLookup
	}
	return mapActivitySnapshot(parent, output)
}

func mapActivitySnapshot(parent *model.Subject, output appactivity.SnapshotOutput) (*model.ActivitySnapshot, error) {
	days := make([]*model.ActivityDay, 0, len(output.Timeline.Days))
	total := 0
	longest, current := 0, 0
	var previous time.Time
	for _, day := range output.Timeline.Days {
		var err error
		total, err = domain.AddMetricValues(total, day.Count)
		if err != nil {
			return nil, errNodeLookup
		}
		entries := make([]*model.ActivityEntry, 0, len(day.Entries))
		for _, entry := range day.Entries {
			entries = append(entries, &model.ActivityEntry{
				EnvironmentID: string(entry.EnvironmentID), Action: string(entry.Action),
				MetricName: string(entry.Metric.Name), MetricValue: scalar.Long(entry.Metric.Value),
				Metadata: mapActivityMetadata(entry.Metadata),
			})
		}
		days = append(days, &model.ActivityDay{Date: scalar.Date(day.Date), Count: scalar.Long(day.Count), Level: int(day.Level), Entries: entries})
		if day.Count <= 0 {
			current = 0
			continue
		}
		date, err := time.Parse(time.DateOnly, string(day.Date))
		if err != nil {
			return nil, errNodeLookup
		}
		if current > 0 && date.Equal(previous.AddDate(0, 0, 1)) {
			current++
		} else {
			current = 1
		}
		if current > longest {
			longest = current
		}
		previous = date
	}
	visibleEnvironments := make([]*model.ActivityEnvironment, 0, len(output.Timeline.Environments))
	for _, environment := range output.Timeline.Environments {
		visibleEnvironments = append(visibleEnvironments, &model.ActivityEnvironment{
			ID: string(environment.ID), Key: environment.Key, Name: environment.Name,
			Scope: string(environment.Scope), Metadata: mapActivityMetadata(environment.Metadata),
		})
	}
	result := &model.ActivitySnapshot{
		Subject: parent, Range: &model.DateRange{From: scalar.Date(output.From), To: scalar.Date(output.To)},
		Days: days, Environments: visibleEnvironments, Total: scalar.Long(total), LongestStreak: longest,
		GeneratedAt: scalar.DateTime(output.GeneratedAt), Revision: output.Revision,
	}
	if output.DataUpdatedAt != nil {
		updated := scalar.DateTime(*output.DataUpdatedAt)
		result.DataUpdatedAt = &updated
	}
	return result, nil
}

func mapActivityMetadata(metadata map[string]string) []*model.ActivityMetadataEntry {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]*model.ActivityMetadataEntry, 0, len(keys))
	for _, key := range keys {
		result = append(result, &model.ActivityMetadataEntry{Key: key, Value: metadata[key]})
	}
	return result
}

func snapshotSubjectError(err error) error {
	if errors.Is(err, subjects.ErrNotFound) || errors.Is(err, subjects.ErrForbidden) || errors.Is(err, subjects.ErrUnauthenticated) {
		return nil
	}
	if errors.Is(err, subjects.ErrInvalidInput) {
		return snapshotUserError("BAD_USER_INPUT", "invalid subject")
	}
	return errNodeLookup
}

func snapshotActivityError(err error) error {
	switch {
	case errors.Is(err, appactivity.ErrInvalidDate), errors.Is(err, appactivity.ErrInvalidDateRange):
		return snapshotUserError("INVALID_DATE_RANGE", "invalid activity date range")
	case errors.Is(err, appactivity.ErrDateRangeTooLarge):
		return snapshotUserError("RANGE_TOO_LARGE", "activity date range is too large")
	case errors.Is(err, appactivity.ErrInvalidProvider):
		return snapshotUserError("INVALID_ENVIRONMENT", "invalid environment selection")
	case errors.Is(err, appactivity.ErrProviderUnavailable):
		return snapshotUserError("PROVIDER_UNAVAILABLE", "activity provider unavailable")
	default:
		return errNodeLookup
	}
}

func snapshotUserError(code, message string) error {
	return &gqlerror.Error{Message: message, Extensions: map[string]any{"code": code}}
}
