package cockroach

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

const magicLinkDeliveryColumns = `
id::STRING, recipient_email, redirect_uri, purpose, status, attempts,
available_at, lease_until, COALESCE(claim_token::STRING, ''), created_at,
updated_at, terminal_at, COALESCE(terminal_reason, '')`

var (
	_ coreauth.MagicLinkDeliveryIssuer     = (*Store)(nil)
	_ coreauth.MagicLinkDeliveryRepository = (*Store)(nil)
)

// CheckMagicLinkDeliverySchema is a read-only startup probe for the worker's
// exact, FK-free outbox contract. In particular, there is no bearer-token or
// decryptable-token column and the worker needs no users/token-table access.
func (store *Store) CheckMagicLinkDeliverySchema(ctx context.Context) error {
	var columns int
	err := store.db.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'magic_link_mail_outbox'
  AND column_name IN (
    'id', 'token_hash', 'token_expires_at', 'consumed_at', 'recipient_email',
    'redirect_uri', 'purpose', 'status', 'attempts', 'available_at',
    'lease_until', 'claim_token', 'created_at', 'updated_at', 'terminal_at',
    'terminal_reason'
  )`).Scan(&columns)
	if err != nil {
		return persistenceError(err)
	}
	if columns != 16 {
		return fmt.Errorf("auth cockroach: Magic Link delivery schema is incomplete: got %d of 16 columns", columns)
	}
	return nil
}

// SaveMagicLinkDeliveryIntent stores no bearer material. In the same
// transaction it invalidates already-sent links and supersedes pending or
// in-flight jobs for the normalized identity before inserting the new intent.
func (store *Store) SaveMagicLinkDeliveryIntent(ctx context.Context, delivery coreauth.MagicLinkDelivery) error {
	if delivery.ID == "" || delivery.RecipientEmail == "" || !delivery.Purpose.Valid() ||
		delivery.Status != coreauth.MagicLinkDeliveryPending || delivery.AvailableAt.IsZero() || delivery.CreatedAt.IsZero() {
		return coreauth.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	// Preserve compatibility with links issued by the direct-mail development
	// path while ensuring a new request supersedes every older bearer.
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_tokens
SET consumed_at = $3
WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL`, delivery.RecipientEmail, delivery.Purpose, delivery.CreatedAt); err != nil {
		return persistenceError(err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET consumed_at = $3, updated_at = $3
WHERE recipient_email = $1 AND purpose = $2 AND status = 'sent'
  AND consumed_at IS NULL`, delivery.RecipientEmail, delivery.Purpose, delivery.CreatedAt); err != nil {
		return persistenceError(err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET status = 'superseded', token_hash = NULL, token_expires_at = NULL,
    consumed_at = NULL, lease_until = NULL, claim_token = NULL,
    updated_at = $3, terminal_at = $3, terminal_reason = 'superseded'
WHERE recipient_email = $1 AND purpose = $2 AND status IN ('pending', 'processing')`, delivery.RecipientEmail, delivery.Purpose, delivery.CreatedAt); err != nil {
		return persistenceError(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO magic_link_mail_outbox (
  id, recipient_email, redirect_uri, purpose, status, attempts,
  available_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, 'pending', 0, $5, $6, $6)`,
		delivery.ID, delivery.RecipientEmail, delivery.RedirectURI, delivery.Purpose,
		delivery.AvailableAt, delivery.CreatedAt); err != nil {
		return persistenceError(err)
	}
	return persistenceError(scope.Commit())
}

func (store *Store) ClaimMagicLinkDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit, maxAttempts int) ([]coreauth.MagicLinkDelivery, error) {
	if now.IsZero() || lease <= 0 || limit <= 0 || maxAttempts <= 0 || maxAttempts > 5 {
		return nil, coreauth.ErrInvalidInput
	}
	claimToken, err := deliveryClaimUUID()
	if err != nil {
		return nil, err
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return nil, err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	// ConsumeMagicLink transitions an in-flight delivery to sent in its own
	// atomic UPDATE. This extra transition covers rows written by an older API
	// binary during a rolling deploy.
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET status = 'sent', lease_until = NULL, claim_token = NULL, updated_at = $1
WHERE status = 'processing' AND lease_until <= $1 AND consumed_at IS NOT NULL`, now); err != nil {
		return nil, persistenceError(err)
	}
	// A crash after taking the final attempt must not strand a processing row.
	// Clearing the digest and expiry makes the prior bearer unusable atomically
	// with terminalization.
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET status = 'dead', token_hash = NULL, token_expires_at = NULL,
    consumed_at = NULL, lease_until = NULL, claim_token = NULL,
    updated_at = $1, terminal_at = $1, terminal_reason = 'attempts_exhausted'
WHERE status = 'processing' AND lease_until <= $1 AND attempts >= $2`, now, maxAttempts); err != nil {
		return nil, persistenceError(err)
	}
	// Reclaiming a crashed, unconsumed send invalidates its digest before a new
	// token is minted. Pending rows already have a null token state.
	if _, err := tx.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET status = 'processing', attempts = attempts + 1, lease_until = $2,
    claim_token = $3, token_hash = NULL, token_expires_at = NULL,
    consumed_at = NULL, updated_at = $1
WHERE id IN (
  SELECT id FROM magic_link_mail_outbox
  WHERE attempts < $4 AND (
    (status = 'pending' AND available_at <= $1)
    OR (status = 'processing' AND lease_until <= $1)
  )
  ORDER BY available_at, created_at, id
  LIMIT $5
  FOR UPDATE SKIP LOCKED
)`, now, now.Add(lease), claimToken, maxAttempts, limit); err != nil {
		return nil, persistenceError(err)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT `+magicLinkDeliveryColumns+`
FROM magic_link_mail_outbox
WHERE status = 'processing' AND claim_token = $1
ORDER BY available_at, created_at, id`, claimToken)
	if err != nil {
		return nil, persistenceError(err)
	}
	defer rows.Close()
	jobs := make([]coreauth.MagicLinkDelivery, 0)
	for rows.Next() {
		job, scanErr := scanMagicLinkDelivery(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, persistenceError(err)
	}
	if err := scope.Commit(); err != nil {
		return nil, persistenceError(err)
	}
	return jobs, nil
}

func (store *Store) ActivateMagicLinkDelivery(ctx context.Context, id, claimToken string, link coreauth.MagicLink) error {
	if id == "" || claimToken == "" || link.Email == "" || !link.Purpose.Valid() || !link.ExpiresAt.After(link.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	var recipient string
	var purpose coreauth.MagicLinkPurpose
	if err := tx.QueryRowContext(ctx, `
SELECT recipient_email, purpose
FROM magic_link_mail_outbox
WHERE id = $1 AND claim_token = $2 AND status = 'processing'
FOR UPDATE`, id, claimToken).Scan(&recipient, &purpose); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.ErrConflict
		}
		return persistenceError(err)
	}
	if recipient != link.Email || purpose != link.Purpose {
		return coreauth.ErrConflict
	}
	count, err := rowsAffected(tx.ExecContext(ctx, `
UPDATE magic_link_mail_outbox
SET token_hash = $3, token_expires_at = $4, consumed_at = NULL, updated_at = $5
WHERE id = $1 AND claim_token = $2 AND status = 'processing'`,
		id, claimToken, link.TokenHash[:], link.ExpiresAt, link.CreatedAt))
	if err != nil {
		return err
	}
	if count != 1 {
		return coreauth.ErrConflict
	}
	return persistenceError(scope.Commit())
}

func (store *Store) CompleteMagicLinkDelivery(ctx context.Context, id, claimToken string, now time.Time) error {
	return store.finishDelivery(ctx, `
UPDATE magic_link_mail_outbox
SET status = 'sent', lease_until = NULL, claim_token = NULL, updated_at = $3
WHERE id = $1 AND claim_token = $2 AND status = 'processing'
  AND token_hash IS NOT NULL AND token_expires_at IS NOT NULL`, id, claimToken, now)
}

func (store *Store) RetryMagicLinkDelivery(ctx context.Context, id, claimToken string, now, next time.Time, maxAttempts int) (coreauth.MagicLinkDeliveryStatus, error) {
	if id == "" || claimToken == "" || now.IsZero() || !next.After(now) || maxAttempts <= 0 || maxAttempts > 5 {
		return "", coreauth.ErrInvalidInput
	}
	ctx, scope, err := store.beginMutation(ctx)
	if err != nil {
		return "", err
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	var status coreauth.MagicLinkDeliveryStatus
	err = tx.QueryRowContext(ctx, `
UPDATE magic_link_mail_outbox
SET status = CASE WHEN attempts >= $5 THEN 'dead' ELSE 'pending' END,
    token_hash = NULL, token_expires_at = NULL, consumed_at = NULL,
    available_at = CASE WHEN attempts >= $5 THEN available_at ELSE $4 END,
    lease_until = NULL, claim_token = NULL, updated_at = $3,
    terminal_at = CASE WHEN attempts >= $5 THEN $3 ELSE NULL END,
    terminal_reason = CASE WHEN attempts >= $5 THEN 'attempts_exhausted' ELSE NULL END
WHERE id = $1 AND claim_token = $2 AND status = 'processing'
RETURNING status`, id, claimToken, now, next, maxAttempts).Scan(&status)
	if err == sql.ErrNoRows {
		return "", coreauth.ErrConflict
	}
	if err != nil {
		return "", persistenceError(err)
	}
	return status, persistenceError(scope.Commit())
}

func (store *Store) finishDelivery(ctx context.Context, query, id, claimToken string, now time.Time) error {
	if id == "" || claimToken == "" || now.IsZero() {
		return coreauth.ErrInvalidInput
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	count, err := rowsAffected(executor.ExecContext(ctx, query, id, claimToken, now))
	if err != nil {
		return err
	}
	if count != 1 {
		return coreauth.ErrConflict
	}
	return nil
}

func scanMagicLinkDelivery(row scanner) (coreauth.MagicLinkDelivery, error) {
	var job coreauth.MagicLinkDelivery
	var lease, terminal sql.NullTime
	if err := row.Scan(
		&job.ID, &job.RecipientEmail, &job.RedirectURI, &job.Purpose,
		&job.Status, &job.Attempts, &job.AvailableAt, &lease,
		&job.ClaimToken, &job.CreatedAt, &job.UpdatedAt, &terminal,
		&job.TerminalReason,
	); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.MagicLinkDelivery{}, coreauth.ErrNotFound
		}
		return coreauth.MagicLinkDelivery{}, persistenceError(err)
	}
	if !job.Purpose.Valid() {
		return coreauth.MagicLinkDelivery{}, coreauth.ErrInvalidInput
	}
	if lease.Valid {
		job.LeaseUntil = &lease.Time
	}
	if terminal.Valid {
		job.TerminalAt = &terminal.Time
	}
	return job, nil
}

func deliveryClaimUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
