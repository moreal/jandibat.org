package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/identity"
)

func (store *Store) GetUserByID(ctx context.Context, id string) (coreauth.User, error) {
	if store.pool == nil {
		return coreauth.User{}, ErrNilDB
	}
	row, err := generated.GetUserById(ctx, appdb.PGXExecutorFor(ctx, store.pool), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return coreauth.User{}, coreauth.ErrNotFound
	}
	if err != nil {
		return coreauth.User{}, persistenceError(err)
	}
	return userFromGenerated(row)
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
	if store.pool == nil {
		return coreauth.User{}, ErrNilDB
	}
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

func normalizeEmail(value string) (string, bool) {
	return identity.CanonicalEmail(value)
}
