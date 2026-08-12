package cockroach

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func (s *Store) CheckRevocationSchema(ctx context.Context) error {
	var columns int
	err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
	AND (
	  (table_name = 'provider_token_revocation_jobs' AND column_name IN (
	    'id', 'connection_id', 'provider_id', 'token_ciphertext', 'token_key_id',
	    'status', 'attempts', 'available_at', 'lease_until', 'claim_token',
	    'terminal_at', 'terminal_reason', 'created_at', 'updated_at'
	  ))
	  OR (table_name = 'provider_sync_jobs' AND column_name IN (
	    'idempotency_key_hash', 'request_hash', 'idempotency_expires_at', 'claim_token'
	  ))
	  OR (table_name = 'activity_facts' AND column_name = 'custom_provider_id')
	)`).Scan(&columns)
	if err != nil {
		return fmt.Errorf("check OAuth token revocation schema: %w", err)
	}
	if columns != 19 {
		return fmt.Errorf("check OAuth token revocation schema: required columns are missing")
	}
	return nil
}

// RevokeConnectionAggregate provides the local half of disconnect as one
// Cockroach transaction. The encrypted token is copied into a durable worker
// job before provider_connections is sanitized; plaintext never enters the API
// service or this adapter.
func (s *Store) RevokeConnectionAggregate(ctx context.Context, id string, now time.Time) (integrations.ProviderConnection, error) {
	if id == "" {
		return integrations.ProviderConnection{}, integrations.ErrEmptyConnectionID
	}
	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: begin transaction: %w", err)
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()

	query := `SELECT ` + connectionColumns + ` FROM provider_connections WHERE id = $1::UUID FOR UPDATE`
	record, err := scanConnection(tx.QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return integrations.ProviderConnection{}, notFound("connection", id)
	}
	if err != nil {
		return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: load aggregate: %w", err)
	}
	connection := &record.Connection
	if connection.Status != integrations.ConnectionRevoked && connection.AuthMethod == integrations.AuthOAuth2 && len(record.Credentials.AccessToken) != 0 {
		keyID, err := encryptedCredentialKeyID(record.Credentials.AccessToken)
		if err != nil {
			// A malformed historical envelope must not prevent immediate local
			// credential destruction. The worker retains the opaque ciphertext
			// and durably retries; no parser detail reaches the API response.
			keyID = ""
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_token_revocation_jobs (
  connection_id, provider_id, token_ciphertext, token_key_id,
  status, attempts, available_at, created_at, updated_at
) VALUES ($1::UUID, $2, $3, NULLIF($4, ''), 'pending', 0, $5, $5, $5)
`, id, connection.ProviderID, record.Credentials.AccessToken, keyID, now); err != nil {
			return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: enqueue provider revocation: %w", err)
		}
	}
	if connection.Status != integrations.ConnectionRevoked {
		if _, err := tx.ExecContext(ctx, `
UPDATE provider_connections
SET status = 'revoked', access_token_ciphertext = NULL, access_token_key_id = NULL,
    refresh_token_ciphertext = NULL, refresh_token_key_id = NULL,
    token_expires_at = NULL, last_error = NULL, updated_at = $2,
    sync_cursor = ((COALESCE(sync_cursor, '{}'::JSONB) - 'sync_execution_expires_at') - 'sync_execution_claim_token')
      || jsonb_build_object('connection_status', 'revoked')
WHERE id = $1::UUID`, id, now); err != nil {
			return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: sanitize connection: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_connection_private_consents WHERE connection_id = $1::UUID`, id); err != nil {
		return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: remove private data consent: %w", err)
	}
	if connection.AuthMethod != integrations.AuthNone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM activity_facts WHERE subject_id = $1 AND environment_id = $2`, connection.SubjectID, connection.EnvironmentID); err != nil {
			return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: purge facts: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_sync_jobs WHERE provider_connection_id = $1::UUID`, id); err != nil {
		return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: purge sync jobs: %w", err)
	}
	if err := scope.Commit(); err != nil {
		return integrations.ProviderConnection{}, fmt.Errorf("revoke connection: commit transaction: %w", err)
	}
	connection.Status = integrations.ConnectionRevoked
	connection.LastError = ""
	connection.TokenExpiresAt = nil
	connection.PrivateDataEnabled = false
	connection.UpdatedAt = now
	return *connection, nil
}

func (s *Store) ClaimOAuthTokenRevocations(ctx context.Context, now, leaseUntil time.Time, limit int) ([]integrations.OAuthTokenRevocationJob, error) {
	if limit <= 0 || !leaseUntil.After(now) {
		return nil, integrations.ErrInvalidRevocationConfig
	}
	rows, err := s.db.QueryContext(ctx, `
WITH candidates AS (
  SELECT id
  FROM provider_token_revocation_jobs
  WHERE (status = 'pending' AND available_at <= $1)
     OR (status = 'processing' AND lease_until <= $1)
  ORDER BY available_at, id
  LIMIT $3
  FOR UPDATE SKIP LOCKED
)
UPDATE provider_token_revocation_jobs AS jobs
SET status = 'processing', attempts = jobs.attempts + 1,
    lease_until = $2, claim_token = gen_random_uuid(), updated_at = $1
FROM candidates
WHERE jobs.id = candidates.id
RETURNING jobs.id::STRING, COALESCE(jobs.connection_id::STRING, ''), jobs.provider_id,
          jobs.token_ciphertext, COALESCE(jobs.token_key_id, ''), jobs.claim_token::STRING, jobs.attempts,
          jobs.available_at, jobs.lease_until, jobs.created_at, jobs.updated_at`, now, leaseUntil, limit)
	if err != nil {
		return nil, fmt.Errorf("claim OAuth token revocations: %w", err)
	}
	defer rows.Close()
	jobs := make([]integrations.OAuthTokenRevocationJob, 0, limit)
	for rows.Next() {
		var job integrations.OAuthTokenRevocationJob
		var lease sql.NullTime
		if err := rows.Scan(&job.ID, &job.ConnectionID, &job.ProviderID, &job.TokenCiphertext, &job.TokenKeyID, &job.ClaimToken, &job.Attempts, &job.AvailableAt, &lease, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, fmt.Errorf("claim OAuth token revocations: scan: %w", err)
		}
		if lease.Valid {
			job.LeaseUntil = &lease.Time
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim OAuth token revocations: %w", err)
	}
	return jobs, nil
}

func (s *Store) CompleteOAuthTokenRevocation(ctx context.Context, id, claimToken string) error {
	if id == "" || claimToken == "" {
		return integrations.ErrInvalidRevocationConfig
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM provider_token_revocation_jobs WHERE id = $1::UUID AND status = 'processing' AND claim_token = $2::UUID`, id, claimToken)
	if err != nil {
		return fmt.Errorf("complete OAuth token revocation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete OAuth token revocation: rows affected: %w", err)
	}
	if count != 1 {
		return integrations.ErrConflict
	}
	return nil
}

func (s *Store) RetryOAuthTokenRevocation(ctx context.Context, id, claimToken string, availableAt time.Time) error {
	if id == "" || claimToken == "" || availableAt.IsZero() {
		return integrations.ErrInvalidRevocationConfig
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE provider_token_revocation_jobs
SET status = 'pending', available_at = $3, lease_until = NULL, claim_token = NULL, updated_at = now()
WHERE id = $1::UUID AND status = 'processing' AND claim_token = $2::UUID`, id, claimToken, availableAt)
	if err != nil {
		return fmt.Errorf("retry OAuth token revocation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("retry OAuth token revocation: rows affected: %w", err)
	}
	if count != 1 {
		return integrations.ErrConflict
	}
	return nil
}

func (s *Store) DeadLetterOAuthTokenRevocation(ctx context.Context, id, claimToken string, terminalAt time.Time) error {
	if id == "" || claimToken == "" || terminalAt.IsZero() {
		return integrations.ErrInvalidRevocationConfig
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE provider_token_revocation_jobs
SET status = 'dead', available_at = $3, lease_until = NULL, claim_token = NULL,
    terminal_at = $3, terminal_reason = 'max_attempts_exhausted', updated_at = $3
WHERE id = $1::UUID AND status = 'processing' AND claim_token = $2::UUID`, id, claimToken, terminalAt)
	if err != nil {
		return fmt.Errorf("dead-letter OAuth token revocation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("dead-letter OAuth token revocation: rows affected: %w", err)
	}
	if count != 1 {
		return integrations.ErrConflict
	}
	return nil
}
