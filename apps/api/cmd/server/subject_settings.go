package main

import (
	"context"
	"errors"
	"time"

	activityapp "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

// runtimeSubjectSettings is a composition adapter. It keeps activity and
// integration scheduling independent from the subjects persistence model while
// giving both application boundaries the same durable source of truth.
type runtimeSubjectSettings struct {
	repository subjects.Repository
}

func (reader runtimeSubjectSettings) LoadSubjectTimelineSettings(ctx context.Context, subjectID activity.SubjectID) (activityapp.SubjectTimelineSettings, bool, error) {
	settings, found, err := reader.load(ctx, string(subjectID))
	if err != nil || !found {
		return activityapp.SubjectTimelineSettings{}, found, err
	}
	return activityapp.SubjectTimelineSettings{
		Timezone: settings.Timezone, FailurePolicy: activity.FetchFailurePolicy(settings.FailurePolicy),
	}, true, nil
}

func (reader runtimeSubjectSettings) LoadSubjectSyncSettings(ctx context.Context, subjectID string) (integrations.SubjectSyncSettings, bool, error) {
	settings, found, err := reader.load(ctx, subjectID)
	if err != nil || !found {
		return integrations.SubjectSyncSettings{}, found, err
	}
	return integrations.SubjectSyncSettings{
		Timezone: settings.Timezone, Enabled: settings.SyncEnabled,
		Interval:      time.Duration(settings.SyncIntervalMinutes) * time.Minute,
		FailurePolicy: activity.FetchFailurePolicy(settings.FailurePolicy),
	}, true, nil
}

func (reader runtimeSubjectSettings) load(ctx context.Context, subjectID string) (subjects.SubjectSettings, bool, error) {
	settings, err := reader.repository.GetSubjectSettings(ctx, subjectID)
	if errors.Is(err, subjects.ErrNotFound) {
		return subjects.SubjectSettings{}, false, nil
	}
	if err != nil {
		return subjects.SubjectSettings{}, false, err
	}
	return settings, true, nil
}
