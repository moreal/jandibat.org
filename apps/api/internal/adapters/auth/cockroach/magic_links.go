package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"time"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

const magicLinkColumns = `id, email, token_hash, purpose, created_at, expires_at, consumed_at`

func (store *Store) SaveMagicLink(ctx context.Context, link coreauth.MagicLink) error {
	if link.ID == "" || link.Email == "" || !link.Purpose.Valid() || !link.ExpiresAt.After(link.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_tokens
SET consumed_at = $3
WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL`, link.Email, link.Purpose, link.CreatedAt); err != nil {
		return persistenceError(err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO magic_link_tokens (id, email, token_hash, purpose, expires_at, consumed_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		link.ID, link.Email, link.TokenHash[:], link.Purpose, link.ExpiresAt, link.ConsumedAt, link.CreatedAt)
	if err != nil {
		return persistenceError(err)
	}
	return persistenceError(scope.Commit())
}

func (store *Store) ConsumeMagicLink(ctx context.Context, tokenHash coreauth.Digest, purpose coreauth.MagicLinkPurpose, now time.Time) (coreauth.MagicLink, error) {
	if !purpose.Valid() {
		return coreauth.MagicLink{}, coreauth.ErrInvalidInput
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return coreauth.MagicLink{}, err
	}
	row := executor.QueryRowContext(ctx, `
UPDATE magic_link_tokens
SET consumed_at = $3
WHERE token_hash = $1 AND purpose = $2 AND consumed_at IS NULL AND expires_at > $3
RETURNING `+magicLinkColumns, tokenHash[:], purpose, now)
	link, err := scanMagicLink(row)
	if !errors.Is(err, coreauth.ErrNotFound) {
		return link, err
	}
	// Outbox-issued tokens live in the FK-free delivery table so the worker
	// never needs access to users or legacy auth token rows. Consuming an
	// in-flight token also terminalizes the delivery and clears its claim; this
	// prevents a send-before-complete crash from producing a second message.
	row = executor.QueryRowContext(ctx, `
UPDATE magic_link_mail_outbox
SET consumed_at = $3,
    status = CASE WHEN status = 'processing' THEN 'sent' ELSE status END,
    lease_until = CASE WHEN status = 'processing' THEN NULL ELSE lease_until END,
    claim_token = CASE WHEN status = 'processing' THEN NULL ELSE claim_token END,
    updated_at = $3
WHERE token_hash = $1 AND purpose = $2 AND consumed_at IS NULL
  AND token_expires_at > $3 AND status IN ('processing', 'sent')
RETURNING id::STRING, recipient_email, token_hash, purpose, created_at,
          token_expires_at, consumed_at`, tokenHash[:], purpose, now)
	link, err = scanMagicLink(row)
	if !errors.Is(err, coreauth.ErrNotFound) {
		return link, err
	}
	return coreauth.MagicLink{}, store.magicLinkState(ctx, tokenHash, purpose, now)
}

func (store *Store) magicLinkState(ctx context.Context, tokenHash coreauth.Digest, purpose coreauth.MagicLinkPurpose, now time.Time) error {
	var consumed sql.NullTime
	var expires time.Time
	err := store.db.QueryRowContext(ctx, `
SELECT consumed_at, expires_at
FROM magic_link_tokens
WHERE token_hash = $1 AND purpose = $2
UNION ALL
SELECT consumed_at, token_expires_at
FROM magic_link_mail_outbox
WHERE token_hash = $1 AND purpose = $2 AND status IN ('processing', 'sent')
LIMIT 1`, tokenHash[:], purpose).Scan(&consumed, &expires)
	if err == sql.ErrNoRows {
		return coreauth.ErrNotFound
	}
	if err != nil {
		return persistenceError(err)
	}
	if consumed.Valid {
		return coreauth.ErrConsumed
	}
	if !now.Before(expires) {
		return coreauth.ErrExpired
	}
	return coreauth.ErrConflict
}

func (store *Store) InvalidateMagicLink(ctx context.Context, tokenHash coreauth.Digest, now time.Time) error {
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
UPDATE magic_link_tokens SET consumed_at = COALESCE(consumed_at, $2) WHERE token_hash = $1`, tokenHash[:], now)
	count, err := rowsAffected(result, err)
	if err != nil {
		return err
	}
	if count == 0 {
		count, err = rowsAffected(executor.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET consumed_at = $2, status = CASE WHEN status = 'processing' THEN 'sent' ELSE status END,
    lease_until = CASE WHEN status = 'processing' THEN NULL ELSE lease_until END,
    claim_token = CASE WHEN status = 'processing' THEN NULL ELSE claim_token END,
    updated_at = $2
WHERE token_hash = $1 AND consumed_at IS NULL AND status IN ('processing', 'sent')`, tokenHash[:], now))
		if err != nil {
			return err
		}
		if count == 0 {
			return coreauth.ErrNotFound
		}
	}
	return nil
}

func scanMagicLink(row scanner) (coreauth.MagicLink, error) {
	var link coreauth.MagicLink
	var digest []byte
	var consumed sql.NullTime
	if err := row.Scan(&link.ID, &link.Email, &digest, &link.Purpose, &link.CreatedAt, &link.ExpiresAt, &consumed); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.MagicLink{}, coreauth.ErrNotFound
		}
		return coreauth.MagicLink{}, persistenceError(err)
	}
	if len(digest) != len(link.TokenHash) || !link.Purpose.Valid() {
		return coreauth.MagicLink{}, coreauth.ErrInvalidInput
	}
	copy(link.TokenHash[:], digest)
	if consumed.Valid {
		value := consumed.Time
		link.ConsumedAt = &value
	}
	return link, nil
}
