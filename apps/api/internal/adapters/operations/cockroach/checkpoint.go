package cockroach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

var errCheckpointPGXPoolRequired = errors.New("maintenance checkpoint: pgx pool is required")

func (store *Store) LoadCheckpoint(ctx context.Context, operation operations.MaintenanceOperation, scope string) (operations.MaintenanceCheckpoint, bool, error) {
	if strings.TrimSpace(scope) == "" {
		return operations.MaintenanceCheckpoint{}, false, operations.ErrInvalidMaintenanceCheckpoint
	}
	if store.pool == nil {
		return operations.MaintenanceCheckpoint{}, false, errCheckpointPGXPoolRequired
	}
	row, err := generated.GetMaintenanceCheckpoint(ctx, appdb.PGXExecutorFor(ctx, store.pool), string(operation), scope)
	if err != nil {
		return operations.MaintenanceCheckpoint{}, false, fmt.Errorf("load maintenance checkpoint: %w", err)
	}
	if row == nil {
		return operations.MaintenanceCheckpoint{}, false, nil
	}
	checkpoint := operations.MaintenanceCheckpoint{
		Operation: operations.MaintenanceOperation(row.Operation), Scope: row.Scope,
		Payload: append(json.RawMessage(nil), row.Payload...), UpdatedAt: row.UpdatedAt,
	}
	if err := operations.ValidateMaintenanceCheckpoint(checkpoint); err != nil {
		return operations.MaintenanceCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

func (store *Store) SaveCheckpoint(ctx context.Context, checkpoint operations.MaintenanceCheckpoint) error {
	if err := operations.ValidateMaintenanceCheckpoint(checkpoint); err != nil {
		return err
	}
	if store.pool == nil {
		return errCheckpointPGXPoolRequired
	}
	err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		return generated.SaveMaintenanceCheckpoint(txctx, tx, string(checkpoint.Operation),
			checkpoint.Scope, checkpoint.Payload, checkpoint.UpdatedAt.UTC())
	})
	if err != nil {
		return fmt.Errorf("save maintenance checkpoint: %w", err)
	}
	return nil
}

func (store *Store) DeleteCheckpoint(ctx context.Context, operation operations.MaintenanceOperation, scope string) error {
	if strings.TrimSpace(scope) == "" {
		return operations.ErrInvalidMaintenanceCheckpoint
	}
	if store.pool == nil {
		return errCheckpointPGXPoolRequired
	}
	err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		return generated.DeleteMaintenanceCheckpoint(txctx, tx, string(operation), scope)
	})
	if err != nil {
		return fmt.Errorf("delete maintenance checkpoint: %w", err)
	}
	return nil
}
