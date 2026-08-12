package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func (store *Store) LoadCheckpoint(ctx context.Context, operation operations.MaintenanceOperation, scope string) (operations.MaintenanceCheckpoint, bool, error) {
	if strings.TrimSpace(scope) == "" {
		return operations.MaintenanceCheckpoint{}, false, operations.ErrInvalidMaintenanceCheckpoint
	}
	var checkpoint operations.MaintenanceCheckpoint
	var payload []byte
	err := store.db.QueryRowContext(ctx, `
SELECT operation, scope, payload, updated_at
FROM maintenance_checkpoints
WHERE operation = $1 AND scope = $2`, operation, scope).Scan(
		&checkpoint.Operation, &checkpoint.Scope, &payload, &checkpoint.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return operations.MaintenanceCheckpoint{}, false, nil
	}
	if err != nil {
		return operations.MaintenanceCheckpoint{}, false, fmt.Errorf("load maintenance checkpoint: %w", err)
	}
	checkpoint.Payload = append(json.RawMessage(nil), payload...)
	if err := operations.ValidateMaintenanceCheckpoint(checkpoint); err != nil {
		return operations.MaintenanceCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

func (store *Store) SaveCheckpoint(ctx context.Context, checkpoint operations.MaintenanceCheckpoint) error {
	if err := operations.ValidateMaintenanceCheckpoint(checkpoint); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO maintenance_checkpoints (operation, scope, payload, updated_at)
VALUES ($1, $2, $3::JSONB, $4)
ON CONFLICT (operation, scope) DO UPDATE SET
  payload = excluded.payload,
  updated_at = excluded.updated_at`, checkpoint.Operation, checkpoint.Scope, []byte(checkpoint.Payload), checkpoint.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("save maintenance checkpoint: %w", err)
	}
	return nil
}

func (store *Store) DeleteCheckpoint(ctx context.Context, operation operations.MaintenanceOperation, scope string) error {
	if strings.TrimSpace(scope) == "" {
		return operations.ErrInvalidMaintenanceCheckpoint
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM maintenance_checkpoints WHERE operation = $1 AND scope = $2`, operation, scope); err != nil {
		return fmt.Errorf("delete maintenance checkpoint: %w", err)
	}
	return nil
}
