package cockroach

import (
	"context"
	"database/sql"

	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

const userColumns = `id, primary_email, email_verified_at, status, created_at, updated_at`
const userSettingsColumns = `locale, timezone, theme, updated_at`

func (store *Store) SaveUser(ctx context.Context, user subjects.User, settings subjects.UserSettings) error {
	if user.ID == "" || user.PrimaryEmail == "" || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() || settings.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE users
SET primary_email = $2, email_verified_at = $3, updated_at = GREATEST(updated_at, $4)
WHERE id = $1 AND status = 'active'`,
		user.ID, user.PrimaryEmail, user.EmailVerifiedAt, user.UpdatedAt)
	if err != nil {
		return persistenceError(err)
	}
	count, err := affected(result, nil)
	if err != nil {
		return err
	}
	if count == 0 {
		// The auth boundary owns user creation and lifecycle status. Never
		// recreate a user from an authentication result that raced deletion.
		return subjects.ErrForbidden
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO user_settings (user_id, locale, timezone, theme, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO NOTHING`,
		user.ID, settings.Locale, settings.Timezone, settings.Theme, settings.UpdatedAt)
	if err != nil {
		return persistenceError(err)
	}
	return persistenceError(scope.Commit())
}

func (store *Store) GetUser(ctx context.Context, userID string) (subjects.User, error) {
	return scanUser(appdb.ExecutorFor(ctx, store.db).QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, userID))
}

func (store *Store) GetUserSettings(ctx context.Context, userID string) (subjects.UserSettings, error) {
	return scanUserSettings(appdb.ExecutorFor(ctx, store.db).QueryRowContext(ctx, `
SELECT `+userSettingsColumns+` FROM user_settings WHERE user_id = $1`, userID))
}

func (store *Store) SaveUserSettings(ctx context.Context, userID string, settings subjects.UserSettings) error {
	if userID == "" || settings.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
UPDATE user_settings
SET locale = $2, timezone = $3, theme = $4, updated_at = $5
WHERE user_id = $1`, userID, settings.Locale, settings.Timezone, settings.Theme, settings.UpdatedAt)
	count, err := affected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		return subjects.ErrNotFound
	}
	return nil
}

func scanUser(row scanner) (subjects.User, error) {
	var user subjects.User
	var verified sql.NullTime
	var status string
	if err := row.Scan(&user.ID, &user.PrimaryEmail, &verified, &status, &user.CreatedAt, &user.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return subjects.User{}, subjects.ErrNotFound
		}
		return subjects.User{}, persistenceError(err)
	}
	user.Status = subjects.UserStatus(status)
	if verified.Valid {
		value := verified.Time
		user.EmailVerifiedAt = &value
	}
	return user, nil
}

func scanUserSettings(row scanner) (subjects.UserSettings, error) {
	var settings subjects.UserSettings
	var theme string
	if err := row.Scan(&settings.Locale, &settings.Timezone, &theme, &settings.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return subjects.UserSettings{}, subjects.ErrNotFound
		}
		return subjects.UserSettings{}, persistenceError(err)
	}
	settings.Theme = subjects.HeatmapTheme(theme)
	return settings, nil
}
