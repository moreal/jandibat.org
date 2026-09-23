package cockroach

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func (s *Store) CheckRevocationSchema(ctx context.Context) error {
	if s.pool == nil {
		return ErrNilDB
	}
	var columns int
	// adapter-sql-allowlist: virtual-catalog; this fixed information_schema.columns probe reads
	// virtual-catalog metadata, which official unpatched Scythe 0.17.0 cannot
	// model as a source table. It accepts no caller-supplied SQL or identifiers;
	// this limitation is not evidence of an upstream defect.
	query := `
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
	)`
	err := appdb.PGXExecutorFor(ctx, s.pool).QueryRow(ctx, query).Scan(&columns)
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
	if s.pool == nil {
		return integrations.ProviderConnection{}, ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.ProviderConnection{}, integrations.ErrInvalidIdentifier
	}
	var revoked integrations.ProviderConnection
	err = appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		row, err := generated.LockConnectionForRevocation(txctx, tx, parsed)
		if err != nil {
			return fmt.Errorf("revoke connection: load aggregate: %w", err)
		}
		if row == nil {
			return notFound("connection", id)
		}
		record, err := connectionFromGenerated(generated.GetConnectionByIdRow(*row))
		if err != nil {
			return fmt.Errorf("revoke connection: load aggregate: %w", err)
		}
		connection := &record.Connection
		if connection.Status != integrations.ConnectionRevoked && connection.AuthMethod == integrations.AuthOAuth2 && len(record.Credentials.AccessToken) != 0 {
			keyID, keyErr := encryptedCredentialKeyID(record.Credentials.AccessToken)
			if keyErr != nil {
				// Malformed historical envelopes must not prevent immediate local
				// credential destruction. Keep the opaque ciphertext for worker retry.
				keyID = ""
			}
			if err := generated.EnqueueOAuthTokenRevocation(txctx, tx, &parsed, connection.ProviderID, record.Credentials.AccessToken, optionalString(keyID), now); err != nil {
				return fmt.Errorf("revoke connection: enqueue provider revocation: %w", err)
			}
		}
		if connection.Status != integrations.ConnectionRevoked {
			if err := generated.SanitizeRevokedConnection(txctx, tx, parsed, now); err != nil {
				return fmt.Errorf("revoke connection: sanitize connection: %w", err)
			}
		}
		if err := generated.DisableConnectionPrivateConsent(txctx, tx, parsed); err != nil {
			return fmt.Errorf("revoke connection: remove private data consent: %w", err)
		}
		if connection.AuthMethod != integrations.AuthNone {
			if err := generated.PurgeConnectionFacts(txctx, tx, connection.SubjectID, connection.EnvironmentID); err != nil {
				return fmt.Errorf("revoke connection: purge facts: %w", err)
			}
		}
		if err := generated.PurgeConnectionSyncJobs(txctx, tx, parsed); err != nil {
			return fmt.Errorf("revoke connection: purge sync jobs: %w", err)
		}
		connection.Status = integrations.ConnectionRevoked
		connection.LastError = ""
		connection.TokenExpiresAt = nil
		connection.PrivateDataEnabled = false
		connection.UpdatedAt = now
		revoked = *connection
		return nil
	})
	return revoked, err
}

func (s *Store) ClaimOAuthTokenRevocations(ctx context.Context, now, leaseUntil time.Time, limit int) ([]integrations.OAuthTokenRevocationJob, error) {
	if limit <= 0 || !leaseUntil.After(now) {
		return nil, integrations.ErrInvalidRevocationConfig
	}
	if s.pool == nil {
		return nil, ErrNilDB
	}
	rows, err := generated.ClaimOAuthTokenRevocations(ctx, appdb.PGXExecutorFor(ctx, s.pool), now, leaseUntil, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("claim OAuth token revocations: %w", err)
	}
	jobs := make([]integrations.OAuthTokenRevocationJob, 0, len(rows))
	for _, row := range rows {
		if row.ClaimToken == nil || row.Attempts < 0 || int64(int(row.Attempts)) != row.Attempts {
			return nil, integrations.ErrInvalidRevocationConfig
		}
		job := integrations.OAuthTokenRevocationJob{
			ID: row.Id, ConnectionID: row.ConnectionId, ProviderID: row.ProviderId,
			TokenCiphertext: append([]byte(nil), row.TokenCiphertext...), TokenKeyID: row.TokenKeyId,
			ClaimToken: *row.ClaimToken, Attempts: int(row.Attempts), AvailableAt: row.AvailableAt,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		if row.LeaseUntil != nil {
			value := *row.LeaseUntil
			job.LeaseUntil = &value
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) CompleteOAuthTokenRevocation(ctx context.Context, id, claimToken string) error {
	if id == "" || claimToken == "" {
		return integrations.ErrInvalidRevocationConfig
	}
	if s.pool == nil {
		return ErrNilDB
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return integrations.ErrInvalidRevocationConfig
	}
	parsedClaim, err := uuid.Parse(claimToken)
	if err != nil {
		return integrations.ErrInvalidRevocationConfig
	}
	count, err := generated.CompleteOAuthTokenRevocation(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsedID, parsedClaim)
	if err != nil {
		return fmt.Errorf("complete OAuth token revocation: %w", err)
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
	if s.pool == nil {
		return ErrNilDB
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return integrations.ErrInvalidRevocationConfig
	}
	parsedClaim, err := uuid.Parse(claimToken)
	if err != nil {
		return integrations.ErrInvalidRevocationConfig
	}
	count, err := generated.RetryOAuthTokenRevocation(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsedID, parsedClaim, availableAt)
	if err != nil {
		return fmt.Errorf("retry OAuth token revocation: %w", err)
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
	if s.pool == nil {
		return ErrNilDB
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return integrations.ErrInvalidRevocationConfig
	}
	parsedClaim, err := uuid.Parse(claimToken)
	if err != nil {
		return integrations.ErrInvalidRevocationConfig
	}
	count, err := generated.DeadLetterOAuthTokenRevocation(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsedID, parsedClaim, terminalAt)
	if err != nil {
		return fmt.Errorf("dead-letter OAuth token revocation: %w", err)
	}
	if count != 1 {
		return integrations.ErrConflict
	}
	return nil
}
