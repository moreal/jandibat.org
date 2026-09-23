package cockroach

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

type syncJobPayload struct {
	Trigger            integrations.SyncTrigger    `json:"trigger"`
	Force              bool                        `json:"force,omitempty"`
	Timezone           string                      `json:"timezone,omitempty"`
	FailurePolicy      activity.FetchFailurePolicy `json:"failure_policy,omitempty"`
	FactsWritten       int                         `json:"facts_written"`
	LastError          string                      `json:"last_error,omitempty"`
	IdempotencyKeyHash string                      `json:"idempotency_key_hash,omitempty"`
	RequestHash        string                      `json:"request_hash,omitempty"`
	IdempotencyExpires *time.Time                  `json:"idempotency_expires_at,omitempty"`
}

func (s *Store) SaveSyncJob(ctx context.Context, job integrations.SyncJob) error {
	if job.ID == "" {
		return integrations.ErrNotFound
	}
	if s.pool == nil {
		return ErrNilDB
	}
	payload, err := encodeSyncJobPayload(job)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(job.ID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	connectionID, err := uuid.Parse(job.ConnectionID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	if job.Attempt < 0 || job.Attempt > math.MaxInt32 || maxSyncAttempts(job.Attempt) > math.MaxInt32 {
		return integrations.ErrInvalidSyncClaim
	}
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if job.Status == integrations.SyncJobPending && len(job.IdempotencyKeyHash) != 0 {
			if err := generated.DeleteExpiredSyncReservation(txctx, tx, connectionID, job.IdempotencyKeyHash, job.CreatedAt); err != nil {
				return fmt.Errorf("remove expired sync idempotency reservation: %w", err)
			}
		}
		count, err := generated.UpsertSyncJob(txctx, tx, id, connectionID,
			databaseSyncJobStatus(job.Status), dateOrEmpty(job.From), dateOrEmpty(job.To),
			int32(job.Attempt), int32(maxSyncAttempts(job.Attempt)), syncJobAvailableAt(job),
			optionalTimeText(job.StartedAt), optionalTimeText(job.FinishedAt),
			job.IdempotencyKeyHash, job.RequestHash, optionalTimeText(job.IdempotencyExpires),
			string(payload), job.CreatedAt, job.UpdatedAt)
		if err != nil {
			return persistenceError(err, integrations.ErrConflict)
		}
		if count == 0 {
			return notFound("connection", job.ConnectionID)
		}
		return nil
	})
}

func encodeSyncJobPayload(job integrations.SyncJob) ([]byte, error) {
	payload, err := json.Marshal(syncJobPayload{
		Trigger: job.Trigger, Force: job.Force, Timezone: job.Timezone, FailurePolicy: job.FailurePolicy,
		FactsWritten: job.FactsWritten, LastError: job.LastError,
		IdempotencyKeyHash: hex.EncodeToString(job.IdempotencyKeyHash),
		RequestHash:        hex.EncodeToString(job.RequestHash), IdempotencyExpires: job.IdempotencyExpires,
	})
	if err != nil {
		return nil, fmt.Errorf("encode sync job payload: %w", err)
	}
	return payload, nil
}

func (s *Store) GetSyncJob(ctx context.Context, id string) (integrations.SyncJob, error) {
	if s.pool == nil {
		return integrations.SyncJob{}, ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.SyncJob{}, integrations.ErrInvalidIdentifier
	}
	row, err := generated.GetSyncJobById(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed)
	if err != nil {
		return integrations.SyncJob{}, fmt.Errorf("get sync job: %w", err)
	}
	if row == nil {
		return integrations.SyncJob{}, notFound("sync job", id)
	}
	return syncJobFromGenerated(*row)
}

func (s *Store) ListSyncJobs(ctx context.Context, connectionID string) ([]integrations.SyncJob, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	rows, err := generated.ListSyncJobs(ctx, appdb.PGXExecutorFor(ctx, s.pool), connectionID)
	if err != nil {
		return nil, fmt.Errorf("list sync jobs: %w", err)
	}
	jobs := make([]integrations.SyncJob, 0, len(rows))
	for _, row := range rows {
		job, err := syncJobFromGenerated(generated.GetSyncJobByIdRow(row))
		if err != nil {
			return nil, fmt.Errorf("list sync jobs: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) ListSyncJobsPage(ctx context.Context, connectionID string, after *integrations.SyncJobCursor, limit int) ([]integrations.SyncJob, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	if limit < 1 || limit > 101 {
		return nil, integrations.ErrInvalidSyncPageSize
	}
	parsedConnection, err := uuid.Parse(connectionID)
	if err != nil || parsedConnection.String() != connectionID {
		return nil, integrations.ErrInvalidIdentifier
	}
	createdAt, afterID := time.Unix(0, 0).UTC(), uuid.Nil
	if after != nil {
		afterID, err = uuid.Parse(after.ID)
		if after.CreatedAt.IsZero() || err != nil || afterID.String() != after.ID {
			return nil, integrations.ErrInvalidIdentifier
		}
		createdAt = after.CreatedAt
	}
	rows, err := generated.ListSyncJobsPage(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsedConnection, after != nil, createdAt, afterID, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list sync jobs page: %w", err)
	}
	jobs := make([]integrations.SyncJob, 0, len(rows))
	for _, row := range rows {
		job, err := syncJobFromGenerated(generated.GetSyncJobByIdRow(row))
		if err != nil {
			return nil, fmt.Errorf("list sync jobs page: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func syncJobFromGenerated(row generated.GetSyncJobByIdRow) (integrations.SyncJob, error) {
	job := integrations.SyncJob{
		ID: row.Id, ConnectionID: row.ConnectionId, Status: applicationSyncJobStatus(row.Status),
		Attempt: int(row.Attempt), StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.DateFrom != nil {
		value := activity.Date(*row.DateFrom)
		job.From = &value
	}
	if row.DateTo != nil {
		value := activity.Date(*row.DateTo)
		job.To = &value
	}
	var payload syncJobPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return integrations.SyncJob{}, fmt.Errorf("decode sync job payload: %w", err)
	}
	job.Trigger, job.Force, job.Timezone, job.FailurePolicy = payload.Trigger, payload.Force, payload.Timezone, payload.FailurePolicy
	job.FactsWritten, job.LastError = payload.FactsWritten, payload.LastError
	keyHash, err := hex.DecodeString(payload.IdempotencyKeyHash)
	if err != nil {
		return integrations.SyncJob{}, fmt.Errorf("decode sync job idempotency key hash: %w", err)
	}
	job.IdempotencyKeyHash = keyHash
	requestHash, err := hex.DecodeString(payload.RequestHash)
	if err != nil {
		return integrations.SyncJob{}, fmt.Errorf("decode sync job request hash: %w", err)
	}
	job.RequestHash = requestHash
	job.IdempotencyExpires = payload.IdempotencyExpires
	if row.AvailableAt.After(job.CreatedAt) {
		value := row.AvailableAt
		job.NextAttemptAt = &value
	}
	return job, nil
}

func dateOrEmpty(value *activity.Date) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func (s *Store) GetSyncJobByIdempotencyKey(ctx context.Context, connectionID string, keyHash []byte, now time.Time) (integrations.SyncJob, bool, error) {
	if s.pool == nil {
		return integrations.SyncJob{}, false, ErrNilDB
	}
	parsed, err := uuid.Parse(connectionID)
	if err != nil {
		return integrations.SyncJob{}, false, integrations.ErrInvalidIdentifier
	}
	row, err := generated.GetSyncJobByIdempotency(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed, keyHash, now)
	if err != nil {
		return integrations.SyncJob{}, false, fmt.Errorf("get idempotent sync job: %w", err)
	}
	if row == nil {
		return integrations.SyncJob{}, false, nil
	}
	job, err := syncJobFromGenerated(generated.GetSyncJobByIdRow(*row))
	return job, err == nil, err
}

func (s *Store) ClaimSyncJob(ctx context.Context, id string, now, leaseUntil time.Time) (string, bool, error) {
	if id == "" || !leaseUntil.After(now) {
		return "", false, integrations.ErrInvalidSyncClaim
	}
	if s.pool == nil {
		return "", false, ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", false, integrations.ErrInvalidIdentifier
	}
	row, err := generated.ClaimSyncJob(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed, now, leaseUntil)
	if err != nil {
		return "", false, fmt.Errorf("claim sync job: %w", err)
	}
	if row == nil {
		return "", false, nil
	}
	if row.ClaimToken == nil || *row.ClaimToken == "" {
		return "", false, integrations.ErrInvalidSyncClaim
	}
	return *row.ClaimToken, true, nil
}

func (s *Store) ListClaimableSyncJobs(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, integrations.ErrInvalidSyncClaim
	}
	if s.pool == nil {
		return nil, ErrNilDB
	}
	rows, err := generated.ListClaimableSyncJobs(ctx, appdb.PGXExecutorFor(ctx, s.pool), now, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list claimable sync jobs: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Id)
	}
	return ids, nil
}

func (s *Store) CompleteClaimedSyncJob(ctx context.Context, job integrations.SyncJob, claimToken string) error {
	if s.pool == nil {
		return ErrNilDB
	}
	payload, err := encodeSyncJobPayload(job)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(job.ID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	claim, err := uuid.Parse(claimToken)
	if err != nil {
		return integrations.ErrInvalidSyncClaim
	}
	count, err := generated.CompleteClaimedSyncJob(ctx, appdb.PGXExecutorFor(ctx, s.pool),
		id, claim, databaseSyncJobStatus(job.Status), optionalTimeText(job.FinishedAt),
		syncJobAvailableAt(job), string(payload), job.UpdatedAt)
	if err != nil {
		return fmt.Errorf("complete claimed sync job: %w", err)
	}
	if count != 1 {
		return integrations.ErrSyncAlreadyRunning
	}
	return nil
}

func maxSyncAttempts(attempt int) int {
	if attempt > 8 {
		return attempt
	}
	return 8
}

func syncJobAvailableAt(job integrations.SyncJob) time.Time {
	if job.NextAttemptAt != nil {
		return *job.NextAttemptAt
	}
	return job.CreatedAt
}

func databaseSyncJobStatus(status integrations.SyncJobStatus) string {
	if status == integrations.SyncJobPending {
		return "queued"
	}
	return string(status)
}

func applicationSyncJobStatus(status string) integrations.SyncJobStatus {
	if status == "queued" {
		return integrations.SyncJobPending
	}
	return integrations.SyncJobStatus(status)
}
