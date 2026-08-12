package cockroach

import (
	"context"
	"database/sql"

	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func (store *Store) GetSubjectSettings(ctx context.Context, subjectID string) (subjects.SubjectSettings, error) {
	return scanSubjectSettings(appdb.ExecutorFor(ctx, store.db).QueryRowContext(ctx, `
SELECT ss.subject_id, s.timezone, s.is_public, ss.default_theme, ss.week_start,
       ss.sync_enabled, ss.sync_interval_minutes, ss.failure_policy, ss.updated_at
FROM subject_settings AS ss
JOIN subjects AS s ON s.id = ss.subject_id
WHERE ss.subject_id = $1`, subjectID))
}

func (store *Store) SaveSubjectSettings(ctx context.Context, settings subjects.SubjectSettings) error {
	if settings.SubjectID == "" || settings.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE subject_settings
SET default_theme = $2, week_start = $3, sync_enabled = $4,
    sync_interval_minutes = $5, failure_policy = $6, updated_at = $7
WHERE subject_id = $1`, settings.SubjectID, settings.DefaultTheme, settings.WeekStart,
		settings.SyncEnabled, settings.SyncIntervalMinutes, settings.FailurePolicy, settings.UpdatedAt)
	count, err := affected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		return subjects.ErrNotFound
	}
	result, err = tx.ExecContext(ctx, `
UPDATE subjects SET timezone = $2, is_public = $3, updated_at = $4 WHERE id = $1`,
		settings.SubjectID, settings.Timezone, settings.IsPublic, settings.UpdatedAt)
	count, err = affected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		return subjects.ErrNotFound
	}
	return persistenceError(scope.Commit())
}

func scanSubjectSettings(row scanner) (subjects.SubjectSettings, error) {
	var settings subjects.SubjectSettings
	var theme, weekStart, failurePolicy string
	if err := row.Scan(&settings.SubjectID, &settings.Timezone, &settings.IsPublic, &theme, &weekStart,
		&settings.SyncEnabled, &settings.SyncIntervalMinutes, &failurePolicy, &settings.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return subjects.SubjectSettings{}, subjects.ErrNotFound
		}
		return subjects.SubjectSettings{}, persistenceError(err)
	}
	settings.DefaultTheme = subjects.HeatmapTheme(theme)
	settings.WeekStart = subjects.WeekStart(weekStart)
	settings.FailurePolicy = subjects.FailurePolicy(failurePolicy)
	return settings, nil
}
