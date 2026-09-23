package cockroach

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func (store *Store) SaveUser(ctx context.Context, user subjects.User, settings subjects.UserSettings) error {
	if user.ID == "" || user.PrimaryEmail == "" || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() || settings.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	return persistenceError(store.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		var err error
		verified := user.EmailVerifiedAt
		var count int64
		if verified == nil {
			count, err = generated.UpdateActiveUserWithoutVerification(txctx, tx, user.ID, user.PrimaryEmail, user.UpdatedAt)
		} else {
			count, err = generated.UpdateActiveUser(txctx, tx, user.ID, user.PrimaryEmail, *verified, user.UpdatedAt)
		}
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return subjects.ErrForbidden
		}
		_, err = generated.InsertUserSettingsIfMissing(txctx, tx, user.ID, settings.Locale, settings.Timezone, string(settings.Theme), settings.UpdatedAt)
		return persistenceError(err)
	}))
}

func (store *Store) GetUser(ctx context.Context, userID string) (subjects.User, error) {
	row, err := generated.GetUser(ctx, store.executor(ctx), userID)
	if err != nil {
		return subjects.User{}, persistenceError(err)
	}
	if row == nil {
		return subjects.User{}, subjects.ErrNotFound
	}
	var verified *time.Time
	if row.EmailVerifiedAt != nil {
		value := *row.EmailVerifiedAt
		verified = &value
	}
	return subjects.User{ID: row.Id, PrimaryEmail: row.PrimaryEmail, EmailVerifiedAt: verified, Status: subjects.UserStatus(row.Status), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (store *Store) GetUserSettings(ctx context.Context, userID string) (subjects.UserSettings, error) {
	row, err := generated.GetUserSettings(ctx, store.executor(ctx), userID)
	if err != nil {
		return subjects.UserSettings{}, persistenceError(err)
	}
	if row == nil {
		return subjects.UserSettings{}, subjects.ErrNotFound
	}
	return subjects.UserSettings{Locale: row.Locale, Timezone: row.Timezone, Theme: subjects.HeatmapTheme(row.Theme), UpdatedAt: row.UpdatedAt}, nil
}

func (store *Store) SaveUserSettings(ctx context.Context, userID string, settings subjects.UserSettings) error {
	if userID == "" || settings.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	count, err := generated.UpdateUserSettings(ctx, store.executor(ctx), userID, settings.Locale, settings.Timezone, string(settings.Theme), settings.UpdatedAt)
	if err != nil {
		return persistenceError(err)
	}
	if count == 0 {
		return subjects.ErrNotFound
	}
	return nil
}
