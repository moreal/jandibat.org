package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/identity"
)

const userColumns = `id, primary_email, status, email_verified_at, created_at, updated_at`

func (store *Store) GetUserByID(ctx context.Context, id string) (coreauth.User, error) {
	if store.pool != nil {
		row, err := generated.GetUserById(ctx, appdb.PGXExecutorFor(ctx, store.pool), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return coreauth.User{}, coreauth.ErrNotFound
		}
		if err != nil {
			return coreauth.User{}, persistenceError(err)
		}
		return userFromGenerated(row)
	}
	return scanUser(appdb.ExecutorFor(ctx, store.db).QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func userFromGenerated(row generated.GetUserByIdRow) (coreauth.User, error) {
	user := coreauth.User{ID: row.Id, PrimaryEmail: row.PrimaryEmail, Status: coreauth.UserStatus(row.Status), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.EmailVerifiedAt != nil {
		verified := *row.EmailVerifiedAt
		user.EmailVerifiedAt = &verified
	}
	return user, nil
}

func (store *Store) GetOrCreateUserByEmail(ctx context.Context, email, suggestedID string, now time.Time) (coreauth.User, error) {
	normalized, ok := normalizeEmail(email)
	if !ok || strings.TrimSpace(suggestedID) == "" || now.IsZero() {
		return coreauth.User{}, coreauth.ErrInvalidInput
	}
	if store.pool != nil {
		var user coreauth.User
		err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			for _, key := range store.deletedIdentityHMACKeys {
				digest, ok := identity.EmailHMAC(normalized, key.material)
				if !ok {
					return coreauth.ErrInvalidInput
				}
				row, err := generated.GetActiveIdentityTombstone(txctx, tx, key.id, digest[:], now)
				if err != nil {
					return persistenceError(err)
				}
				if row.Exists {
					return coreauth.ErrUserDisabled
				}
			}
			emailHash := sha256.Sum256([]byte(normalized))
			row, err := generated.InsertOrFindUser(txctx, tx, suggestedID, normalized, now, emailHash[:])
			if err != nil {
				return persistenceError(err)
			}
			if row == nil {
				return coreauth.ErrUserDisabled
			}
			user, err = userFromGenerated(generated.GetUserByIdRow(*row))
			return err
		})
		return user, err
	}
	query := `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
SELECT $1, $2, $3, 'active', $3, $3
WHERE NOT EXISTS (
  SELECT 1
  FROM deleted_identity_tombstones
	WHERE email_hash = $4 AND expires_at > $3
)`
	args := []any{suggestedID, normalized, now, nil}
	emailHash := sha256.Sum256([]byte(normalized))
	args[3] = emailHash[:]
	if len(store.deletedIdentityHMACKeys) != 0 {
		query += `
AND NOT EXISTS (
  SELECT 1
  FROM deleted_identity_tombstones_v2
  WHERE expires_at > $3 AND (identity_key_id, identity_digest) IN (`
		for index, key := range store.deletedIdentityHMACKeys {
			if index != 0 {
				query += ", "
			}
			keyPlaceholder := 5 + index*2
			query += fmt.Sprintf("($%d, $%d)", keyPlaceholder, keyPlaceholder+1)
			digest, ok := identity.EmailHMAC(normalized, key.material)
			if !ok {
				return coreauth.User{}, coreauth.ErrInvalidInput
			}
			args = append(args, key.id, digest[:])
		}
		query += ")\n)"
	}
	query += `
ON CONFLICT (primary_email) DO UPDATE SET primary_email = excluded.primary_email
RETURNING ` + userColumns
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return coreauth.User{}, err
	}
	user, err := scanUser(executor.QueryRowContext(ctx, query, args...))
	if err == coreauth.ErrNotFound {
		// A retained deletion tombstone deliberately looks like an inactive user
		// to the application layer. The single implicit transaction also makes
		// this read/write conflict serializable with account deletion.
		return coreauth.User{}, coreauth.ErrUserDisabled
	}
	return user, err
}

func scanUser(row scanner) (coreauth.User, error) {
	var user coreauth.User
	var status string
	var verified sql.NullTime
	if err := row.Scan(&user.ID, &user.PrimaryEmail, &status, &verified, &user.CreatedAt, &user.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.User{}, coreauth.ErrNotFound
		}
		return coreauth.User{}, persistenceError(err)
	}
	user.Status = coreauth.UserStatus(status)
	if verified.Valid {
		value := verified.Time
		user.EmailVerifiedAt = &value
	}
	return user, nil
}

func normalizeEmail(value string) (string, bool) {
	return identity.CanonicalEmail(value)
}
