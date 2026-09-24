package cockroach

import (
	"context"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func (store *Store) GetSubjectSettings(ctx context.Context, subjectID string) (subjects.SubjectSettings, error) {
	row, err := generated.GetSubjectSettings(ctx, store.executor(ctx), subjectID)
	if err != nil {
		return subjects.SubjectSettings{}, persistenceError(err)
	}
	if row == nil {
		return subjects.SubjectSettings{}, subjects.ErrNotFound
	}
	return subjects.SubjectSettings{SubjectID: row.SubjectId, Timezone: row.Timezone, IsPublic: row.IsPublic, DefaultTheme: subjects.HeatmapTheme(row.DefaultTheme), WeekStart: subjects.WeekStart(row.WeekStart), SyncEnabled: row.SyncEnabled, SyncIntervalMinutes: int(row.SyncIntervalMinutes), FailurePolicy: subjects.FailurePolicy(row.FailurePolicy), UpdatedAt: row.UpdatedAt}, nil
}

func (store *Store) SaveSubjectSettings(ctx context.Context, settings subjects.SubjectSettings) error {
	if settings.SubjectID == "" || settings.UpdatedAt.IsZero() || settings.SyncIntervalMinutes < 15 || settings.SyncIntervalMinutes > 10080 {
		return subjects.ErrInvalidInput
	}
	return persistenceError(store.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		count, err := generated.UpdateSubjectPreferences(txctx, tx, settings.SubjectID, string(settings.DefaultTheme), string(settings.WeekStart), settings.SyncEnabled, int32(settings.SyncIntervalMinutes), string(settings.FailurePolicy), settings.UpdatedAt) // #nosec G115 -- entry validation limits the interval to 15..10080 before the transaction.
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return subjects.ErrNotFound
		}
		count, err = generated.UpdateSubjectResource(txctx, tx, settings.SubjectID, settings.Timezone, settings.IsPublic, settings.UpdatedAt)
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return subjects.ErrNotFound
		}
		return nil
	}))
}
