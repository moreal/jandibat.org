package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"time"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

const sessionColumns = `id, user_id, session_token_hash, created_at, expires_at, revoked_at, last_seen_at, COALESCE(ip::STRING, ''), COALESCE(user_agent, '')`

func (store *Store) SaveSession(ctx context.Context, session coreauth.Session) error {
	if session.ID == "" || session.UserID == "" || !session.ExpiresAt.After(session.CreatedAt) {
		return coreauth.ErrInvalidInput
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
	return store.execSessionUpdate(ctx, `
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE session_token_hash = $1`, tokenHash[:], now)
}

func (store *Store) GetSession(ctx context.Context, tokenHash coreauth.Digest) (coreauth.Session, error) {
	return scanSession(store.db.QueryRowContext(ctx, `
SELECT `+sessionColumns+` FROM user_sessions WHERE session_token_hash = $1`, tokenHash[:]))
}

func (store *Store) ListSessionsByUser(ctx context.Context, userID string) ([]coreauth.Session, error) {
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
	return store.execSessionUpdate(ctx, `
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $3) WHERE user_id = $1 AND id = $2`, userID, sessionID, now)
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
