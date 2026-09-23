package cockroach

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

// TryAcquireSyncExecution uses a bounded lease in the existing sync_cursor
// JSONB column so independent API replicas cannot execute the same connection
// concurrently. An abandoned lease self-recovers after syncExecutionLease.
func (s *Store) TryAcquireSyncExecution(ctx context.Context, connectionID string) (string, bool, error) {
	if s.pool == nil {
		return "", false, ErrNilDB
	}
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

func (s *Store) ReleaseSyncExecution(ctx context.Context, connectionID, claimToken string) error {
	if s.pool == nil {
		return ErrNilDB
	}
	id, err := uuid.Parse(connectionID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	if err := generated.ReleaseSyncExecution(ctx, appdb.PGXExecutorFor(ctx, s.pool), id, claimToken); err != nil {
		return fmt.Errorf("release sync execution: %w", err)
	}
	return nil
}
