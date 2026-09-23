package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

const sessionColumns = `id, user_id, session_token_hash, created_at, expires_at, revoked_at, last_seen_at, COALESCE(ip::STRING, ''), COALESCE(user_agent, '')`

func (store *Store) SaveSession(ctx context.Context, session coreauth.Session) error {
	if session.ID == "" || session.UserID == "" || !session.ExpiresAt.After(session.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	if store.pool != nil {
		id, err := uuid.Parse(session.ID)
		if err != nil {
			return coreauth.ErrInvalidInput
		}
		count, err := generated.InsertActiveSession(ctx, appdb.PGXExecutorFor(ctx, store.pool), id, session.UserID, session.TokenHash[:], session.CreatedAt, session.ExpiresAt, nullableTimeText(session.RevokedAt), nullableTimeText(session.LastSeenAt), session.IPAddress, session.UserAgent)
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return coreauth.ErrUserDisabled
		}
		return nil
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
INSERT INTO user_sessions
  (id, user_id, session_token_hash, created_at, expires_at, revoked_at, last_seen_at, ip, user_agent)
SELECT $1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::INET, NULLIF($9, '')
FROM users
WHERE id = $2 AND status = 'active'`,
		session.ID, session.UserID, session.TokenHash[:], session.CreatedAt, session.ExpiresAt,
		session.RevokedAt, session.LastSeenAt, session.IPAddress, session.UserAgent)
	count, err := rowsAffected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		return coreauth.ErrUserDisabled
	}
	return nil
}

func (store *Store) UseSession(ctx context.Context, tokenHash coreauth.Digest, now time.Time) (coreauth.Session, error) {
	if store.pool != nil {
		executor := appdb.PGXExecutorFor(ctx, store.pool)
		if appdb.HasPendingLazyPGXTransaction(ctx, store.pool) {
			// Authentication precedes OAuth provider I/O. Persist last-seen without
			// beginning the request's audit transaction across that network call.
			executor = store.pool
		}
		row, err := generated.UseActiveSession(ctx, executor, tokenHash[:], now)
		if err != nil {
			return coreauth.Session{}, persistenceError(err)
		}
		if row != nil {
			return sessionFromGenerated(generated.GetSessionByHashRow(*row))
		}
		return coreauth.Session{}, store.sessionStatePGX(ctx, tokenHash, now)
	}
	// ExecutorFor joins an already-active request transaction (for example the
	// session just created by a successful sign-in), but deliberately does not
	// start a lazy transaction during ordinary authentication. OAuth callbacks
	// therefore still perform their preflight before provider network work.
	executor := appdb.ExecutorFor(ctx, store.db)
	row := executor.QueryRowContext(ctx, `
UPDATE user_sessions
SET last_seen_at = $2
WHERE session_token_hash = $1 AND revoked_at IS NULL AND expires_at > $2
  AND EXISTS (
    SELECT 1 FROM users
    WHERE users.id = user_sessions.user_id AND users.status = 'active'
  )
RETURNING `+sessionColumns, tokenHash[:], now)
	session, err := scanSession(row)
	if !errors.Is(err, coreauth.ErrNotFound) {
		return session, err
	}
	return coreauth.Session{}, store.sessionState(ctx, executor, tokenHash, now)
}

func (store *Store) sessionStatePGX(ctx context.Context, tokenHash coreauth.Digest, now time.Time) error {
	row, err := generated.GetSessionState(ctx, appdb.PGXExecutorFor(ctx, store.pool), tokenHash[:])
	if err != nil {
		return persistenceError(err)
	}
	if row == nil {
		return coreauth.ErrNotFound
	}
	if row.RevokedAt != nil {
		return coreauth.ErrConsumed
	}
	if !now.Before(row.ExpiresAt) {
		return coreauth.ErrExpired
	}
	return coreauth.ErrConflict
}

func (store *Store) sessionState(ctx context.Context, executor appdb.Executor, tokenHash coreauth.Digest, now time.Time) error {
	var revoked sql.NullTime
	var expires time.Time
	err := executor.QueryRowContext(ctx, `
SELECT revoked_at, expires_at FROM user_sessions WHERE session_token_hash = $1`, tokenHash[:]).Scan(&revoked, &expires)
	if err == sql.ErrNoRows {
		return coreauth.ErrNotFound
	}
	if err != nil {
		return persistenceError(err)
	}
	if revoked.Valid {
		return coreauth.ErrConsumed
	}
	if !now.Before(expires) {
		return coreauth.ErrExpired
	}
	return coreauth.ErrConflict
}

func (store *Store) RevokeSession(ctx context.Context, tokenHash coreauth.Digest, now time.Time) error {
	if store.pool != nil {
		count, err := generated.RevokeSessionByHash(ctx, appdb.PGXExecutorFor(ctx, store.pool), tokenHash[:], now)
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return coreauth.ErrNotFound
		}
		return nil
	}
	return store.execSessionUpdate(ctx, `
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE session_token_hash = $1`, tokenHash[:], now)
}

func (store *Store) GetSession(ctx context.Context, tokenHash coreauth.Digest) (coreauth.Session, error) {
	if store.pool != nil {
		row, err := generated.GetSessionByHash(ctx, appdb.PGXExecutorFor(ctx, store.pool), tokenHash[:])
		if err != nil {
			return coreauth.Session{}, persistenceError(err)
		}
		if row == nil {
			return coreauth.Session{}, coreauth.ErrNotFound
		}
		return sessionFromGenerated(*row)
	}
	return scanSession(store.db.QueryRowContext(ctx, `
SELECT `+sessionColumns+` FROM user_sessions WHERE session_token_hash = $1`, tokenHash[:]))
}

func (store *Store) ListSessionsByUser(ctx context.Context, userID string) ([]coreauth.Session, error) {
	if store.pool != nil {
		rows, err := generated.ListSessionsByUser(ctx, appdb.PGXExecutorFor(ctx, store.pool), userID)
		if err != nil {
			return nil, persistenceError(err)
		}
		items := make([]coreauth.Session, 0, len(rows))
		for _, row := range rows {
			item, mapErr := sessionFromGenerated(generated.GetSessionByHashRow(row))
			if mapErr != nil {
				return nil, mapErr
			}
			items = append(items, item)
		}
		return items, nil
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT `+sessionColumns+` FROM user_sessions WHERE user_id = $1 ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, persistenceError(err)
	}
	defer rows.Close()
	items := make([]coreauth.Session, 0)
	for rows.Next() {
		item, err := scanSession(rows)
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

func (store *Store) RevokeOtherSessions(ctx context.Context, userID string, exceptTokenHash coreauth.Digest, now time.Time) error {
	if store.pool != nil {
		return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			exists, err := generated.GetSessionOwnedByUserExists(txctx, tx, userID, exceptTokenHash[:])
			if err != nil {
				return persistenceError(err)
			}
			if !exists.Exists {
				return coreauth.ErrNotFound
			}
			return persistenceError(generated.RevokeOtherSessions(txctx, tx, userID, exceptTokenHash[:], now))
		})
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM user_sessions WHERE user_id = $1 AND session_token_hash = $2)`,
		userID, exceptTokenHash[:]).Scan(&exists); err != nil {
		return persistenceError(err)
	}
	if !exists {
		return coreauth.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $3)
WHERE user_id = $1 AND session_token_hash != $2`, userID, exceptTokenHash[:], now); err != nil {
		return persistenceError(err)
	}
	return persistenceError(scope.Commit())
}

func (store *Store) RevokeSessionByID(ctx context.Context, userID, sessionID string, now time.Time) error {
	if store.pool != nil {
		id, err := uuid.Parse(sessionID)
		if err != nil {
			return coreauth.ErrInvalidInput
		}
		count, err := generated.RevokeSessionById(ctx, appdb.PGXExecutorFor(ctx, store.pool), userID, id, now)
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return coreauth.ErrNotFound
		}
		return nil
	}
	return store.execSessionUpdate(ctx, `
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $3) WHERE user_id = $1 AND id = $2`, userID, sessionID, now)
}

func nullableTimeText(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func sessionFromGenerated(row generated.GetSessionByHashRow) (coreauth.Session, error) {
	var session coreauth.Session
	if len(row.SessionTokenHash) != len(session.TokenHash) {
		return coreauth.Session{}, coreauth.ErrInvalidInput
	}
	session.ID, session.UserID = row.Id, row.UserId
	copy(session.TokenHash[:], row.SessionTokenHash)
	session.CreatedAt, session.ExpiresAt = row.CreatedAt, row.ExpiresAt
	if row.RevokedAt != nil {
		value := *row.RevokedAt
		session.RevokedAt = &value
	}
	if row.LastSeenAt != nil {
		value := *row.LastSeenAt
		session.LastSeenAt = &value
	}
	session.IPAddress, session.UserAgent = row.Ip, row.UserAgent
	return session, nil
}

func (store *Store) execSessionUpdate(ctx context.Context, query string, args ...any) error {
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, query, args...)
	count, err := rowsAffected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		return coreauth.ErrNotFound
	}
	return nil
}

func scanSession(row scanner) (coreauth.Session, error) {
	var session coreauth.Session
	var digest []byte
	var revoked, lastSeen sql.NullTime
	if err := row.Scan(&session.ID, &session.UserID, &digest, &session.CreatedAt, &session.ExpiresAt,
		&revoked, &lastSeen, &session.IPAddress, &session.UserAgent); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.Session{}, coreauth.ErrNotFound
		}
		return coreauth.Session{}, persistenceError(err)
	}
	if len(digest) != len(session.TokenHash) {
		return coreauth.Session{}, coreauth.ErrInvalidInput
	}
	copy(session.TokenHash[:], digest)
	if revoked.Valid {
		value := revoked.Time
		session.RevokedAt = &value
	}
	if lastSeen.Valid {
		value := lastSeen.Time
		session.LastSeenAt = &value
	}
	return session, nil
}
