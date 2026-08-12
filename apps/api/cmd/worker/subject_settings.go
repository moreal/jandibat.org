package main

import (
	"context"
	"errors"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type workerSubjectSettings struct{ repository subjects.Repository }

func (reader workerSubjectSettings) LoadSubjectSyncSettings(ctx context.Context, subjectID string) (integrations.SubjectSyncSettings, bool, error) {
	settings, err := reader.repository.GetSubjectSettings(ctx, subjectID)
	if errors.Is(err, subjects.ErrNotFound) {
		return integrations.SubjectSyncSettings{}, false, nil
	}
	if err != nil {
		return integrations.SubjectSyncSettings{}, false, err
	}
	return integrations.SubjectSyncSettings{
		Timezone: settings.Timezone, Enabled: settings.SyncEnabled,
		Interval:      time.Duration(settings.SyncIntervalMinutes) * time.Minute,
		FailurePolicy: activity.FetchFailurePolicy(settings.FailurePolicy),
	}, true, nil
}
