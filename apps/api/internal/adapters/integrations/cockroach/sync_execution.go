package cockroach

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const acquireSyncExecutionQuery = `
UPDATE provider_connections
SET sync_cursor = COALESCE(sync_cursor, '{}'::JSONB)
    || jsonb_build_object(
      'sync_execution_expires_at', now() + INTERVAL '20 minutes',
      'sync_execution_claim_token', gen_random_uuid()::STRING
    )
WHERE id = $1::UUID
	AND COALESCE(sync_cursor->>'connection_status', status) IN ('active', 'error')
  AND COALESCE(
    NULLIF(sync_cursor->>'sync_execution_expires_at', '')::TIMESTAMPTZ,
    'epoch'::TIMESTAMPTZ
  ) <= now()
RETURNING sync_cursor->>'sync_execution_claim_token'`

const releaseSyncExecutionQuery = `
UPDATE provider_connections
SET sync_cursor = (COALESCE(sync_cursor, '{}'::JSONB) - 'sync_execution_expires_at') - 'sync_execution_claim_token'
WHERE id = $1::UUID AND sync_cursor->>'sync_execution_claim_token' = $2`

// TryAcquireSyncExecution uses a bounded lease in the existing sync_cursor
// JSONB column so independent API replicas cannot execute the same connection
// concurrently. An abandoned lease self-recovers after syncExecutionLease.
func (s *Store) TryAcquireSyncExecution(ctx context.Context, connectionID string) (string, bool, error) {
	if s.pool != nil {
		id, err := uuid.Parse(connectionID)
		if err != nil {
			return "", false, integrations.ErrInvalidIdentifier
		}
		row, err := generated.AcquireSyncExecution(ctx, appdb.PGXExecutorFor(ctx, s.pool), id)
		if err != nil {
			return "", false, fmt.Errorf("acquire sync execution: %w", err)
		}
		if row == nil {
			return "", false, nil
		}
		if row.ClaimToken == nil || *row.ClaimToken == "" {
			return "", false, integrations.ErrConflict
		}
		return *row.ClaimToken, true, nil
	}
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return "", false, err
	}
	var claimToken string
	err = executor.QueryRowContext(ctx, acquireSyncExecutionQuery, connectionID).Scan(&claimToken)
	if err == nil {
		return claimToken, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, fmt.Errorf("acquire sync execution: %w", err)
}

func (s *Store) ReleaseSyncExecution(ctx context.Context, connectionID, claimToken string) error {
	if s.pool != nil {
		id, err := uuid.Parse(connectionID)
		if err != nil {
			return integrations.ErrInvalidIdentifier
		}
		if err := generated.ReleaseSyncExecution(ctx, appdb.PGXExecutorFor(ctx, s.pool), id, claimToken); err != nil {
			return fmt.Errorf("release sync execution: %w", err)
		}
		return nil
	}
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	if _, err := executor.ExecContext(ctx, releaseSyncExecutionQuery, connectionID, claimToken); err != nil {
		return fmt.Errorf("release sync execution: %w", err)
	}
	return nil
}
