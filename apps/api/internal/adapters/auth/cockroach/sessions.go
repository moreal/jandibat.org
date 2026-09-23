package cockroach

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

func (store *Store) SaveSession(ctx context.Context, session coreauth.Session) error {
	if session.ID == "" || session.UserID == "" || !session.ExpiresAt.After(session.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
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

func (store *Store) UseSession(ctx context.Context, tokenHash coreauth.Digest, now time.Time) (coreauth.Session, error) {
	if store.pool == nil {
		return coreauth.Session{}, ErrNilDB
	}
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

func (store *Store) RevokeSession(ctx context.Context, tokenHash coreauth.Digest, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
	count, err := generated.RevokeSessionByHash(ctx, appdb.PGXExecutorFor(ctx, store.pool), tokenHash[:], now)
	if err != nil {
		return persistenceError(err)
	}
	if count == 0 {
		return coreauth.ErrNotFound
	}
	return nil
}

func (store *Store) GetSession(ctx context.Context, tokenHash coreauth.Digest) (coreauth.Session, error) {
	if store.pool == nil {
		return coreauth.Session{}, ErrNilDB
	}
	row, err := generated.GetSessionByHash(ctx, appdb.PGXExecutorFor(ctx, store.pool), tokenHash[:])
	if err != nil {
		return coreauth.Session{}, persistenceError(err)
	}
	if row == nil {
		return coreauth.Session{}, coreauth.ErrNotFound
	}
	return sessionFromGenerated(*row)
}

func (store *Store) GetSessionByID(ctx context.Context, userID, sessionID string) (coreauth.Session, error) {
	if store.pool == nil {
		return coreauth.Session{}, ErrNilDB
	}
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return coreauth.Session{}, coreauth.ErrInvalidInput
	}
	row, err := generated.GetSessionByIdOwned(ctx, appdb.PGXExecutorFor(ctx, store.pool), userID, id)
	if err != nil {
		return coreauth.Session{}, persistenceError(err)
	}
	if row == nil {
		return coreauth.Session{}, coreauth.ErrNotFound
	}
	result := coreauth.Session{
		ID: row.Id, UserID: row.UserId, CreatedAt: row.CreatedAt,
		ExpiresAt: row.ExpiresAt, IPAddress: row.Ip, UserAgent: row.UserAgent,
	}
	if row.RevokedAt != nil {
		value := *row.RevokedAt
		result.RevokedAt = &value
	}
	if row.LastSeenAt != nil {
		value := *row.LastSeenAt
		result.LastSeenAt = &value
	}
	return result, nil
}

func (store *Store) ListSessionsByUser(ctx context.Context, userID string) ([]coreauth.Session, error) {
	if store.pool == nil {
		return nil, ErrNilDB
	}
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

func (store *Store) ListSessionsPage(ctx context.Context, userID string, after *coreauth.SessionCursor, first int) ([]coreauth.Session, error) {
	if store.pool == nil {
		return nil, ErrNilDB
	}
	if first < 1 || first > 100 || userID == "" {
		return nil, coreauth.ErrInvalidInput
	}
	afterTime, afterID := "", uuid.Nil
	if after != nil {
		if after.CreatedAt.IsZero() {
			return nil, coreauth.ErrInvalidInput
		}
		var err error
		afterID, err = uuid.Parse(after.ID)
		if err != nil {
			return nil, coreauth.ErrInvalidInput
		}
		afterTime = after.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	rows, err := generated.ListSessionsPage(ctx, appdb.PGXExecutorFor(ctx, store.pool), userID, afterTime, afterID, int64(first+1))
	if err != nil {
		return nil, persistenceError(err)
	}
	items := make([]coreauth.Session, 0, len(rows))
	for _, row := range rows {
		item := coreauth.Session{
			ID: row.Id, UserID: row.UserId, CreatedAt: row.CreatedAt,
			ExpiresAt: row.ExpiresAt, IPAddress: row.Ip, UserAgent: row.UserAgent,
		}
		if row.RevokedAt != nil {
			value := *row.RevokedAt
			item.RevokedAt = &value
		}
		if row.LastSeenAt != nil {
			value := *row.LastSeenAt
			item.LastSeenAt = &value
		}
		items = append(items, item)
	}
	return items, nil
}

func (store *Store) RevokeOtherSessions(ctx context.Context, userID string, exceptTokenHash coreauth.Digest, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
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

func (store *Store) RevokeOtherSessionsExceptID(ctx context.Context, userID, sessionID string, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
	if userID == "" {
		return coreauth.ErrInvalidInput
	}
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		current, err := generated.LockActiveOwnedSessionById(txctx, tx, userID, id, now)
		if err != nil {
			return persistenceError(err)
		}
		if current == nil {
			return coreauth.ErrNotFound
		}
		return persistenceError(generated.RevokeOtherSessionsById(txctx, tx, userID, id, now))
	})
}

func (store *Store) RevokeSessionByID(ctx context.Context, userID, sessionID string, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
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
