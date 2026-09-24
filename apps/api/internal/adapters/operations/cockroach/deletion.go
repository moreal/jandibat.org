package cockroach

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func (store *Store) CreateOrLoadDeletion(ctx context.Context, request operations.DeletionRequest) (operations.DeletionRequest, error) {
	if err := operations.ValidateDeletionRequest(request); err != nil {
		return operations.DeletionRequest{}, err
	}
	if store.pool == nil {
		return operations.DeletionRequest{}, ErrNilDB
	}
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
	if store.pool == nil {
		return operations.DeletionRequest{}, ErrNilDB
	}
	executor := appdb.PGXExecutorFor(ctx, store.pool)
	row, err := generated.GetDeletionInboxForRequest(ctx, executor, request.RequestID, string(request.TargetType), request.TargetID)
	if err != nil {
		return operations.DeletionRequest{}, fmt.Errorf("load deletion inbox: %w", err)
	}
	preexisting := row != nil
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
	if preexisting {
		// Only a previously committed, same-target row may complete without a
		// state write. The request-scoped marker allows its outcome-only audit.
		appdb.MarkVerifiedDurableReplay(ctx, store.pool)
	}
	return inboxDeletionFromGenerated(*row), nil
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
	if store.pool == nil {
		return operations.DeletionRequest{}, ErrNilDB
	}
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

func (store *Store) ClaimDeletions(ctx context.Context, now, leaseUntil time.Time, limit int) ([]operations.DeletionRequest, error) {
	if now.IsZero() || !leaseUntil.After(now) || limit <= 0 {
		return nil, operations.ErrInvalidDeletionRequest
	}
	if store.pool == nil {
		return nil, ErrNilDB
	}
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
	if store.pool == nil {
		return operations.DeletionRequest{}, ErrNilDB
	}
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

func (store *Store) RevokeDeletionCredentials(ctx context.Context, request operations.DeletionRequest, now time.Time) (operations.DeletionRequest, error) {
	if store.pool == nil {
		return request, ErrNilDB
	}
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
	if store.pool == nil {
		return request, ErrNilDB
	}
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
	if store.pool == nil {
		return operations.DeletionResiduals{}, ErrNilDB
	}
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

func (store *Store) CompleteDeletionWithAudit(ctx context.Context, request operations.DeletionRequest, event operations.AuditEvent, completedAt, backupExpiryAt time.Time) (operations.DeletionRequest, error) {
	if err := operations.ValidateAuditEvent(event); err != nil || event.ID == "" {
		return request, operations.ErrInvalidDeletionRequest
	}
	if store.pool == nil {
		return request, ErrNilDB
	}
	var completed operations.DeletionRequest
	err := appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := lockDeletionLeasePGX(txctx, tx, request); err != nil {
			return err
		}
		if err := store.WriteAuditEvent(txctx, event); err != nil {
			return err
		}
		if request.TargetType == operations.DeletionTargetAccount {
			count, err := generated.ExtendDeletedIdentityHmacTombstone(txctx, tx, request.RequestID, backupExpiryAt.UTC())
			if err != nil {
				return fmt.Errorf("extend deleted identity HMAC tombstone: %w", err)
			}
			if count == 0 {
				count, err = generated.ExtendLegacyDeletedIdentityTombstone(txctx, tx, request.RequestID, backupExpiryAt.UTC())
				if err != nil {
					return fmt.Errorf("extend legacy deleted identity tombstone: %w", err)
				}
				if count != 1 {
					return fmt.Errorf("%w: account identity tombstone is missing", operations.ErrDeletionResiduals)
				}
			} else if count != 1 {
				return fmt.Errorf("%w: multiple account identity tombstones", operations.ErrDeletionResiduals)
			}
		}
		eventID, err := uuid.Parse(event.ID)
		if err != nil {
			return fmt.Errorf("complete deletion audit event ID: %w", err)
		}
		count, err := generated.MarkDeletionCompleted(txctx, tx, request.RequestID, request.SubjectIDs, completedAt.UTC(), backupExpiryAt.UTC(), eventID)
		if err != nil {
			return fmt.Errorf("complete deletion request: %w", err)
		}
		if count != 1 {
			return operations.ErrDeletionNotFound
		}
		if err := generated.DeleteCompletedDeletionClaim(txctx, tx, request.RequestID); err != nil {
			return fmt.Errorf("complete deletion claim: %w", err)
		}
		completed, err = store.LoadDeletion(txctx, request.RequestID)
		return err
	})
	if err != nil {
		return request, err
	}
	return completed, nil
}

func (store *Store) FailDeletion(ctx context.Context, request operations.DeletionRequest, code string, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := lockDeletionLeasePGX(txctx, tx, request); err != nil {
			return err
		}
		count, err := generated.MarkDeletionFailed(txctx, tx, request.RequestID, code, now.UTC())
		if err != nil {
			return fmt.Errorf("fail deletion request: %w", err)
		}
		if count != 1 {
			return operations.ErrDeletionLeaseLost
		}
		if request.ClaimToken != "" {
			count, err = generated.ReleaseFailedDeletionClaim(txctx, tx, request.RequestID, now.UTC(), request.ClaimToken)
			if err != nil {
				return fmt.Errorf("release failed deletion claim: %w", err)
			}
			if count != 1 {
				return operations.ErrDeletionLeaseLost
			}
		}
		return nil
	})
}

func (store *Store) DeferDeletionForLegalHold(ctx context.Context, request operations.DeletionRequest, now time.Time) error {
	if store.pool == nil {
		return ErrNilDB
	}
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := lockDeletionLeasePGX(txctx, tx, request); err != nil {
			return err
		}
		count, err := generated.MarkDeletionDeferredForLegalHold(txctx, tx, request.RequestID, now.UTC())
		if err != nil {
			return fmt.Errorf("defer deletion for legal hold: %w", err)
		}
		if count != 1 {
			return operations.ErrDeletionLeaseLost
		}
		if request.ClaimToken != "" {
			releaseAt, err := generated.GetDeletionHoldReleaseAt(txctx, tx,
				string(request.TargetType), request.TargetID, now.UTC())
			if err != nil {
				return fmt.Errorf("find deletion legal hold expiry: %w", err)
			}
			if releaseAt.AvailableAt == nil {
				return fmt.Errorf("find deletion legal hold expiry: missing available_at")
			}
			count, err = generated.ReleaseHeldDeletionClaim(txctx, tx, request.RequestID,
				*releaseAt.AvailableAt, now.UTC(), request.ClaimToken)
			if err != nil {
				return fmt.Errorf("defer deletion claim for legal hold: %w", err)
			}
			if count != 1 {
				return operations.ErrDeletionLeaseLost
			}
		}
		return nil
	})
}

func accountTargetID(request operations.DeletionRequest) string {
	if request.TargetType == operations.DeletionTargetAccount {
		return request.TargetID
	}
	return ""
}
