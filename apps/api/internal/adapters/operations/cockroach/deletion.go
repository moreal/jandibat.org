package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const deletionBaseColumns = `
id::STRING, request_id, target_type, target_id, status,
COALESCE(last_completed_stage, ''), COALESCE(error_code, ''), COALESCE(array_to_json(subject_ids), '[]'::JSON),
requested_at, updated_at, completed_at, backup_expiry_at,
COALESCE(audit_event_id::STRING, '')`

const deletionColumns = deletionBaseColumns + `,
COALESCE((SELECT claim.attempts FROM deletion_request_claims AS claim WHERE claim.deletion_request_id = deletion_requests.id), 0),
COALESCE((SELECT claim.available_at FROM deletion_request_claims AS claim WHERE claim.deletion_request_id = deletion_requests.id), requested_at),
(SELECT claim.lease_until FROM deletion_request_claims AS claim WHERE claim.deletion_request_id = deletion_requests.id),
COALESCE((SELECT claim.claim_token::STRING FROM deletion_request_claims AS claim WHERE claim.deletion_request_id = deletion_requests.id), '')`

// The API role can enqueue and read its deletion request, but intentionally
// cannot inspect maintenance-only lease coordination.
const deletionRequesterColumns = deletionBaseColumns + `, 0, requested_at, NULL, ''`

const deletionInboxColumns = `
id::STRING, request_id, target_type, target_id, 'requested', 'requested', '', '[]'::JSON,
requested_at, requested_at, NULL, NULL, '', 0, requested_at, NULL, ''`

// Named query constants keep destructive SQL reviewable and let unit tests
// assert that the workflow and its ten residual checks do not silently drift.
const deletionLegalHoldQuery = `
SELECT EXISTS (
  SELECT 1 FROM legal_holds AS hold
  WHERE hold.expires_at > $3
    AND (
      (hold.target_type = $1 AND hold.target_id = $2)
      OR ($1 = 'account' AND hold.target_type = 'subject' AND EXISTS (
        SELECT 1 FROM subjects WHERE subjects.id = hold.target_id AND subjects.owner_user_id = $2
      ))
      OR ($1 = 'subject' AND hold.target_type = 'account' AND EXISTS (
        SELECT 1 FROM subjects WHERE subjects.id = $2 AND subjects.owner_user_id = hold.target_id
      ))
    )
)`

const deletionVerificationQuery = `
SELECT
  (SELECT count(*) FROM subjects WHERE owner_user_id = $1),
  (SELECT count(*) FROM user_passkeys WHERE user_id = $1),
  (SELECT count(*) FROM user_sessions WHERE user_id = $1),
  (SELECT count(*) FROM magic_link_tokens WHERE user_id = $1),
  (SELECT count(*) FROM auth_challenges WHERE user_id = $1 OR payload->>'SubjectID' = ANY($2::STRING[])),
  (SELECT count(*) FROM provider_connections WHERE subject_id = ANY($2::STRING[])),
  (SELECT count(*) FROM provider_connection_private_consents WHERE connection_id IN (
    SELECT id FROM provider_connections WHERE subject_id = ANY($2::STRING[])
  )),
  (SELECT count(*) FROM custom_providers WHERE subject_id = ANY($2::STRING[])),
  (SELECT count(*) FROM activity_facts WHERE subject_id = ANY($2::STRING[])),
  (SELECT count(*) FROM timeline_cache WHERE subject_id = ANY($2::STRING[])),
  (SELECT count(*) FROM activity_refresh_cache WHERE subject_id = ANY($2::STRING[])),
  (SELECT count(*) FROM provider_sync_jobs WHERE subject_id = ANY($2::STRING[]))`

func (store *Store) CreateOrLoadDeletion(ctx context.Context, request operations.DeletionRequest) (operations.DeletionRequest, error) {
	if err := operations.ValidateDeletionRequest(request); err != nil {
		return operations.DeletionRequest{}, err
	}
	if store.pool != nil {
		var stored operations.DeletionRequest
		err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			if err := generated.InsertDeletionRequestIfAbsent(txctx, tx, request.RequestID,
				string(request.TargetType), request.TargetID, request.RequestedAt.UTC()); err != nil {
				return fmt.Errorf("create deletion request: %w", err)
			}
			row, err := generated.GetDeletionRequestForRequester(txctx, tx, request.RequestID,
				string(request.TargetType), request.TargetID)
			if err != nil {
				return fmt.Errorf("load deletion request: %w", err)
			}
			if row == nil {
				return operations.ErrDeletionNotFound
			}
			stored, err = requesterDeletionFromGenerated(*row)
			if err != nil {
				return err
			}
			if stored.TargetType != request.TargetType || stored.TargetID != request.TargetID {
				return fmt.Errorf("%w: request ID is already bound to another target", operations.ErrInvalidDeletionRequest)
			}
			return nil
		})
		if err != nil {
			return operations.DeletionRequest{}, err
		}
		return stored, nil
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO deletion_requests (
  request_id, target_type, target_id, status, last_completed_stage,
  subject_ids, requested_at, updated_at
) VALUES ($1, $2, $3, 'requested', 'requested', ARRAY[]::STRING[], $4, $4)
ON CONFLICT DO NOTHING`, request.RequestID, request.TargetType, request.TargetID, request.RequestedAt.UTC())
	if err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("create deletion request: %w", err)
	}
	stored, err := scanDeletion(store.db.QueryRowContext(ctx, `SELECT `+deletionRequesterColumns+`
FROM deletion_requests
WHERE request_id = $1 OR (target_type = $2 AND target_id = $3 AND status <> 'completed')
ORDER BY (request_id = $1) DESC, requested_at
LIMIT 1`, request.RequestID, request.TargetType, request.TargetID))
	if err != nil {
		return operations.DeletionRequest{}, err
	}
	if stored.TargetType != request.TargetType || stored.TargetID != request.TargetID {
		return operations.DeletionRequest{}, fmt.Errorf("%w: request ID is already bound to another target", operations.ErrInvalidDeletionRequest)
	}
	return stored, nil
}

func requesterDeletionFromGenerated(row generated.GetDeletionRequestForRequesterRow) (operations.DeletionRequest, error) {
	request := operations.DeletionRequest{
		ID: row.Id, RequestID: row.RequestId,
		TargetType: operations.DeletionTargetType(row.TargetType), TargetID: row.TargetId,
		Status: operations.DeletionStatus(row.Status), LastCompletedStage: operations.DeletionStage(row.LastCompletedStage),
		ErrorCode: row.ErrorCode, RequestedAt: row.RequestedAt, UpdatedAt: row.UpdatedAt,
		CompletedAt: row.CompletedAt, BackupExpiryAt: row.BackupExpiryAt, AuditEventID: row.AuditEventId,
		AvailableAt: row.RequestedAt,
	}
	if err := json.Unmarshal(row.SubjectIds, &request.SubjectIDs); err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("scan deletion request subject IDs: %w", err)
	}
	return request, nil
}

func (store *Store) EnqueueDeletion(ctx context.Context, request operations.DeletionRequest) (operations.DeletionRequest, error) {
	if err := operations.ValidateDeletionRequest(request); err != nil {
		return operations.DeletionRequest{}, err
	}
	if store.pool != nil {
		executor := appdb.PGXExecutorFor(ctx, store.pool)
		row, err := generated.GetDeletionInboxForRequest(ctx, executor, request.RequestID, string(request.TargetType), request.TargetID)
		if err != nil {
			return operations.DeletionRequest{}, fmt.Errorf("load deletion inbox: %w", err)
		}
		if row == nil {
			if err := generated.InsertDeletionInboxIfAbsent(ctx, executor, request.RequestID, string(request.TargetType), request.TargetID, request.RequestedAt.UTC()); err != nil {
				return operations.DeletionRequest{}, fmt.Errorf("enqueue deletion request: %w", err)
			}
			row, err = generated.GetDeletionInboxForRequest(ctx, executor, request.RequestID, string(request.TargetType), request.TargetID)
			if err != nil {
				return operations.DeletionRequest{}, fmt.Errorf("load deletion inbox: %w", err)
			}
		}
		if row == nil {
			return operations.DeletionRequest{}, operations.ErrDeletionNotFound
		}
		if operations.DeletionTargetType(row.TargetType) != request.TargetType || row.TargetId != request.TargetID {
			return operations.DeletionRequest{}, fmt.Errorf("%w: request ID is already bound to another target", operations.ErrInvalidDeletionRequest)
		}
		return inboxDeletionFromGenerated(*row), nil
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return operations.DeletionRequest{}, err
	}
	stored, err := scanDeletion(executor.QueryRowContext(ctx, `SELECT `+deletionInboxColumns+`
FROM deletion_request_inbox
WHERE request_id = $1 OR (target_type = $2 AND target_id = $3)
ORDER BY (request_id = $1) DESC, requested_at
LIMIT 1`, request.RequestID, request.TargetType, request.TargetID))
	if err == nil {
		if stored.TargetType != request.TargetType || stored.TargetID != request.TargetID {
			return operations.DeletionRequest{}, fmt.Errorf("%w: request ID is already bound to another target", operations.ErrInvalidDeletionRequest)
		}
		return stored, nil
	}
	if !errors.Is(err, operations.ErrDeletionNotFound) {
		return operations.DeletionRequest{}, err
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO deletion_request_inbox (request_id, target_type, target_id, requested_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING`, request.RequestID, request.TargetType, request.TargetID, request.RequestedAt.UTC())
	if err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("enqueue deletion request: %w", err)
	}
	stored, err = scanDeletion(executor.QueryRowContext(ctx, `SELECT `+deletionInboxColumns+`
FROM deletion_request_inbox
WHERE request_id = $1 OR (target_type = $2 AND target_id = $3)
ORDER BY (request_id = $1) DESC, requested_at
LIMIT 1`, request.RequestID, request.TargetType, request.TargetID))
	if err != nil {
		return operations.DeletionRequest{}, err
	}
	if stored.TargetType != request.TargetType || stored.TargetID != request.TargetID {
		return operations.DeletionRequest{}, fmt.Errorf("%w: request ID is already bound to another target", operations.ErrInvalidDeletionRequest)
	}
	return stored, nil
}

func inboxDeletionFromGenerated(row generated.GetDeletionInboxForRequestRow) operations.DeletionRequest {
	return operations.DeletionRequest{
		ID: row.Id, RequestID: row.RequestId,
		TargetType: operations.DeletionTargetType(row.TargetType), TargetID: row.TargetId,
		Status: operations.DeletionRequested, LastCompletedStage: operations.DeletionStageRequested,
		SubjectIDs: []string{}, RequestedAt: row.RequestedAt, UpdatedAt: row.RequestedAt,
		AvailableAt: row.RequestedAt,
	}
}

func (store *Store) LoadDeletion(ctx context.Context, requestID string) (operations.DeletionRequest, error) {
	if strings.TrimSpace(requestID) == "" {
		return operations.DeletionRequest{}, operations.ErrInvalidDeletionRequest
	}
	if store.pool != nil {
		row, err := generated.GetDeletionRequestById(ctx, appdb.PGXExecutorFor(ctx, store.pool), requestID)
		if err != nil {
			return operations.DeletionRequest{}, fmt.Errorf("load deletion request: %w", err)
		}
		if row == nil {
			return operations.DeletionRequest{}, operations.ErrDeletionNotFound
		}
		request := operations.DeletionRequest{
			ID: row.Id, RequestID: row.RequestId,
			TargetType: operations.DeletionTargetType(row.TargetType), TargetID: row.TargetId,
			Status: operations.DeletionStatus(row.Status), LastCompletedStage: operations.DeletionStage(row.LastCompletedStage),
			ErrorCode: row.ErrorCode, RequestedAt: row.RequestedAt, UpdatedAt: row.UpdatedAt,
			CompletedAt: row.CompletedAt, BackupExpiryAt: row.BackupExpiryAt, AuditEventID: row.AuditEventId,
			Attempts: row.Attempts, AvailableAt: row.AvailableAt, LeaseUntil: row.LeaseUntil, ClaimToken: row.ClaimToken,
		}
		if err := json.Unmarshal(row.SubjectIds, &request.SubjectIDs); err != nil {
			return operations.DeletionRequest{}, fmt.Errorf("scan deletion request subject IDs: %w", err)
		}
		return request, nil
	}
	return scanDeletion(store.db.QueryRowContext(ctx, `SELECT `+deletionColumns+` FROM deletion_requests WHERE request_id = $1`, requestID))
}

func (store *Store) ClaimDeletions(ctx context.Context, now, leaseUntil time.Time, limit int) ([]operations.DeletionRequest, error) {
	if now.IsZero() || !leaseUntil.After(now) || limit <= 0 {
		return nil, operations.ErrInvalidDeletionRequest
	}
	if store.pool != nil {
		var claimed []operations.DeletionRequest
		err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			if err := promoteDeletionInboxPGX(txctx, tx, now, limit); err != nil {
				return err
			}
			if err := generated.BootstrapPendingDeletionClaims(txctx, tx, now.UTC()); err != nil {
				return fmt.Errorf("claim deletion requests: bootstrap: %w", err)
			}
			rows, err := generated.ListClaimableDeletionRequests(txctx, tx, now.UTC(), int64(limit))
			if err != nil {
				return fmt.Errorf("claim deletion requests: select: %w", err)
			}
			claimed = make([]operations.DeletionRequest, 0, len(rows))
			for _, row := range rows {
				request, err := store.ClaimDeletion(txctx, row.RequestId, now, leaseUntil)
				if err != nil {
					return fmt.Errorf("claim deletion request %s: %w", row.RequestId, err)
				}
				claimed = append(claimed, request)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return claimed, nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim deletion requests: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := promoteDeletionInbox(ctx, tx, now, limit); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO deletion_request_claims (deletion_request_id, available_at, updated_at)
SELECT request.id, LEAST(request.requested_at, $1), $1
FROM deletion_requests AS request
WHERE request.status <> 'completed'
ON CONFLICT (deletion_request_id) DO NOTHING`, now.UTC()); err != nil {
		return nil, fmt.Errorf("claim deletion requests: bootstrap: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT request.request_id
FROM deletion_request_claims AS claim
JOIN deletion_requests AS request ON request.id = claim.deletion_request_id
WHERE request.status <> 'completed'
  AND claim.attempts < 100
  AND claim.available_at <= $1
  AND (claim.lease_until IS NULL OR claim.lease_until <= $1)
ORDER BY claim.available_at, claim.updated_at, claim.deletion_request_id
LIMIT $2
FOR UPDATE OF claim SKIP LOCKED`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim deletion requests: select: %w", err)
	}
	requestIDs, err := scanStringRows(rows)
	if err != nil {
		return nil, fmt.Errorf("claim deletion requests: scan: %w", err)
	}
	claimed := make([]operations.DeletionRequest, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		claimToken, tokenErr := operations.NewAuditEventID()
		if tokenErr != nil {
			return nil, fmt.Errorf("claim deletion request token: %w", tokenErr)
		}
		result, updateErr := tx.ExecContext(ctx, `
UPDATE deletion_request_claims SET
  claim_token = $2::UUID, lease_until = $3, attempts = attempts + 1, updated_at = $1
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $4)
  AND attempts < 100
  AND available_at <= $1
  AND (lease_until IS NULL OR lease_until <= $1)`, now.UTC(), claimToken, leaseUntil.UTC(), requestID)
		if updateErr != nil {
			return nil, fmt.Errorf("claim deletion request %s: %w", requestID, updateErr)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			if affectedErr != nil {
				return nil, fmt.Errorf("claim deletion request %s rows affected: %w", requestID, affectedErr)
			}
			return nil, operations.ErrDeletionLeaseLost
		}
		request, loadErr := scanDeletion(tx.QueryRowContext(ctx, `SELECT `+deletionColumns+`
FROM deletion_requests WHERE request_id = $1`, requestID))
		if loadErr != nil {
			return nil, fmt.Errorf("load claimed deletion request %s: %w", requestID, loadErr)
		}
		claimed = append(claimed, request)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim deletion requests: commit: %w", err)
	}
	return claimed, nil
}

func promoteDeletionInboxPGX(ctx context.Context, tx pgx.Tx, now time.Time, limit int) error {
	rows, err := generated.ListDeletionInboxForPromotion(ctx, tx, int64(limit))
	if err != nil {
		return fmt.Errorf("promote deletion inbox: select: %w", err)
	}
	for _, row := range rows {
		inboxID, err := uuid.Parse(row.Id)
		if err != nil {
			return fmt.Errorf("promote deletion inbox: invalid ID: %w", err)
		}
		if err := generated.PromoteDeletionInboxRequest(ctx, tx, row.RequestId, row.TargetType,
			row.TargetId, row.RequestedAt.UTC(), now.UTC()); err != nil {
			return fmt.Errorf("promote deletion inbox %s: %w", row.RequestId, err)
		}
		promoted, err := generated.GetDeletionIdForExactTarget(ctx, tx, row.RequestId, row.TargetType, row.TargetId)
		if err != nil {
			return fmt.Errorf("promote deletion inbox %s: %w", row.RequestId, err)
		}
		if promoted == nil {
			// An existing incomplete request owns the target uniqueness key.
			if err := generated.AcknowledgeDuplicateDeletionInbox(ctx, tx, inboxID, now.UTC()); err != nil {
				return fmt.Errorf("acknowledge duplicate deletion inbox %s: %w", row.RequestId, err)
			}
			continue
		}
		deletionID, err := uuid.Parse(promoted.Id)
		if err != nil {
			return fmt.Errorf("promote deletion inbox: invalid request ID: %w", err)
		}
		if err := generated.BootstrapDeletionClaim(ctx, tx, deletionID, now.UTC()); err != nil {
			return fmt.Errorf("promote deletion inbox claim %s: %w", row.RequestId, err)
		}
		count, err := generated.DeletePromotedDeletionInboxById(ctx, tx, inboxID)
		if err != nil {
			return fmt.Errorf("promote deletion inbox cleanup %s: %w", row.RequestId, err)
		}
		if count != 1 {
			return operations.ErrDeletionLeaseLost
		}
	}
	return nil
}

func (store *Store) ClaimDeletion(ctx context.Context, requestID string, now, leaseUntil time.Time) (operations.DeletionRequest, error) {
	if strings.TrimSpace(requestID) == "" || now.IsZero() || !leaseUntil.After(now) {
		return operations.DeletionRequest{}, operations.ErrInvalidDeletionRequest
	}
	if store.pool != nil {
		var claimed operations.DeletionRequest
		err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			active, err := generated.GetActiveDeletionId(txctx, tx, requestID)
			if err != nil {
				return fmt.Errorf("claim deletion request: lock request: %w", err)
			}
			var deletionID string
			if active != nil {
				deletionID = active.Id
			} else {
				inbox, err := generated.LockDeletionInboxForClaim(txctx, tx, requestID)
				if err != nil {
					return fmt.Errorf("claim deletion request: lock inbox: %w", err)
				}
				if inbox == nil {
					return operations.ErrDeletionNotFound
				}
				if err := generated.PromoteDeletionInboxRequest(txctx, tx, requestID,
					inbox.TargetType, inbox.TargetId, inbox.RequestedAt.UTC(), now.UTC()); err != nil {
					return fmt.Errorf("claim deletion request: promote inbox: %w", err)
				}
				promoted, err := generated.GetDeletionIdForExactTarget(txctx, tx, requestID, inbox.TargetType, inbox.TargetId)
				if err != nil {
					return fmt.Errorf("claim deletion request: promote inbox: %w", err)
				}
				if promoted == nil {
					return operations.ErrDeletionNotFound
				}
				deletionID = promoted.Id
				if err := generated.DeletePromotedDeletionInbox(txctx, tx, requestID); err != nil {
					return fmt.Errorf("claim deletion request: cleanup inbox: %w", err)
				}
			}
			deletionUUID, err := uuid.Parse(deletionID)
			if err != nil {
				return fmt.Errorf("claim deletion request: invalid ID: %w", err)
			}
			if err := generated.BootstrapDeletionClaim(txctx, tx, deletionUUID, now.UTC()); err != nil {
				return fmt.Errorf("claim deletion request: bootstrap: %w", err)
			}
			claimToken, err := operations.NewAuditEventID()
			if err != nil {
				return fmt.Errorf("claim deletion request: token: %w", err)
			}
			claimUUID, err := uuid.Parse(claimToken)
			if err != nil {
				return fmt.Errorf("claim deletion request: token: %w", err)
			}
			count, err := generated.AcquireDeletionClaim(txctx, tx, now.UTC(), claimUUID, leaseUntil.UTC(), deletionUUID)
			if err != nil {
				return fmt.Errorf("claim deletion request: update: %w", err)
			}
			if count != 1 {
				return operations.ErrDeletionLeaseLost
			}
			claimed, err = store.LoadDeletion(txctx, requestID)
			return err
		})
		if err != nil {
			return operations.DeletionRequest{}, err
		}
		return claimed, nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var deletionID string
	if err := tx.QueryRowContext(ctx, `SELECT id::STRING FROM deletion_requests
WHERE request_id = $1 AND status <> 'completed'`, requestID).Scan(&deletionID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: lock request: %w", err)
		}
		var targetType, targetID string
		var requestedAt time.Time
		if err := tx.QueryRowContext(ctx, `SELECT target_type, target_id, requested_at
FROM deletion_request_inbox WHERE request_id = $1 AND status = 'requested' FOR UPDATE`, requestID).
			Scan(&targetType, &targetID, &requestedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return operations.DeletionRequest{}, operations.ErrDeletionNotFound
			}
			return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: lock inbox: %w", err)
		}
		err := tx.QueryRowContext(ctx, `INSERT INTO deletion_requests (
  request_id, target_type, target_id, status, last_completed_stage, subject_ids, requested_at, updated_at
) VALUES ($1, $2, $3, 'requested', 'requested', ARRAY[]::STRING[], $4, $5)
ON CONFLICT DO NOTHING RETURNING id::STRING`, requestID, targetType, targetID, requestedAt.UTC(), now.UTC()).Scan(&deletionID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT id::STRING FROM deletion_requests
WHERE request_id = $1 AND target_type = $2 AND target_id = $3`, requestID, targetType, targetID).Scan(&deletionID)
		}
		if err != nil {
			return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: promote inbox: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM deletion_request_inbox WHERE request_id = $1`, requestID); err != nil {
			return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: cleanup inbox: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deletion_request_claims (deletion_request_id, available_at, updated_at)
VALUES ($1::UUID, $2, $2) ON CONFLICT (deletion_request_id) DO NOTHING`, deletionID, now.UTC()); err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: bootstrap: %w", err)
	}
	claimToken, err := operations.NewAuditEventID()
	if err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: token: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE deletion_request_claims SET
  claim_token = $2::UUID, lease_until = $3, attempts = attempts + 1, updated_at = $1
WHERE deletion_request_id = $4::UUID
  AND attempts < 100 AND available_at <= $1
  AND (lease_until IS NULL OR lease_until <= $1)`, now.UTC(), claimToken, leaseUntil.UTC(), deletionID)
	if err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: update: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		if affectedErr != nil {
			return operations.DeletionRequest{}, fmt.Errorf("claim deletion request rows affected: %w", affectedErr)
		}
		return operations.DeletionRequest{}, operations.ErrDeletionLeaseLost
	}
	request, err := scanDeletion(tx.QueryRowContext(ctx, `SELECT `+deletionColumns+`
FROM deletion_requests WHERE request_id = $1`, requestID))
	if err != nil {
		return operations.DeletionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("claim deletion request: commit: %w", err)
	}
	return request, nil
}

func promoteDeletionInbox(ctx context.Context, tx *sql.Tx, now time.Time, limit int) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id::STRING, request_id, target_type, target_id, requested_at
FROM deletion_request_inbox
WHERE status = 'requested'
ORDER BY requested_at, id
LIMIT $1
FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return fmt.Errorf("promote deletion inbox: select: %w", err)
	}
	type inboxRequest struct {
		id, requestID, targetType, targetID string
		requestedAt                         time.Time
	}
	var requests []inboxRequest
	for rows.Next() {
		var request inboxRequest
		if err := rows.Scan(&request.id, &request.requestID, &request.targetType, &request.targetID, &request.requestedAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("promote deletion inbox: scan: %w", err)
		}
		requests = append(requests, request)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("promote deletion inbox: close: %w", err)
	}
	for _, request := range requests {
		var deletionID string
		err := tx.QueryRowContext(ctx, `
INSERT INTO deletion_requests (
  request_id, target_type, target_id, status, last_completed_stage,
  subject_ids, requested_at, updated_at
) VALUES ($1, $2, $3, 'requested', 'requested', ARRAY[]::STRING[], $4, $5)
ON CONFLICT DO NOTHING
RETURNING id::STRING`, request.requestID, request.targetType, request.targetID, request.requestedAt.UTC(), now.UTC()).Scan(&deletionID)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT id::STRING FROM deletion_requests
WHERE request_id = $1 AND target_type = $2 AND target_id = $3`, request.requestID, request.targetType, request.targetID).Scan(&deletionID)
		}
		if errors.Is(err, sql.ErrNoRows) {
			// Another request currently owns the incomplete-target uniqueness
			// constraint. It already covers this target; terminally acknowledge
			// this duplicate instead of starving every later inbox item.
			if _, updateErr := tx.ExecContext(ctx, `UPDATE deletion_request_inbox
SET status = 'promoted', promoted_at = $2
WHERE id = $1::UUID AND status = 'requested'`, request.id, now.UTC()); updateErr != nil {
				return fmt.Errorf("acknowledge duplicate deletion inbox %s: %w", request.requestID, updateErr)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("promote deletion inbox %s: %w", request.requestID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deletion_request_claims (deletion_request_id, available_at, updated_at)
VALUES ($1::UUID, $2, $2) ON CONFLICT (deletion_request_id) DO NOTHING`, deletionID, now.UTC()); err != nil {
			return fmt.Errorf("promote deletion inbox claim %s: %w", request.requestID, err)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM deletion_request_inbox
WHERE id = $1::UUID AND status = 'requested'`, request.id)
		if err != nil {
			return fmt.Errorf("promote deletion inbox cleanup %s: %w", request.requestID, err)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			if affectedErr != nil {
				return fmt.Errorf("promote deletion inbox cleanup %s rows affected: %w", request.requestID, affectedErr)
			}
			return operations.ErrDeletionLeaseLost
		}
	}
	return nil
}

func (store *Store) RevokeDeletionCredentials(ctx context.Context, request operations.DeletionRequest, now time.Time) (operations.DeletionRequest, error) {
	if store.pool != nil {
		var updated operations.DeletionRequest
		err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			if err := lockDeletionLeasePGX(txctx, tx, request); err != nil {
				return err
			}
			if err := rejectActiveDeletionHoldPGX(txctx, tx, request, now); err != nil {
				return err
			}
			var subjectIDs []string
			var primaryEmail string
			switch request.TargetType {
			case operations.DeletionTargetAccount:
				account, err := generated.GetAccountEmailForDeletion(txctx, tx, request.TargetID)
				if err != nil {
					return fmt.Errorf("lock account for deletion: %w", err)
				}
				if account == nil {
					return operations.ErrDeletionNotFound
				}
				primaryEmail = account.PrimaryEmail
				count, err := generated.MarkAccountDeletionPending(txctx, tx, request.TargetID, now.UTC())
				if err != nil {
					return fmt.Errorf("mark account deletion pending: %w", err)
				}
				if count != 1 {
					return operations.ErrDeletionNotFound
				}
				rows, err := generated.ListAccountSubjectsForDeletion(txctx, tx, request.TargetID)
				if err != nil {
					return fmt.Errorf("freeze account subjects: %w", err)
				}
				subjectIDs = make([]string, 0, len(rows))
				for _, row := range rows {
					subjectIDs = append(subjectIDs, row.Id)
				}
			case operations.DeletionTargetSubject:
				subject, err := generated.GetSubjectForDeletion(txctx, tx, request.TargetID)
				if err != nil {
					return fmt.Errorf("freeze subject: %w", err)
				}
				if subject == nil {
					return operations.ErrDeletionNotFound
				}
				subjectIDs = []string{subject.Id}
			default:
				return operations.ErrInvalidDeletionRequest
			}
			if err := generated.EnqueueDeletionTokenRevocations(txctx, tx, subjectIDs, now.UTC()); err != nil {
				return fmt.Errorf("enqueue deletion token revocations: %w", err)
			}
			if request.TargetType == operations.DeletionTargetAccount {
				if err := store.persistDeletionIdentityTombstonePGX(txctx, tx, request.RequestID, primaryEmail, now); err != nil {
					return err
				}
				if err := generated.DeleteAccountSessionsForDeletion(txctx, tx, request.TargetID); err != nil {
					return fmt.Errorf("revoke account sessions: %w", err)
				}
				if err := generated.DeleteAccountMagicLinksForDeletion(txctx, tx, request.TargetID, primaryEmail); err != nil {
					return fmt.Errorf("revoke account magic links: %w", err)
				}
				if err := generated.DeleteAccountAuthChallengesForDeletion(txctx, tx, request.TargetID, subjectIDs); err != nil {
					return fmt.Errorf("revoke account auth challenges: %w", err)
				}
			}
			count, err := generated.MarkDeletionCredentialsRevoked(txctx, tx, request.RequestID, subjectIDs, now.UTC())
			if err != nil {
				return fmt.Errorf("update deletion request: %w", err)
			}
			if count != 1 {
				return operations.ErrDeletionNotFound
			}
			updated, err = store.LoadDeletion(txctx, request.RequestID)
			return err
		})
		if err != nil {
			return request, err
		}
		return updated, nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return request, fmt.Errorf("revoke deletion credentials: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockDeletionLease(ctx, tx, request); err != nil {
		return request, err
	}
	if err := rejectActiveDeletionHold(ctx, tx, request, now); err != nil {
		return request, err
	}

	var subjectIDs []string
	var primaryEmail string
	switch request.TargetType {
	case operations.DeletionTargetAccount:
		if err := tx.QueryRowContext(ctx, `SELECT primary_email FROM users WHERE id = $1 FOR UPDATE`, request.TargetID).Scan(&primaryEmail); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return request, operations.ErrDeletionNotFound
			}
			return request, fmt.Errorf("lock account for deletion: %w", err)
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE users SET status = 'deletion_pending', updated_at = $2 WHERE id = $1`, request.TargetID, now.UTC())
		if updateErr != nil {
			return request, fmt.Errorf("mark account deletion pending: %w", updateErr)
		}
		if count, affectedErr := result.RowsAffected(); affectedErr != nil || count != 1 {
			if affectedErr != nil {
				return request, fmt.Errorf("mark account deletion pending: %w", affectedErr)
			}
			return request, operations.ErrDeletionNotFound
		}
		rows, queryErr := tx.QueryContext(ctx, `SELECT id FROM subjects WHERE owner_user_id = $1 ORDER BY id FOR UPDATE`, request.TargetID)
		if queryErr != nil {
			return request, fmt.Errorf("freeze account subjects: %w", queryErr)
		}
		subjectIDs, err = scanStringRows(rows)
		if err != nil {
			return request, fmt.Errorf("freeze account subjects: %w", err)
		}
	case operations.DeletionTargetSubject:
		var subjectID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM subjects WHERE id = $1 FOR UPDATE`, request.TargetID).Scan(&subjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return request, operations.ErrDeletionNotFound
			}
			return request, fmt.Errorf("freeze subject: %w", err)
		}
		subjectIDs = []string{subjectID}
	default:
		return request, operations.ErrInvalidDeletionRequest
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_token_revocation_jobs (
  connection_id, provider_id, token_ciphertext, token_key_id,
  status, attempts, available_at, created_at, updated_at
)
SELECT connection.id, COALESCE(connection.sync_cursor->>'provider_id', ''),
       connection.access_token_ciphertext, connection.access_token_key_id,
       'pending', 0, $2, $2, $2
FROM provider_connections AS connection
WHERE connection.subject_id = ANY($1::STRING[])
  AND connection.auth_method = 'oauth2'
  AND connection.access_token_ciphertext IS NOT NULL
	AND COALESCE(NULLIF(connection.sync_cursor->>'provider_id', ''), '') <> ''
ON CONFLICT (connection_id) DO NOTHING`, subjectIDs, now.UTC()); err != nil {
		return request, fmt.Errorf("enqueue deletion token revocations: %w", err)
	}
	if request.TargetType == operations.DeletionTargetAccount {
		if err := store.persistDeletionIdentityTombstone(ctx, tx, request.RequestID, primaryEmail, now); err != nil {
			return request, err
		}
		statements := []struct {
			name  string
			query string
			args  []any
		}{
			{"sessions", `DELETE FROM user_sessions WHERE user_id = $1`, []any{request.TargetID}},
			{"magic links", `DELETE FROM magic_link_tokens WHERE user_id = $1 OR email = $2`, []any{request.TargetID, primaryEmail}},
			{"auth challenges", `DELETE FROM auth_challenges WHERE user_id = $1 OR payload->>'SubjectID' = ANY($2::STRING[])`, []any{request.TargetID, subjectIDs}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return request, fmt.Errorf("revoke account %s: %w", statement.name, err)
			}
		}
	}
	updated, err := updateDeletionTx(ctx, tx, request.RequestID, operations.DeletionDeletingPrimary,
		operations.DeletionStageCredentialsRevoked, "", subjectIDs, now, nil, nil, "")
	if err != nil {
		return request, err
	}
	if err := tx.Commit(); err != nil {
		return request, fmt.Errorf("revoke deletion credentials: commit: %w", err)
	}
	return updated, nil
}

func lockDeletionLeasePGX(ctx context.Context, tx pgx.Tx, request operations.DeletionRequest) error {
	if request.ClaimToken == "" {
		row, err := generated.LockUnclaimedDeletionRequest(ctx, tx, request.RequestID)
		if err != nil {
			return fmt.Errorf("lock unclaimed deletion request: %w", err)
		}
		if row == nil {
			return operations.ErrDeletionNotFound
		}
		if !row.Unclaimed {
			return operations.ErrDeletionLeaseLost
		}
		return nil
	}
	row, err := generated.LockDeletionLease(ctx, tx, request.RequestID)
	if err != nil {
		return fmt.Errorf("lock deletion lease: %w", err)
	}
	if row == nil {
		return operations.ErrDeletionNotFound
	}
	if row.ClaimToken != request.ClaimToken || row.LeaseActive == nil || !*row.LeaseActive {
		return operations.ErrDeletionLeaseLost
	}
	return nil
}

func rejectActiveDeletionHoldPGX(ctx context.Context, tx pgx.Tx, request operations.DeletionRequest, asOf time.Time) error {
	row, err := generated.HasActiveDeletionHold(ctx, tx, string(request.TargetType), request.TargetID, asOf.UTC())
	if err != nil {
		return fmt.Errorf("check deletion legal hold: %w", err)
	}
	if row.Active {
		return operations.ErrLegalHoldActive
	}
	return nil
}

func (store *Store) persistDeletionIdentityTombstonePGX(ctx context.Context, tx pgx.Tx, requestID, primaryEmail string, now time.Time) error {
	expiresAt := now.UTC().Add(operations.BackupRetentionWindow)
	count, err := generated.ExtendDeletedIdentityHmacTombstone(ctx, tx, requestID, expiresAt)
	if err != nil {
		return fmt.Errorf("extend existing deleted identity HMAC tombstone: %w", err)
	}
	if count == 1 {
		return nil
	}
	if count != 0 {
		return fmt.Errorf("%w: multiple account identity tombstones", operations.ErrDeletionResiduals)
	}
	keyID, digest, err := store.deletedIdentityHMAC(primaryEmail)
	if err != nil {
		return fmt.Errorf("persist deleted identity HMAC tombstone: %w", err)
	}
	count, err = generated.InsertDeletedIdentityHmacTombstone(ctx, tx, requestID, keyID, digest, now.UTC(), expiresAt)
	if err != nil {
		return fmt.Errorf("persist deleted identity HMAC tombstone: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: identity is already tombstoned by another deletion request", operations.ErrDeletionResiduals)
	}
	return nil
}

func (store *Store) DeletePrimaryData(ctx context.Context, request operations.DeletionRequest, now time.Time) (operations.DeletionRequest, error) {
	if store.pool != nil {
		var updated operations.DeletionRequest
		err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
			if err := lockDeletionLeasePGX(txctx, tx, request); err != nil {
				return err
			}
			if err := rejectActiveDeletionHoldPGX(txctx, tx, request, now); err != nil {
				return err
			}
			var primaryEmail string
			if request.TargetType == operations.DeletionTargetAccount {
				account, err := generated.GetAccountEmailForDeletion(txctx, tx, request.TargetID)
				if err != nil {
					return fmt.Errorf("lock account identity before primary deletion: %w", err)
				}
				if account != nil {
					primaryEmail = account.PrimaryEmail
					if err := store.persistDeletionIdentityTombstonePGX(txctx, tx, request.RequestID, primaryEmail, now); err != nil {
						return err
					}
				}
			}
			if err := deleteSubjectPrimaryDataPGX(txctx, tx, request.SubjectIDs); err != nil {
				return err
			}
			if request.TargetType == operations.DeletionTargetAccount {
				if err := generated.DeleteAccountAuthChallengesForDeletion(txctx, tx, request.TargetID, request.SubjectIDs); err != nil {
					return fmt.Errorf("delete residual auth challenges: %w", err)
				}
				if err := generated.DeleteAccountMagicLinksForDeletion(txctx, tx, request.TargetID, primaryEmail); err != nil {
					return fmt.Errorf("delete residual magic links: %w", err)
				}
				if err := generated.DeleteAccountPasskeys(txctx, tx, request.TargetID); err != nil {
					return fmt.Errorf("delete passkeys: %w", err)
				}
				count, err := generated.DeleteAccountRow(txctx, tx, request.TargetID)
				if err != nil {
					return fmt.Errorf("delete account: %w", err)
				}
				if count > 1 {
					return fmt.Errorf("delete account affected %d rows", count)
				}
			}
			count, err := generated.MarkDeletionPrimaryDeleted(txctx, tx, request.RequestID, request.SubjectIDs, now.UTC())
			if err != nil {
				return fmt.Errorf("update deletion request: %w", err)
			}
			if count != 1 {
				return operations.ErrDeletionNotFound
			}
			updated, err = store.LoadDeletion(txctx, request.RequestID)
			return err
		})
		if err != nil {
			return request, err
		}
		return updated, nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return request, fmt.Errorf("delete primary data: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockDeletionLease(ctx, tx, request); err != nil {
		return request, err
	}
	if err := rejectActiveDeletionHold(ctx, tx, request, now); err != nil {
		return request, err
	}
	if request.TargetType == operations.DeletionTargetAccount {
		var primaryEmail string
		if err := tx.QueryRowContext(ctx, `SELECT primary_email FROM users WHERE id = $1 FOR UPDATE`, request.TargetID).Scan(&primaryEmail); err == nil {
			if err := store.persistDeletionIdentityTombstone(ctx, tx, request.RequestID, primaryEmail, now); err != nil {
				return request, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return request, fmt.Errorf("lock account identity before primary deletion: %w", err)
		}
	}
	if len(request.SubjectIDs) != 0 {
		queries := []struct{ name, sql string }{
			{"sync jobs", `DELETE FROM provider_sync_jobs WHERE subject_id = ANY($1::STRING[])`},
			{"activity facts", `DELETE FROM activity_facts WHERE subject_id = ANY($1::STRING[])`},
			{"timeline cache", `DELETE FROM timeline_cache WHERE subject_id = ANY($1::STRING[])`},
			{"activity refresh cache", `DELETE FROM activity_refresh_cache WHERE subject_id = ANY($1::STRING[])`},
			{"custom providers", `DELETE FROM custom_providers WHERE subject_id = ANY($1::STRING[])`},
			{"provider connections", `DELETE FROM provider_connections WHERE subject_id = ANY($1::STRING[])`},
			{"subject environments", `DELETE FROM environments WHERE owner_subject_id = ANY($1::STRING[])`},
			{"subjects", `DELETE FROM subjects WHERE id = ANY($1::STRING[])`},
		}
		for _, statement := range queries {
			if _, err := tx.ExecContext(ctx, statement.sql, request.SubjectIDs); err != nil {
				return request, fmt.Errorf("delete %s: %w", statement.name, err)
			}
		}
	}
	if request.TargetType == operations.DeletionTargetAccount {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_challenges WHERE user_id = $1 OR payload->>'SubjectID' = ANY($2::STRING[])`, request.TargetID, request.SubjectIDs); err != nil {
			return request, fmt.Errorf("delete residual auth challenges: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM magic_link_tokens WHERE user_id = $1 OR email = (SELECT primary_email FROM users WHERE id = $1)`, request.TargetID); err != nil {
			return request, fmt.Errorf("delete residual magic links: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_passkeys WHERE user_id = $1`, request.TargetID); err != nil {
			return request, fmt.Errorf("delete passkeys: %w", err)
		}
		if result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, request.TargetID); err != nil {
			return request, fmt.Errorf("delete account: %w", err)
		} else if count, countErr := result.RowsAffected(); countErr != nil || count > 1 {
			if countErr != nil {
				return request, fmt.Errorf("delete account rows affected: %w", countErr)
			}
			return request, fmt.Errorf("delete account affected %d rows", count)
		}
	}
	updated, err := updateDeletionTx(ctx, tx, request.RequestID, operations.DeletionVerifying,
		operations.DeletionStagePrimaryDeleted, "", request.SubjectIDs, now, nil, nil, "")
	if err != nil {
		return request, err
	}
	if err := tx.Commit(); err != nil {
		return request, fmt.Errorf("delete primary data: commit: %w", err)
	}
	return updated, nil
}

func deleteSubjectPrimaryDataPGX(ctx context.Context, tx pgx.Tx, subjectIDs []string) error {
	if len(subjectIDs) == 0 {
		return nil
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"sync jobs", func() error { return generated.DeleteSubjectSyncJobs(ctx, tx, subjectIDs) }},
		{"activity facts", func() error { return generated.DeleteSubjectActivityFacts(ctx, tx, subjectIDs) }},
		{"timeline cache", func() error { return generated.DeleteSubjectTimelineCache(ctx, tx, subjectIDs) }},
		{"activity refresh cache", func() error { return generated.DeleteSubjectActivityRefresh(ctx, tx, subjectIDs) }},
		{"custom providers", func() error { return generated.DeleteSubjectCustomProviders(ctx, tx, subjectIDs) }},
		{"provider connections", func() error { return generated.DeleteSubjectProviderConnections(ctx, tx, subjectIDs) }},
		{"subject environments", func() error { return generated.DeleteSubjectEnvironments(ctx, tx, subjectIDs) }},
		{"subjects", func() error { return generated.DeleteSubjectRows(ctx, tx, subjectIDs) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("delete %s: %w", step.name, err)
		}
	}
	return nil
}

func (store *Store) VerifyDeletion(ctx context.Context, request operations.DeletionRequest) (operations.DeletionResiduals, error) {
	if store.pool != nil {
		row, err := generated.CountDeletionResiduals(ctx, appdb.PGXExecutorFor(ctx, store.pool),
			accountTargetID(request), request.SubjectIDs)
		if err != nil {
			return operations.DeletionResiduals{}, fmt.Errorf("verify deletion: %w", err)
		}
		return operations.DeletionResiduals{
			Subjects: row.Subjects, Passkeys: row.Passkeys, Sessions: row.Sessions,
			MagicLinks: row.MagicLinks, AuthChallenges: row.AuthChallenges,
			ProviderConnections: row.ProviderConnections, PrivateConsents: row.PrivateConsents,
			CustomProviders: row.CustomProviders, ActivityFacts: row.ActivityFacts,
			TimelineCache: row.TimelineCache, ActivityRefresh: row.ActivityRefresh,
			ProviderSyncJobs: row.ProviderSyncJobs,
		}, nil
	}
	var residuals operations.DeletionResiduals
	err := store.db.QueryRowContext(ctx, deletionVerificationQuery,
		accountTargetID(request), request.SubjectIDs).Scan(
		&residuals.Subjects, &residuals.Passkeys, &residuals.Sessions, &residuals.MagicLinks, &residuals.AuthChallenges,
		&residuals.ProviderConnections, &residuals.PrivateConsents, &residuals.CustomProviders, &residuals.ActivityFacts,
		&residuals.TimelineCache, &residuals.ActivityRefresh, &residuals.ProviderSyncJobs,
	)
	if err != nil {
		return operations.DeletionResiduals{}, fmt.Errorf("verify deletion: %w", err)
	}
	return residuals, nil
}

func (store *Store) CompleteDeletionWithAudit(ctx context.Context, request operations.DeletionRequest, event operations.AuditEvent, completedAt, backupExpiryAt time.Time) (operations.DeletionRequest, error) {
	if err := operations.ValidateAuditEvent(event); err != nil || event.ID == "" {
		return request, operations.ErrInvalidDeletionRequest
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return request, fmt.Errorf("complete deletion with audit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockDeletionLease(ctx, tx, request); err != nil {
		return request, err
	}
	if err := store.writeAuditEvent(ctx, tx, event); err != nil {
		return request, err
	}
	if request.TargetType == operations.DeletionTargetAccount {
		result, err := tx.ExecContext(ctx, `
UPDATE deleted_identity_tombstones_v2
SET expires_at = GREATEST(expires_at, $2)
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)`, request.RequestID, backupExpiryAt.UTC())
		if err != nil {
			return request, fmt.Errorf("extend deleted identity HMAC tombstone: %w", err)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
			return request, fmt.Errorf("extend deleted identity HMAC tombstone rows affected: %w", affectedErr)
		} else if affected == 0 {
			// Requests that had already deleted their primary row before the v2
			// rollout cannot be HMAC-backfilled without retaining raw email. Let
			// those requests complete only when their legacy transition marker is
			// present; every new deletion writes v2 before deleting the user.
			legacy, legacyErr := tx.ExecContext(ctx, `
UPDATE deleted_identity_tombstones
SET expires_at = GREATEST(expires_at, $2)
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)`, request.RequestID, backupExpiryAt.UTC())
			if legacyErr != nil {
				return request, fmt.Errorf("extend legacy deleted identity tombstone: %w", legacyErr)
			}
			if legacyAffected, legacyAffectedErr := legacy.RowsAffected(); legacyAffectedErr != nil {
				return request, fmt.Errorf("extend legacy deleted identity tombstone rows affected: %w", legacyAffectedErr)
			} else if legacyAffected != 1 {
				return request, fmt.Errorf("%w: account identity tombstone is missing", operations.ErrDeletionResiduals)
			}
		} else if affected != 1 {
			return request, fmt.Errorf("%w: multiple account identity tombstones", operations.ErrDeletionResiduals)
		}
	}
	completed, err := updateDeletion(ctx, tx, request.RequestID, operations.DeletionCompleted, operations.DeletionStageCompleted,
		"", request.SubjectIDs, completedAt, &completedAt, &backupExpiryAt, event.ID)
	if err != nil {
		return request, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deletion_request_claims
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)`, request.RequestID); err != nil {
		return request, fmt.Errorf("complete deletion claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return request, fmt.Errorf("complete deletion with audit: commit: %w", err)
	}
	return completed, nil
}

func (store *Store) FailDeletion(ctx context.Context, request operations.DeletionRequest, code string, now time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fail deletion request: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockDeletionLease(ctx, tx, request); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE deletion_requests
SET status = 'failed', error_code = $2, updated_at = $3
WHERE request_id = $1 AND status <> 'completed'`, request.RequestID, code, now.UTC())
	if err != nil {
		return fmt.Errorf("fail deletion request: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("fail deletion request rows affected: %w", affectedErr)
	} else if affected != 1 {
		return operations.ErrDeletionLeaseLost
	}
	if request.ClaimToken != "" {
		result, err = tx.ExecContext(ctx, `
UPDATE deletion_request_claims SET
  available_at = $2 + INTERVAL '1 minute', lease_until = NULL, claim_token = NULL, updated_at = $2
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)
  AND claim_token::STRING = $3`, request.RequestID, now.UTC(), request.ClaimToken)
		if err != nil {
			return fmt.Errorf("release failed deletion claim: %w", err)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			if affectedErr != nil {
				return fmt.Errorf("release failed deletion claim rows affected: %w", affectedErr)
			}
			return operations.ErrDeletionLeaseLost
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fail deletion request: commit: %w", err)
	}
	return nil
}

func (store *Store) DeferDeletionForLegalHold(ctx context.Context, request operations.DeletionRequest, now time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("defer deletion for legal hold: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockDeletionLease(ctx, tx, request); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE deletion_requests
SET status = 'failed', error_code = 'legal_hold_active', updated_at = $2
WHERE request_id = $1 AND status <> 'completed'`,
		request.RequestID, now.UTC())
	if err != nil {
		return fmt.Errorf("defer deletion for legal hold: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		if affectedErr != nil {
			return fmt.Errorf("defer deletion for legal hold rows affected: %w", affectedErr)
		}
		return operations.ErrDeletionLeaseLost
	}
	if request.ClaimToken != "" {
		result, err = tx.ExecContext(ctx, `
UPDATE deletion_request_claims SET
    attempts = GREATEST(attempts - 1, 0),
    available_at = COALESCE((
      SELECT max(hold.expires_at) FROM legal_holds AS hold
      WHERE hold.expires_at > $4
        AND (
          (hold.target_type = $2 AND hold.target_id = $3)
          OR ($2 = 'account' AND hold.target_type = 'subject' AND EXISTS (
            SELECT 1 FROM subjects WHERE subjects.id = hold.target_id AND subjects.owner_user_id = $3
          ))
          OR ($2 = 'subject' AND hold.target_type = 'account' AND EXISTS (
            SELECT 1 FROM subjects WHERE subjects.id = $3 AND subjects.owner_user_id = hold.target_id
          ))
        )
    ), $4 + INTERVAL '1 minute'),
    lease_until = NULL, claim_token = NULL, updated_at = $4
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)
  AND claim_token::STRING = $5`,
			request.RequestID, request.TargetType, request.TargetID, now.UTC(), request.ClaimToken)
		if err != nil {
			return fmt.Errorf("defer deletion claim for legal hold: %w", err)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			if affectedErr != nil {
				return fmt.Errorf("defer deletion claim for legal hold rows affected: %w", affectedErr)
			}
			return operations.ErrDeletionLeaseLost
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("defer deletion for legal hold: commit: %w", err)
	}
	return nil
}

type deletionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func updateDeletion(ctx context.Context, db deletionQuerier, requestID string, status operations.DeletionStatus, stage operations.DeletionStage,
	errorCode string, subjectIDs []string, now time.Time, completedAt, backupExpiryAt *time.Time, auditEventID string,
) (operations.DeletionRequest, error) {
	return scanDeletion(db.QueryRowContext(ctx, `
UPDATE deletion_requests SET
  status = $2, last_completed_stage = $3, error_code = NULLIF($4, ''),
  subject_ids = $5, updated_at = $6, completed_at = $7,
  backup_expiry_at = $8, audit_event_id = NULLIF($9, '')::UUID
WHERE request_id = $1
RETURNING `+deletionColumns, requestID, status, stage, errorCode, subjectIDs, now.UTC(), completedAt, backupExpiryAt, auditEventID))
}

func updateDeletionTx(ctx context.Context, tx *sql.Tx, requestID string, status operations.DeletionStatus, stage operations.DeletionStage,
	errorCode string, subjectIDs []string, now time.Time, completedAt, backupExpiryAt *time.Time, auditEventID string,
) (operations.DeletionRequest, error) {
	return updateDeletion(ctx, tx, requestID, status, stage, errorCode, subjectIDs, now, completedAt, backupExpiryAt, auditEventID)
}

func rejectActiveDeletionHold(ctx context.Context, tx *sql.Tx, request operations.DeletionRequest, asOf time.Time) error {
	var active bool
	err := tx.QueryRowContext(ctx, deletionLegalHoldQuery, request.TargetType, request.TargetID, asOf.UTC()).Scan(&active)
	if err != nil {
		return fmt.Errorf("check deletion legal hold: %w", err)
	}
	if active {
		return operations.ErrLegalHoldActive
	}
	return nil
}

func lockDeletionLease(ctx context.Context, tx *sql.Tx, request operations.DeletionRequest) error {
	if request.ClaimToken == "" {
		var unclaimed bool
		if err := tx.QueryRowContext(ctx, `
SELECT NOT EXISTS (
  SELECT 1 FROM deletion_request_claims AS claim
  WHERE claim.deletion_request_id = deletion_requests.id AND claim.claim_token IS NOT NULL
)
FROM deletion_requests
WHERE request_id = $1
FOR UPDATE`, request.RequestID).Scan(&unclaimed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return operations.ErrDeletionNotFound
			}
			return fmt.Errorf("lock unclaimed deletion request: %w", err)
		}
		if !unclaimed {
			return operations.ErrDeletionLeaseLost
		}
		return nil
	}
	var claimToken string
	var leaseActive bool
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(claim.claim_token::STRING, ''), claim.lease_until > current_timestamp
FROM deletion_request_claims AS claim
JOIN deletion_requests AS request ON request.id = claim.deletion_request_id
WHERE request.request_id = $1
FOR UPDATE OF claim`, request.RequestID).Scan(&claimToken, &leaseActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operations.ErrDeletionNotFound
		}
		return fmt.Errorf("lock deletion lease: %w", err)
	}
	if claimToken != request.ClaimToken || (claimToken != "" && !leaseActive) {
		return operations.ErrDeletionLeaseLost
	}
	return nil
}

func (store *Store) persistDeletionIdentityTombstone(ctx context.Context, tx *sql.Tx, requestID, primaryEmail string, now time.Time) error {
	expiresAt := now.UTC().Add(operations.BackupRetentionWindow)
	existing, err := tx.ExecContext(ctx, `
UPDATE deleted_identity_tombstones_v2
SET expires_at = GREATEST(expires_at, $2)
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)`, requestID, expiresAt)
	if err != nil {
		return fmt.Errorf("extend existing deleted identity HMAC tombstone: %w", err)
	}
	if affected, affectedErr := existing.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("extend existing deleted identity HMAC tombstone rows affected: %w", affectedErr)
	} else if affected == 1 {
		return nil
	} else if affected != 0 {
		return fmt.Errorf("%w: multiple account identity tombstones", operations.ErrDeletionResiduals)
	}
	keyID, digest, err := store.deletedIdentityHMAC(primaryEmail)
	if err != nil {
		return fmt.Errorf("persist deleted identity HMAC tombstone: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO deleted_identity_tombstones_v2 (
  identity_key_id, identity_digest, deletion_request_id, created_at, expires_at
)
SELECT $2, $3, id, $4, $5
FROM deletion_requests
WHERE request_id = $1
ON CONFLICT (identity_key_id, identity_digest) DO NOTHING`,
		requestID, keyID, digest, now.UTC(), expiresAt)
	if err != nil {
		return fmt.Errorf("persist deleted identity HMAC tombstone: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("persist deleted identity HMAC tombstone rows affected: %w", affectedErr)
	} else if affected != 1 {
		return fmt.Errorf("%w: identity is already tombstoned by another deletion request", operations.ErrDeletionResiduals)
	}
	return nil
}

func deletionAdapterSQLForTest() string {
	return deletionLegalHoldQuery + `
UPDATE users SET status = 'deletion_pending';
INSERT INTO provider_token_revocation_jobs ON CONFLICT (connection_id) DO NOTHING;
SELECT connection.sync_cursor->>'provider_id' WHERE COALESCE(NULLIF(connection.sync_cursor->>'provider_id', ''), '') <> '';
INSERT INTO deleted_identity_tombstones_v2 SELECT identity_key_id, identity_digest;
UPDATE deleted_identity_tombstones_v2 SET expires_at = GREATEST(deleted_identity_tombstones_v2.expires_at, excluded.expires_at);
DELETE FROM provider_sync_jobs; DELETE FROM activity_facts; DELETE FROM timeline_cache;
DELETE FROM activity_refresh_cache; DELETE FROM custom_providers; DELETE FROM provider_connections;
DELETE FROM environments; DELETE FROM subjects; DELETE FROM users;`
}

func deletionVerificationQueryForTest() string { return deletionVerificationQuery }

func scanDeletion(row *sql.Row) (operations.DeletionRequest, error) {
	var request operations.DeletionRequest
	var completedAt, backupExpiryAt, leaseUntil sql.NullTime
	var subjectIDs []byte
	if err := row.Scan(
		&request.ID, &request.RequestID, &request.TargetType, &request.TargetID, &request.Status,
		&request.LastCompletedStage, &request.ErrorCode, &subjectIDs, &request.RequestedAt,
		&request.UpdatedAt, &completedAt, &backupExpiryAt, &request.AuditEventID,
		&request.Attempts, &request.AvailableAt, &leaseUntil, &request.ClaimToken,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operations.DeletionRequest{}, operations.ErrDeletionNotFound
		}
		return operations.DeletionRequest{}, fmt.Errorf("scan deletion request: %w", err)
	}
	if err := json.Unmarshal(subjectIDs, &request.SubjectIDs); err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("scan deletion request subject IDs: %w", err)
	}
	if completedAt.Valid {
		request.CompletedAt = &completedAt.Time
	}
	if backupExpiryAt.Valid {
		request.BackupExpiryAt = &backupExpiryAt.Time
	}
	if leaseUntil.Valid {
		request.LeaseUntil = &leaseUntil.Time
	}
	return request, nil
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func accountTargetID(request operations.DeletionRequest) string {
	if request.TargetType == operations.DeletionTargetAccount {
		return request.TargetID
	}
	return ""
}
