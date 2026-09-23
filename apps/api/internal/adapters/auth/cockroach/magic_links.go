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

func (store *Store) SaveMagicLink(ctx context.Context, link coreauth.MagicLink) error {
	if link.ID == "" || link.Email == "" || !link.Purpose.Valid() || !link.ExpiresAt.After(link.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
	id, err := uuid.Parse(link.ID)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := generated.SupersedeLegacyMagicLinks(txctx, tx, link.Email, string(link.Purpose), link.CreatedAt); err != nil {
			return persistenceError(err)
		}
		var consumed *string
		if link.ConsumedAt != nil {
			value := nullableTimeText(link.ConsumedAt)
			consumed = &value
		}
		return persistenceError(generated.InsertMagicLink(txctx, tx, id, link.Email, link.TokenHash[:], string(link.Purpose), link.ExpiresAt, consumed, link.CreatedAt))
	})
}

func (store *Store) ConsumeMagicLink(ctx context.Context, tokenHash coreauth.Digest, purpose coreauth.MagicLinkPurpose, now time.Time) (coreauth.MagicLink, error) {
	if !purpose.Valid() {
		return coreauth.MagicLink{}, coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return coreauth.MagicLink{}, ErrNilDB
	}
	executor := appdb.PGXExecutorFor(ctx, store.pool)
	legacy, err := generated.ConsumeLegacyMagicLink(ctx, executor, tokenHash[:], string(purpose), now)
	if err != nil {
		return coreauth.MagicLink{}, persistenceError(err)
	}
	if legacy != nil {
		return magicLinkFromGenerated(legacy.Id, legacy.Email, legacy.TokenHash, legacy.Purpose, legacy.CreatedAt, legacy.ExpiresAt, legacy.ConsumedAt)
	}
	delivery, err := generated.ConsumeDeliveryMagicLink(ctx, executor, tokenHash[:], string(purpose), now)
	if err != nil {
		return coreauth.MagicLink{}, persistenceError(err)
	}
	if delivery != nil {
		if delivery.TokenHash == nil || delivery.ExpiresAt == nil {
			return coreauth.MagicLink{}, coreauth.ErrInvalidInput
		}
		return magicLinkFromGenerated(delivery.Id, delivery.Email, *delivery.TokenHash, delivery.Purpose, delivery.CreatedAt, *delivery.ExpiresAt, delivery.ConsumedAt)
	}
	return coreauth.MagicLink{}, store.magicLinkStatePGX(ctx, tokenHash, purpose, now)
}

func (store *Store) magicLinkStatePGX(ctx context.Context, tokenHash coreauth.Digest, purpose coreauth.MagicLinkPurpose, now time.Time) error {
	row, err := generated.GetMagicLinkState(ctx, appdb.PGXExecutorFor(ctx, store.pool), tokenHash[:], string(purpose))
	if err != nil {
		return persistenceError(err)
	}
	if row == nil {
		return coreauth.ErrNotFound
	}
	if row.ConsumedAt != nil {
		return coreauth.ErrConsumed
	}
	if row.ExpiresAt == nil {
		return coreauth.ErrInvalidInput
	}
	if !now.Before(*row.ExpiresAt) {
		return coreauth.ErrExpired
	}
	return coreauth.ErrConflict
}

func magicLinkFromGenerated(id, email string, digest []byte, purpose string, created, expires time.Time, consumed *time.Time) (coreauth.MagicLink, error) {
	var link coreauth.MagicLink
	if len(digest) != len(link.TokenHash) || !coreauth.MagicLinkPurpose(purpose).Valid() {
		return coreauth.MagicLink{}, coreauth.ErrInvalidInput
	}
	link.ID, link.Email, link.Purpose = id, email, coreauth.MagicLinkPurpose(purpose)
	copy(link.TokenHash[:], digest)
	link.CreatedAt, link.ExpiresAt = created, expires
	if consumed != nil {
		value := *consumed
		link.ConsumedAt = &value
	}
	return link, nil
}

func (store *Store) InvalidateMagicLink(ctx context.Context, tokenHash coreauth.Digest, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
	executor := appdb.PGXExecutorFor(ctx, store.pool)
	count, err := generated.InvalidateLegacyMagicLink(ctx, executor, tokenHash[:], now)
	if err != nil {
		return persistenceError(err)
	}
	if count == 0 {
		count, err = generated.InvalidateDeliveryMagicLink(ctx, executor, tokenHash[:], now)
		if err != nil {
			return persistenceError(err)
		}
		if count == 0 {
			return coreauth.ErrNotFound
		}
	}
	return nil
}
