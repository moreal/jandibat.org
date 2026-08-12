package cockroach

import (
	"context"
	"database/sql"
	"strconv"

	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

const subjectColumns = `id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at`

func (store *Store) CreateSubject(ctx context.Context, subject subjects.Subject, settings subjects.SubjectSettings) error {
	_, err := store.ClaimOrCreateSubject(ctx, subject, settings)
	return err
}

func (store *Store) ClaimOrCreateSubject(ctx context.Context, subject subjects.Subject, settings subjects.SubjectSettings) (subjects.Subject, error) {
	if subject.ID == "" || subject.OwnerUserID == "" || subject.Handle == "" ||
		settings.SubjectID != subject.ID || settings.Timezone != subject.Timezone ||
		settings.IsPublic != subject.IsPublic || subject.CreatedAt.IsZero() || settings.UpdatedAt.IsZero() {
		return subjects.Subject{}, subjects.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return subjects.Subject{}, err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	claimed, claimErr := scanSubject(tx.QueryRowContext(ctx, `
UPDATE subjects
SET owner_user_id = $2, display_name = $3, timezone = $4,
    is_public = $5, updated_at = $6
WHERE handle = $1 AND owner_user_id IS NULL
RETURNING `+subjectColumns,
		subject.Handle, subject.OwnerUserID, subject.DisplayName, subject.Timezone,
		subject.IsPublic, subject.UpdatedAt))
	if claimErr == nil {
		settings.SubjectID = claimed.ID
		settings.Timezone = claimed.Timezone
		settings.IsPublic = claimed.IsPublic
		if err := insertInitialSubjectSettings(ctx, tx, settings); err != nil {
			return subjects.Subject{}, err
		}
		if err := scope.Commit(); err != nil {
			return subjects.Subject{}, persistenceError(err)
		}
		return claimed, nil
	}
	if claimErr != subjects.ErrNotFound {
		return subjects.Subject{}, claimErr
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO subjects (id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		subject.ID, subject.OwnerUserID, subject.Handle, subject.DisplayName, subject.Timezone,
		subject.IsPublic, subject.CreatedAt, subject.UpdatedAt)
	if err != nil {
		return subjects.Subject{}, persistenceError(err)
	}
	if err := insertInitialSubjectSettings(ctx, tx, settings); err != nil {
		return subjects.Subject{}, err
	}
	if err := scope.Commit(); err != nil {
		return subjects.Subject{}, persistenceError(err)
	}
	return subject, nil
}

func insertInitialSubjectSettings(ctx context.Context, tx *sql.Tx, settings subjects.SubjectSettings) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO subject_settings
  (subject_id, default_theme, week_start, sync_enabled, sync_interval_minutes, failure_policy, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (subject_id) DO UPDATE SET
  default_theme = excluded.default_theme, week_start = excluded.week_start,
  sync_enabled = excluded.sync_enabled,
  sync_interval_minutes = excluded.sync_interval_minutes,
  failure_policy = excluded.failure_policy, updated_at = excluded.updated_at`, settings.SubjectID, settings.DefaultTheme,
		settings.WeekStart, settings.SyncEnabled, settings.SyncIntervalMinutes, settings.FailurePolicy, settings.UpdatedAt)
	if err != nil {
		return persistenceError(err)
	}
	return nil
}

func (store *Store) GetSubject(ctx context.Context, identifier string) (subjects.Subject, error) {
	return scanSubject(appdb.ExecutorFor(ctx, store.db).QueryRowContext(ctx, `
SELECT `+subjectColumns+` FROM subjects
WHERE id = $1 OR handle = $1
ORDER BY CASE WHEN id = $1 THEN 0 ELSE 1 END
LIMIT 1`, identifier))
}

func (store *Store) ListSubjects(ctx context.Context, ownerUserID string, after *subjects.SubjectCursor, limit int) ([]subjects.Subject, error) {
	if limit < 1 {
		return nil, subjects.ErrInvalidInput
	}
	query := `SELECT ` + subjectColumns + ` FROM subjects WHERE owner_user_id = $1`
	args := []any{ownerUserID}
	if after != nil {
		query += ` AND (created_at < $2 OR (created_at = $2 AND id > $3))`
		args = append(args, after.CreatedAt, after.ID)
	}
	query += ` ORDER BY created_at DESC, id LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := appdb.ExecutorFor(ctx, store.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, persistenceError(err)
	}
	defer rows.Close()
	items := make([]subjects.Subject, 0)
	for rows.Next() {
		item, err := scanSubject(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, persistenceError(err)
	}
	return items, nil
}

func (store *Store) SaveSubject(ctx context.Context, subject subjects.Subject) error {
	if subject.ID == "" || subject.OwnerUserID == "" || subject.Handle == "" || subject.UpdatedAt.IsZero() {
		return subjects.ErrInvalidInput
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
UPDATE subjects
SET handle = $2, display_name = $3, updated_at = $4
WHERE id = $1 AND owner_user_id = $5 AND timezone = $6 AND is_public = $7`,
		subject.ID, subject.Handle, subject.DisplayName, subject.UpdatedAt,
		subject.OwnerUserID, subject.Timezone, subject.IsPublic)
	count, err := affected(result, err)
	if err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	var exists bool
	if err := executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM subjects WHERE id = $1)`, subject.ID).Scan(&exists); err != nil {
		return persistenceError(err)
	}
	if !exists {
		return subjects.ErrNotFound
	}
	return subjects.ErrConflict
}

func (store *Store) DeleteSubject(ctx context.Context, subjectID string) error {
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return persistenceError(err)
	}
	tx := scope.Tx
	defer scope.Rollback()
	var lockedSubjectID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM subjects WHERE id = $1 FOR UPDATE`, subjectID).Scan(&lockedSubjectID); err != nil {
		if err == sql.ErrNoRows {
			return subjects.ErrNotFound
		}
		return persistenceError(err)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id
FROM provider_connections
WHERE subject_id = $1
FOR UPDATE`, subjectID)
	if err != nil {
		return persistenceError(err)
	}
	for rows.Next() {
		var connectionID string
		if err := rows.Scan(&connectionID); err != nil {
			_ = rows.Close()
			return persistenceError(err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return persistenceError(err)
	}
	if err := rows.Close(); err != nil {
		return persistenceError(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_token_revocation_jobs (
  connection_id, provider_id, token_ciphertext, token_key_id,
  status, attempts, available_at, created_at, updated_at
)
SELECT id, COALESCE(sync_cursor->>'provider_id', ''), access_token_ciphertext,
       access_token_key_id, 'pending', 0, now(), now(), now()
FROM provider_connections
WHERE subject_id = $1 AND auth_method = 'oauth2'
  AND access_token_ciphertext IS NOT NULL
`, subjectID); err != nil {
		return persistenceError(err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM subjects WHERE id = $1`, subjectID)
	count, err := affected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		return subjects.ErrNotFound
	}
	return persistenceError(scope.Commit())
}

func scanSubject(row scanner) (subjects.Subject, error) {
	var subject subjects.Subject
	var owner, displayName sql.NullString
	if err := row.Scan(&subject.ID, &owner, &subject.Handle, &displayName, &subject.Timezone,
		&subject.IsPublic, &subject.CreatedAt, &subject.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return subjects.Subject{}, subjects.ErrNotFound
		}
		return subjects.Subject{}, persistenceError(err)
	}
	if owner.Valid {
		subject.OwnerUserID = owner.String
	}
	if displayName.Valid {
		value := displayName.String
		subject.DisplayName = &value
	}
	return subject, nil
}
