package cockroach

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

var (
	_ operations.MutationAuditCoordinator = (*Store)(nil)
	_ operations.MutationAuditOutbox      = (*Store)(nil)
)

func (store *Store) CheckMutationAuditOutboxSchema(ctx context.Context) error {
	var columns int
	err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'mutation_audit_outbox'
AND column_name IN (
  'id','audit_event_id','request_id','occurred_at','actor_type','actor_id',
  'action','target_type','target_id','outcome','metadata','status','attempts',
  'available_at','lease_until','claim_token','created_at','updated_at',
  'delivered_at','terminal_at','terminal_reason'
)`).Scan(&columns)
	if err != nil {
		return err
	}
	if columns != 21 {
		return fmt.Errorf("operations cockroach: mutation audit outbox schema is incomplete: got %d of 21 columns", columns)
	}
	return nil
}

type mutationAuditTransaction struct {
	store   *Store
	lazy    *appdb.LazyTransaction
	lazyPGX *appdb.LazyPGXTransaction
	hooks   *operations.MutationRollbackScope
	failure *operations.MutationFailureCommitMarker
}

func (transaction *mutationAuditTransaction) CommitFailure() bool {
	return transaction != nil && transaction.failure.Marked()
}

func (transaction *mutationAuditTransaction) Active() bool {
	if transaction == nil {
		return false
	}
	if transaction.lazyPGX != nil {
		return transaction.lazyPGX.Active()
	}
	if transaction.lazy == nil {
		return false
	}
	_, active := transaction.lazy.Transaction()
	return active
}

func (store *Store) BeginMutation(ctx context.Context) (context.Context, operations.MutationAuditTransaction, error) {
	if store.pool != nil {
		ctx, lazy := appdb.WithLazyPGXTransaction(ctx, store.pool)
		ctx, hooks := operations.WithMutationRollbackScope(ctx)
		ctx, failure := operations.WithMutationFailureCommitMarker(ctx)
		return ctx, &mutationAuditTransaction{store: store, lazyPGX: lazy, hooks: hooks, failure: failure}, nil
	}
	ctx, lazy := appdb.WithLazyTransaction(ctx, store.db)
	ctx, hooks := operations.WithMutationRollbackScope(ctx)
	ctx, failure := operations.WithMutationFailureCommitMarker(ctx)
	return ctx, &mutationAuditTransaction{store: store, lazy: lazy, hooks: hooks, failure: failure}, nil
}

func (transaction *mutationAuditTransaction) Enqueue(ctx context.Context, event operations.AuditEvent) error {
	if transaction == nil || (transaction.lazy == nil && transaction.lazyPGX == nil) {
		return operations.ErrInvalidAuditOutbox
	}
	var sqlTx *sql.Tx
	var pgxTx pgx.Tx
	var ok bool
	if transaction.lazyPGX != nil {
		pgxTx, ok = transaction.lazyPGX.Transaction()
	} else {
		sqlTx, ok = transaction.lazy.Transaction()
	}
	if !ok {
		// A successful registered mutation without a participating state write is
		// a deployment/configuration defect; never create a misleading outcome.
		return operations.ErrInvalidAuditOutbox
	}
	if err := operations.ValidateAuditEvent(event); err != nil {
		return err
	}
	event, err := transaction.store.redactor.RedactEvent(event)
	if err != nil {
		return err
	}
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	if event.SourceIP != "" {
		metadata = cloneAuditMetadata(metadata)
		metadata["source_ip"] = event.SourceIP
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	outboxID, err := operations.NewAuditEventID()
	if err != nil {
		return err
	}
	if pgxTx != nil {
		outboxUUID, err := uuid.Parse(outboxID)
		if err != nil {
			return err
		}
		eventUUID, err := uuid.Parse(event.ID)
		if err != nil {
			return err
		}
		return generated.InsertMutationAuditOutcome(ctx, pgxTx, outboxUUID, eventUUID,
			event.RequestID, event.OccurredAt, string(event.Actor.Type), &event.Actor.ID,
			event.Action, event.Target.Type, &event.Target.ID, string(event.Outcome), encoded)
	}
	_, err = sqlTx.ExecContext(ctx, `
INSERT INTO mutation_audit_outbox (
  id, audit_event_id, request_id, occurred_at, actor_type, actor_id, action,
  target_type, target_id, outcome, metadata, status, attempts, available_at,
  created_at, updated_at
) VALUES (
  $1::UUID, $2::UUID, $3, $4, $5, NULLIF($6, ''), $7,
  $8, NULLIF($9, ''), $10, $11::JSONB, 'pending', 0, $4, $4, $4
)`, outboxID, event.ID, event.RequestID, event.OccurredAt, event.Actor.Type,
		event.Actor.ID, event.Action, event.Target.Type, event.Target.ID,
		event.Outcome, encoded)
	return err
}

func (transaction *mutationAuditTransaction) Commit() error {
	if transaction == nil || (transaction.lazy == nil && transaction.lazyPGX == nil) {
		return operations.ErrInvalidAuditOutbox
	}
	var err error
	if transaction.lazyPGX != nil {
		err = transaction.lazyPGX.Commit()
	} else {
		err = transaction.lazy.Commit()
	}
	if err != nil {
		return err
	}
	transaction.hooks.Commit()
	return nil
}

func (transaction *mutationAuditTransaction) Rollback() error {
	if transaction == nil || (transaction.lazy == nil && transaction.lazyPGX == nil) {
		return nil
	}
	var err error
	if transaction.lazyPGX != nil {
		err = transaction.lazyPGX.Rollback()
	} else {
		err = transaction.lazy.Rollback()
	}
	transaction.hooks.Rollback()
	return err
}

const mutationAuditDeliveryColumns = `
id::STRING, audit_event_id::STRING, request_id, occurred_at, actor_type,
COALESCE(actor_id, ''), action, target_type, COALESCE(target_id, ''), outcome,
metadata, claim_token::STRING, attempts`

func (store *Store) ClaimMutationAudits(ctx context.Context, now time.Time, lease time.Duration, limit, maxAttempts int) ([]operations.MutationAuditDelivery, error) {
	if now.IsZero() || lease <= 0 || limit <= 0 || maxAttempts <= 0 || maxAttempts > 5 {
		return nil, operations.ErrInvalidAuditOutbox
	}
	claim, err := auditOutboxUUID()
	if err != nil {
		return nil, err
	}
	if store.pool != nil {
		claimID, err := uuid.Parse(claim)
		if err != nil {
			return nil, err
		}
		var result []operations.MutationAuditDelivery
		err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			if err := generated.DeadExpiredMutationAudits(txctx, tx, now, int64(maxAttempts)); err != nil {
				return err
			}
			if err := generated.ClaimMutationAuditBatch(txctx, tx, now, now.Add(lease), claimID, int64(maxAttempts), int64(limit)); err != nil {
				return err
			}
			rows, err := generated.ListClaimedMutationAudits(txctx, tx, claimID)
			if err != nil {
				return err
			}
			result = make([]operations.MutationAuditDelivery, 0, len(rows))
			for _, row := range rows {
				if row.ClaimToken == nil || row.Attempts < 0 || int64(int(row.Attempts)) != row.Attempts {
					return operations.ErrInvalidAuditOutbox
				}
				item := operations.MutationAuditDelivery{
					ID: row.Id, ClaimToken: *row.ClaimToken, Attempts: int(row.Attempts),
					Event: operations.AuditEvent{
						ID: row.AuditEventId, RequestID: row.RequestId, OccurredAt: row.OccurredAt,
						Actor:  operations.AuditActor{Type: row.ActorType, ID: row.ActorId},
						Action: row.Action, Target: operations.AuditTarget{Type: row.TargetType, ID: row.TargetId},
						Outcome: operations.AuditOutcome(row.Outcome),
					},
				}
				if err := json.Unmarshal(row.Metadata, &item.Event.Metadata); err != nil {
					return operations.ErrInvalidAuditOutbox
				}
				result = append(result, item)
			}
			return nil
		})
		return result, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE mutation_audit_outbox
SET status = 'dead', lease_until = NULL, claim_token = NULL, updated_at = $1,
    terminal_at = $1, terminal_reason = 'attempts_exhausted'
WHERE status = 'processing' AND lease_until <= $1 AND attempts >= $2`, now, maxAttempts); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mutation_audit_outbox
SET status = 'processing', attempts = attempts + 1, lease_until = $2,
    claim_token = $3, updated_at = $1
WHERE id IN (
  SELECT id FROM mutation_audit_outbox
  WHERE attempts < $4 AND ((status = 'pending' AND available_at <= $1)
    OR (status = 'processing' AND lease_until <= $1))
  ORDER BY available_at, created_at, id LIMIT $5 FOR UPDATE SKIP LOCKED
)`, now, now.Add(lease), claim, maxAttempts, limit); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+mutationAuditDeliveryColumns+`
FROM mutation_audit_outbox WHERE status = 'processing' AND claim_token = $1
ORDER BY available_at, created_at, id`, claim)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]operations.MutationAuditDelivery, 0)
	for rows.Next() {
		var item operations.MutationAuditDelivery
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.Event.ID, &item.Event.RequestID,
			&item.Event.OccurredAt, &item.Event.Actor.Type, &item.Event.Actor.ID,
			&item.Event.Action, &item.Event.Target.Type, &item.Event.Target.ID,
			&item.Event.Outcome, &metadata, &item.ClaimToken, &item.Attempts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(metadata, &item.Event.Metadata); err != nil {
			return nil, operations.ErrInvalidAuditOutbox
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *Store) DeliverMutationAudit(ctx context.Context, id, claim string, now time.Time) error {
	if id == "" || claim == "" || now.IsZero() {
		return operations.ErrInvalidAuditOutbox
	}
	if store.pool != nil {
		outboxID, err := uuid.Parse(id)
		if err != nil {
			return operations.ErrInvalidAuditOutbox
		}
		claimID, err := uuid.Parse(claim)
		if err != nil {
			return operations.ErrInvalidAuditOutbox
		}
		return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			row, err := generated.GetMutationAuditForDelivery(txctx, tx, outboxID, claimID)
			if err != nil {
				return err
			}
			if row == nil {
				return operations.ErrInvalidAuditOutbox
			}
			eventID, err := uuid.Parse(row.AuditEventId)
			if err != nil {
				return operations.ErrInvalidAuditOutbox
			}
			if err := generated.InsertDeliveredMutationAudit(txctx, tx, eventID, row.OccurredAt,
				row.ActorType, &row.ActorId, row.Action, row.TargetType, &row.TargetId,
				row.Outcome, row.RequestId, row.Metadata); err != nil {
				return err
			}
			count, err := generated.MarkMutationAuditDelivered(txctx, tx, outboxID, claimID, now)
			if err != nil {
				return err
			}
			if count != 1 {
				return operations.ErrInvalidAuditOutbox
			}
			return nil
		})
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var event operations.AuditEvent
	var metadata []byte
	err = tx.QueryRowContext(ctx, `SELECT audit_event_id::STRING, occurred_at, actor_type,
COALESCE(actor_id, ''), action, target_type, COALESCE(target_id, ''), outcome,
request_id, metadata FROM mutation_audit_outbox
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'
FOR UPDATE`, id, claim).Scan(&event.ID, &event.OccurredAt, &event.Actor.Type,
		&event.Actor.ID, &event.Action, &event.Target.Type, &event.Target.ID,
		&event.Outcome, &event.RequestID, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return operations.ErrInvalidAuditOutbox
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO audit_events (
  id, occurred_at, actor_type, actor_id, action, target_type, target_id,
  outcome, request_id, metadata
) VALUES ($1::UUID, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''), $8, $9, $10::JSONB)`, event.ID, event.OccurredAt, event.Actor.Type, event.Actor.ID,
		event.Action, event.Target.Type, event.Target.ID, event.Outcome,
		event.RequestID, metadata)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE mutation_audit_outbox
SET status = 'delivered', lease_until = NULL, claim_token = NULL,
    delivered_at = $3, updated_at = $3
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'`, id, claim, now)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return operations.ErrInvalidAuditOutbox
	}
	return tx.Commit()
}

func (store *Store) RetryMutationAudit(ctx context.Context, id, claim string, now, next time.Time, maxAttempts int, reason string) (string, error) {
	if id == "" || claim == "" || now.IsZero() || !next.After(now) || maxAttempts <= 0 || maxAttempts > 5 || reason == "" {
		return "", operations.ErrInvalidAuditOutbox
	}
	if store.pool != nil {
		outboxID, err := uuid.Parse(id)
		if err != nil {
			return "", operations.ErrInvalidAuditOutbox
		}
		claimID, err := uuid.Parse(claim)
		if err != nil {
			return "", operations.ErrInvalidAuditOutbox
		}
		var status string
		err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			row, err := generated.RetryMutationAuditClaim(txctx, tx,
				outboxID, claimID, now, next, int64(maxAttempts), reason)
			if err != nil {
				return err
			}
			if row == nil {
				return operations.ErrInvalidAuditOutbox
			}
			status = row.Status
			return nil
		})
		if err != nil {
			return "", err
		}
		return status, nil
	}
	var status string
	err := store.db.QueryRowContext(ctx, `UPDATE mutation_audit_outbox
SET status = CASE WHEN attempts >= $5 THEN 'dead' ELSE 'pending' END,
    available_at = CASE WHEN attempts >= $5 THEN available_at ELSE $4 END,
    lease_until = NULL, claim_token = NULL, updated_at = $3,
    terminal_at = CASE WHEN attempts >= $5 THEN $3 ELSE NULL END,
    terminal_reason = CASE WHEN attempts >= $5 THEN $6 ELSE NULL END
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'
RETURNING status`, id, claim, now, next, maxAttempts, reason).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", operations.ErrInvalidAuditOutbox
	}
	return status, err
}

func auditOutboxUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
