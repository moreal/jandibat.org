package cockroach

import (
	"context"
	"crypto/rand"
	"encoding/json"
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
	// adapter-sql-allowlist: virtual-catalog; this fixed information_schema.columns probe reads
	// virtual-catalog metadata, which official unpatched Scythe 0.17.0 cannot
	// model as a source table. It accepts no caller-supplied SQL or identifiers;
	// this limitation is not evidence of an upstream defect.
	query := `SELECT count(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'mutation_audit_outbox'
AND column_name IN (
  'id','audit_event_id','request_id','occurred_at','actor_type','actor_id',
  'action','target_type','target_id','outcome','metadata','status','attempts',
  'available_at','lease_until','claim_token','created_at','updated_at',
  'delivered_at','terminal_at','terminal_reason'
)`
	if store.pool == nil {
		return ErrNilDB
	}
	err := appdb.PGXExecutorFor(ctx, store.pool).QueryRow(ctx, query).Scan(&columns)
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
	return transaction.lazyPGX != nil && transaction.lazyPGX.Active()
}

func (store *Store) BeginMutation(ctx context.Context) (context.Context, operations.MutationAuditTransaction, error) {
	if store.pool == nil {
		return ctx, nil, ErrNilDB
	}
	ctx, lazy := appdb.WithLazyPGXTransaction(ctx, store.pool)
	ctx, hooks := operations.WithMutationRollbackScope(ctx)
	ctx, failure := operations.WithMutationFailureCommitMarker(ctx)
	return ctx, &mutationAuditTransaction{store: store, lazyPGX: lazy, hooks: hooks, failure: failure}, nil
}

func (transaction *mutationAuditTransaction) Enqueue(ctx context.Context, event operations.AuditEvent) error {
	if transaction == nil || transaction.lazyPGX == nil {
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
	outboxUUID, err := uuid.Parse(outboxID)
	if err != nil {
		return err
	}
	eventUUID, err := uuid.Parse(event.ID)
	if err != nil {
		return err
	}
	pgxTx, ok := transaction.lazyPGX.Transaction()
	if !ok {
		// A successful mutation with no participating state write is normally a
		// deployment defect. Only a store-verified durable idempotent replay may
		// start an audit-only transaction at this final outcome boundary.
		pgxTx, err = transaction.lazyPGX.BeginVerifiedReplayAudit(ctx)
		if err != nil {
			return operations.ErrInvalidAuditOutbox
		}
	}
	return generated.InsertMutationAuditOutcome(ctx, pgxTx, outboxUUID, eventUUID,
		event.RequestID, event.OccurredAt, string(event.Actor.Type), &event.Actor.ID,
		event.Action, event.Target.Type, &event.Target.ID, string(event.Outcome), encoded)
}

func (transaction *mutationAuditTransaction) Commit() error {
	if transaction == nil || transaction.lazyPGX == nil {
		return operations.ErrInvalidAuditOutbox
	}
	if err := transaction.lazyPGX.Commit(); err != nil {
		return err
	}
	transaction.hooks.Commit()
	return nil
}

func (transaction *mutationAuditTransaction) Rollback() error {
	if transaction == nil || transaction.lazyPGX == nil {
		return nil
	}
	err := transaction.lazyPGX.Rollback()
	transaction.hooks.Rollback()
	return err
}

func (store *Store) ClaimMutationAudits(ctx context.Context, now time.Time, lease time.Duration, limit, maxAttempts int) ([]operations.MutationAuditDelivery, error) {
	if now.IsZero() || lease <= 0 || limit <= 0 || maxAttempts <= 0 || maxAttempts > 5 {
		return nil, operations.ErrInvalidAuditOutbox
	}
	if store.pool == nil {
		return nil, ErrNilDB
	}
	claim, err := auditOutboxUUID()
	if err != nil {
		return nil, err
	}
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

func (store *Store) DeliverMutationAudit(ctx context.Context, id, claim string, now time.Time) error {
	if id == "" || claim == "" || now.IsZero() {
		return operations.ErrInvalidAuditOutbox
	}
	if store.pool == nil {
		return ErrNilDB
	}
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
		if err := generated.InsertAuditEvent(txctx, tx, eventID, row.OccurredAt,
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

func (store *Store) RetryMutationAudit(ctx context.Context, id, claim string, now, next time.Time, maxAttempts int, reason string) (string, error) {
	if id == "" || claim == "" || now.IsZero() || !next.After(now) || maxAttempts <= 0 || maxAttempts > 5 || reason == "" {
		return "", operations.ErrInvalidAuditOutbox
	}
	if store.pool == nil {
		return "", ErrNilDB
	}
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

func auditOutboxUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
