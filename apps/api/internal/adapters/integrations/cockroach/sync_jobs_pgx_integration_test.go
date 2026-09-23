package cockroach_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestCockroachSyncJobsUsePGXWithoutLegacyHandle(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := integrationSuffix(t)
	userID := "it_job_user_" + suffix
	subjectID := "it_job_subject_" + suffix
	environmentID := "it_job_environment_" + suffix
	connectionID := "4d9ebd00-c2c0-43b7-a341-" + suffix
	jobID := "af12d033-5086-4923-9d6a-" + suffix
	replacementID := "bf12d033-5086-4923-9d6a-" + suffix
	conflictingID := "cf12d033-5086-4923-9d6a-" + suffix
	missingConnectionJobID := "df12d033-5086-4923-9d6a-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := admin.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = admin.ExecContext(cleanupCtx, `DELETE FROM provider_sync_jobs WHERE id=$1`, jobID)
		_, _ = admin.ExecContext(cleanupCtx, `DELETE FROM provider_sync_jobs WHERE id=$1`, replacementID)
		_, _ = admin.ExecContext(cleanupCtx, `DELETE FROM provider_connections WHERE id=$1`, connectionID)
		_, _ = admin.ExecContext(cleanupCtx, `DELETE FROM environments WHERE id=$1`, environmentID)
		_, _ = admin.ExecContext(cleanupCtx, `DELETE FROM subjects WHERE id=$1`, subjectID)
		_, _ = admin.ExecContext(cleanupCtx, `DELETE FROM users WHERE id=$1`, userID)
	})
	if _, err := admin.ExecContext(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public) VALUES ($1, $2, $3, 'UTC', true)`, subjectID, userID, "it-job-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $1, 'Job integration', 'subject', $2)`, environmentID, subjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status) VALUES ($1, $2, $3, 'token', 'active')`, connectionID, subjectID, environmentID); err != nil {
		t.Fatal(err)
	}
	makeStore := func(role string) (*integrationstore.Store, *pgxpool.Pool) {
		t.Helper()
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.User = url.User(role)
		roleDSN := parsed.String()
		pool, err := pgxpool.New(ctx, roleDSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		store, err := integrationstore.New(pool)
		if err != nil {
			t.Fatal(err)
		}
		return store, pool
	}
	apiStore, apiPool := makeStore("jandibat_api")
	workerStore, _ := makeStore("jandibat_worker")
	from, to := activity.Date("2026-08-01"), activity.Date("2026-08-12")
	idempotencyHash := sha256.Sum256([]byte("job-key-" + suffix))
	requestHash := sha256.Sum256([]byte("job-request-" + suffix))
	expires := now.Add(time.Hour)
	job := integrations.SyncJob{
		ID: jobID, ConnectionID: connectionID, Trigger: integrations.SyncTriggerScheduled,
		Status: integrations.SyncJobPending, From: &from, To: &to, Attempt: 1,
		Timezone: "Asia/Seoul", Force: true, FactsWritten: 7,
		IdempotencyKeyHash: idempotencyHash[:], RequestHash: requestHash[:],
		IdempotencyExpires: &expires, CreatedAt: now, UpdatedAt: now,
	}
	rolledBack := errors.New("rollback sync job")
	err = appdb.InTx(ctx, apiPool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := apiStore.SaveSyncJob(txctx, job); err != nil {
			return err
		}
		got, found, err := apiStore.GetSyncJobByIdempotencyKey(txctx, connectionID, idempotencyHash[:], now)
		if err != nil || !found || got.ID != jobID {
			t.Errorf("transactional idempotency lookup = (%+v, %t, %v)", got, found, err)
		}
		return rolledBack
	})
	if !errors.Is(err, rolledBack) {
		t.Fatalf("rollback transaction = %v", err)
	}
	if _, err := apiStore.GetSyncJob(ctx, jobID); !errors.Is(err, integrations.ErrNotFound) {
		t.Fatalf("job survived rollback: %v", err)
	}
	if err := apiStore.SaveSyncJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := apiStore.GetSyncJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != integrations.SyncJobPending || got.Trigger != integrations.SyncTriggerScheduled ||
		got.Attempt != 1 || got.From == nil || *got.From != from || got.To == nil || *got.To != to ||
		!got.Force || got.Timezone != "Asia/Seoul" || got.FactsWritten != 7 ||
		!reflect.DeepEqual(got.IdempotencyKeyHash, idempotencyHash[:]) ||
		!reflect.DeepEqual(got.RequestHash, requestHash[:]) ||
		got.IdempotencyExpires == nil || !got.IdempotencyExpires.Equal(expires) {
		t.Fatalf("pgx sync job mapping = %+v", got)
	}
	listed, err := apiStore.ListSyncJobs(ctx, connectionID)
	if err != nil || len(listed) != 1 || listed[0].ID != jobID {
		t.Fatalf("listed sync jobs = (%+v, %v)", listed, err)
	}
	idempotent, found, err := apiStore.GetSyncJobByIdempotencyKey(ctx, connectionID, idempotencyHash[:], now)
	if err != nil || !found || idempotent.ID != jobID {
		t.Fatalf("idempotent sync job = (%+v, %t, %v)", idempotent, found, err)
	}
	conflicting := job
	conflicting.ID = conflictingID
	conflicting.CreatedAt = now.Add(time.Second)
	conflicting.UpdatedAt = conflicting.CreatedAt
	if err := apiStore.SaveSyncJob(ctx, conflicting); !errors.Is(err, integrations.ErrConflict) {
		t.Fatalf("active same-key reservation = %v, want conflict", err)
	}
	missingConnection := job
	missingConnection.ID = missingConnectionJobID
	missingConnection.ConnectionID = "00000000-0000-4000-8000-000000000000"
	if err := apiStore.SaveSyncJob(ctx, missingConnection); !errors.Is(err, integrations.ErrNotFound) {
		t.Fatalf("missing connection sync job = %v, want not found", err)
	}
	claimable, err := workerStore.ListClaimableSyncJobs(ctx, now.Add(time.Second), 100)
	if err != nil || !containsSyncJobID(claimable, jobID) {
		t.Fatalf("claimable sync jobs = (%+v, %v)", claimable, err)
	}
	claim, acquired, err := workerStore.ClaimSyncJob(ctx, jobID, now.Add(time.Second), now.Add(time.Minute))
	if err != nil || !acquired || claim == "" {
		t.Fatalf("claim sync job = (%q, %t, %v)", claim, acquired, err)
	}
	if _, acquired, err := workerStore.ClaimSyncJob(ctx, jobID, now.Add(2*time.Second), now.Add(time.Minute)); err != nil || acquired {
		t.Fatalf("duplicate lease claim = (%t, %v)", acquired, err)
	}
	reclaimed, acquired, err := workerStore.ClaimSyncJob(ctx, jobID, now.Add(2*time.Minute), now.Add(3*time.Minute))
	if err != nil || !acquired || reclaimed == "" || reclaimed == claim {
		t.Fatalf("expired lease reclaim = (%q, %t, %v)", reclaimed, acquired, err)
	}
	completed := job
	completed.Status = integrations.SyncJobSucceeded
	finished := now.Add(3 * time.Second)
	completed.FinishedAt = &finished
	completed.UpdatedAt = finished
	if err := workerStore.CompleteClaimedSyncJob(ctx, completed, claim); !errors.Is(err, integrations.ErrSyncAlreadyRunning) {
		t.Fatalf("stale token completion = %v", err)
	}
	if err := workerStore.CompleteClaimedSyncJob(ctx, completed, reclaimed); err != nil {
		t.Fatalf("complete sync job = %v", err)
	}
	if err := workerStore.CompleteClaimedSyncJob(ctx, completed, reclaimed); !errors.Is(err, integrations.ErrSyncAlreadyRunning) {
		t.Fatalf("repeated completion = %v", err)
	}
	got, err = apiStore.GetSyncJob(ctx, jobID)
	if err != nil || got.Status != integrations.SyncJobSucceeded || got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Fatalf("completed sync job = (%+v, %v)", got, err)
	}
	replacement := job
	replacement.ID = replacementID
	replacement.CreatedAt = now.Add(2 * time.Hour)
	replacement.UpdatedAt = replacement.CreatedAt
	replacementExpires := now.Add(3 * time.Hour)
	replacement.IdempotencyExpires = &replacementExpires
	if err := apiStore.SaveSyncJob(ctx, replacement); err != nil {
		t.Fatalf("replace expired same-key reservation: %v", err)
	}
	if _, err := apiStore.GetSyncJob(ctx, jobID); !errors.Is(err, integrations.ErrNotFound) {
		t.Fatalf("expired same-key reservation survived replacement: %v", err)
	}
	idempotent, found, err = apiStore.GetSyncJobByIdempotencyKey(ctx, connectionID, idempotencyHash[:], replacement.CreatedAt)
	if err != nil || !found || idempotent.ID != replacementID {
		t.Fatalf("replacement same-key reservation = (%+v, %t, %v)", idempotent, found, err)
	}
}

func containsSyncJobID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
