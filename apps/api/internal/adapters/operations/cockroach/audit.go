package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const insertAuditEventQuery = `
INSERT INTO audit_events (
  id, occurred_at, actor_type, actor_id, action, target_type, target_id,
  outcome, request_id, metadata
) VALUES (
  $1::UUID, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''), $8, $9, $10::JSONB
)`

func (store *Store) WriteAuditEvent(ctx context.Context, event operations.AuditEvent) error {
	if store.pool != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		redacted, encoded, err := store.prepareAuditEvent(event)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(redacted.ID)
		if err != nil {
			return fmt.Errorf("write audit event: %w", err)
		}
		err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			return generated.InsertAuditEvent(txctx, tx, id, redacted.OccurredAt,
				string(redacted.Actor.Type), &redacted.Actor.ID, redacted.Action,
				redacted.Target.Type, &redacted.Target.ID, string(redacted.Outcome),
				redacted.RequestID, encoded)
		})
		if err != nil {
			return fmt.Errorf("write audit event: %w", err)
		}
		return nil
	}
	return store.writeAuditEvent(ctx, store.db, event)
}

type auditExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (store *Store) writeAuditEvent(ctx context.Context, executor auditExecutor, event operations.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	redacted, encoded, err := store.prepareAuditEvent(event)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, insertAuditEventQuery,
		redacted.ID, redacted.OccurredAt, redacted.Actor.Type, redacted.Actor.ID,
		redacted.Action, redacted.Target.Type, redacted.Target.ID, redacted.Outcome,
		redacted.RequestID, encoded,
	)
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func (store *Store) prepareAuditEvent(event operations.AuditEvent) (operations.AuditEvent, []byte, error) {
	if err := operations.ValidateAuditEvent(event); err != nil {
		return operations.AuditEvent{}, nil, err
	}
	redacted, err := store.redactor.RedactEvent(event)
	if err != nil {
		return operations.AuditEvent{}, nil, err
	}
	metadata := redacted.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if redacted.SourceIP != "" {
		metadata = cloneAuditMetadata(metadata)
		metadata["source_ip"] = redacted.SourceIP
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return operations.AuditEvent{}, nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	return redacted, encoded, nil
}

func cloneAuditMetadata(metadata map[string]any) map[string]any {
	result := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		result[key] = value
	}
	return result
}
