package cockroach

import (
	"context"
	"database/sql"
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

const syncJobColumns = `
id::STRING, provider_connection_id::STRING, date_from::STRING, date_to::STRING,
status, attempt, available_at, started_at, finished_at,
COALESCE(last_error, '{}'), created_at, updated_at`

const upsertSyncJobQuery = `
INSERT INTO provider_sync_jobs (
  id, provider_connection_id, subject_id, environment_id, status,
  date_from, date_to, attempt, max_attempts, available_at,
  started_at, finished_at, idempotency_key_hash, request_hash,
  idempotency_expires_at, last_error, created_at, updated_at
)
SELECT $1::UUID, id, subject_id, environment_id, $3,
  $4::DATE, $5::DATE, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
FROM provider_connections WHERE id = $2::UUID
ON CONFLICT (id) DO UPDATE SET
  status = excluded.status,
  date_from = excluded.date_from,
  date_to = excluded.date_to,
  attempt = excluded.attempt,
  max_attempts = excluded.max_attempts,
  available_at = excluded.available_at,
  started_at = excluded.started_at,
  finished_at = excluded.finished_at,
  idempotency_key_hash = excluded.idempotency_key_hash,
  request_hash = excluded.request_hash,
  idempotency_expires_at = excluded.idempotency_expires_at,
  last_error = excluded.last_error,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at`

const getSyncJobByIdempotencyQuery = `SELECT ` + syncJobColumns + ` FROM provider_sync_jobs
WHERE provider_connection_id = $1::UUID
  AND idempotency_key_hash = $2
  AND idempotency_expires_at > $3
ORDER BY created_at DESC, id DESC LIMIT 1`

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
	payload, err := encodeSyncJobPayload(job)
	if err != nil {
		return err
	}
	if s.pool != nil {
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
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	if job.Status == integrations.SyncJobPending && len(job.IdempotencyKeyHash) != 0 {
		if _, err := executor.ExecContext(ctx, `
DELETE FROM provider_sync_jobs
WHERE provider_connection_id = $1::UUID AND idempotency_key_hash = $2
  AND idempotency_expires_at <= $3`, job.ConnectionID, job.IdempotencyKeyHash, job.CreatedAt); err != nil {
			return fmt.Errorf("remove expired sync idempotency reservation: %w", err)
		}
	}
	result, err := executor.ExecContext(ctx, upsertSyncJobQuery,
		job.ID, job.ConnectionID, databaseSyncJobStatus(job.Status), nullableDate(job.From), nullableDate(job.To),
		job.Attempt, maxSyncAttempts(job.Attempt), syncJobAvailableAt(job),
		job.StartedAt, job.FinishedAt, nullableBytes(job.IdempotencyKeyHash), nullableBytes(job.RequestHash), job.IdempotencyExpires,
		string(payload), job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return persistenceError(err, integrations.ErrConflict)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save sync job: rows affected: %w", err)
	}
	if count == 0 {
		return notFound("connection", job.ConnectionID)
	}
	return nil
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
	if s.pool != nil {
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
	query := `SELECT ` + syncJobColumns + ` FROM provider_sync_jobs WHERE id = $1::UUID`
	job, err := scanSyncJob(s.db.QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return integrations.SyncJob{}, notFound("sync job", id)
	}
	if err != nil {
		return integrations.SyncJob{}, fmt.Errorf("get sync job: %w", err)
	}
	return job, nil
}

func (s *Store) ListSyncJobs(ctx context.Context, connectionID string) ([]integrations.SyncJob, error) {
	if s.pool != nil {
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
	query, args := buildListSyncJobsQuery(connectionID)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sync jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]integrations.SyncJob, 0)
	for rows.Next() {
		job, scanErr := scanSyncJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list sync jobs: %w", scanErr)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sync jobs: %w", err)
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
	if s.pool != nil {
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
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return integrations.SyncJob{}, false, err
	}
	job, err := scanSyncJob(executor.QueryRowContext(ctx, getSyncJobByIdempotencyQuery, connectionID, keyHash, now))
	if err == sql.ErrNoRows {
		return integrations.SyncJob{}, false, nil
	}
	if err != nil {
		return integrations.SyncJob{}, false, fmt.Errorf("get idempotent sync job: %w", err)
	}
	return job, true, nil
}

func (s *Store) ClaimSyncJob(ctx context.Context, id string, now, leaseUntil time.Time) (string, bool, error) {
	if id == "" || !leaseUntil.After(now) {
		return "", false, integrations.ErrInvalidSyncClaim
	}
	if s.pool != nil {
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
	var token string
	err := s.db.QueryRowContext(ctx, `
UPDATE provider_sync_jobs
SET status = 'running', started_at = $2, updated_at = $2,
    claim_token = gen_random_uuid(), lease_expires_at = $3
WHERE id = $1::UUID
  AND ((status = 'queued' AND available_at <= $2)
    OR (status = 'running' AND lease_expires_at <= $2))
RETURNING claim_token::STRING`, id, now, leaseUntil).Scan(&token)
	if err == nil {
		return token, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, fmt.Errorf("claim sync job: %w", err)
}

func (s *Store) ListClaimableSyncJobs(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, integrations.ErrInvalidSyncClaim
	}
	if s.pool != nil {
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
	rows, err := s.db.QueryContext(ctx, `
SELECT id::STRING
FROM provider_sync_jobs
WHERE (status = 'queued' AND available_at <= $1)
   OR (status = 'running' AND lease_expires_at <= $1)
ORDER BY available_at, id
LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list claimable sync jobs: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list claimable sync jobs: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list claimable sync jobs: %w", err)
	}
	return ids, nil
}

func (s *Store) CompleteClaimedSyncJob(ctx context.Context, job integrations.SyncJob, claimToken string) error {
	payload, err := encodeSyncJobPayload(job)
	if err != nil {
		return err
	}
	if s.pool != nil {
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
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
UPDATE provider_sync_jobs
SET status = $3, finished_at = $4, available_at = $5, last_error = $6,
    updated_at = $7, claim_token = NULL, lease_expires_at = NULL
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'running'`,
		job.ID, claimToken, databaseSyncJobStatus(job.Status), job.FinishedAt,
		syncJobAvailableAt(job), string(payload), job.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("complete claimed sync job: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete claimed sync job: rows affected: %w", err)
	}
	if count != 1 {
		return integrations.ErrSyncAlreadyRunning
	}
	return nil
}

func buildListSyncJobsQuery(connectionID string) (string, []any) {
	query := `SELECT ` + syncJobColumns + ` FROM provider_sync_jobs`
	args := []any{}
	if connectionID != "" {
		query += ` WHERE provider_connection_id = $1::UUID`
		args = append(args, connectionID)
	}
	query += ` ORDER BY created_at, id`
	return query, args
}

func scanSyncJob(row scanner) (integrations.SyncJob, error) {
	var job integrations.SyncJob
	var status string
	var from, to sql.NullString
	var availableAt time.Time
	var startedAt, finishedAt sql.NullTime
	var payloadBytes []byte
	if err := row.Scan(
		&job.ID, &job.ConnectionID, &from, &to, &status, &job.Attempt, &availableAt, &startedAt, &finishedAt,
		&payloadBytes, &job.CreatedAt, &job.UpdatedAt,
	); err != nil {
		return integrations.SyncJob{}, err
	}
	job.Status = applicationSyncJobStatus(status)
	if from.Valid {
		value := activity.Date(from.String)
		job.From = &value
	}
	if to.Valid {
		value := activity.Date(to.String)
		job.To = &value
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}
	var payload syncJobPayload
	if len(payloadBytes) > 0 {
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			return integrations.SyncJob{}, fmt.Errorf("decode sync job payload: %w", err)
		}
	}
	job.Trigger = payload.Trigger
	job.Force = payload.Force
	job.Timezone = payload.Timezone
	job.FailurePolicy = payload.FailurePolicy
	job.FactsWritten = payload.FactsWritten
	job.LastError = payload.LastError
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
	if availableAt.After(job.CreatedAt) {
		job.NextAttemptAt = &availableAt
	}
	return job, nil
}

func nullableDate(value *activity.Date) any {
	if value == nil {
		return nil
	}
	return string(*value)
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
