//go:build integration

package cockroach

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A worker crash after its final attempt must terminalize an expired claim
// before that outbox row can be selected for another delivery attempt.
func TestCockroachWorkerAuditClaimTerminalizesExpiredFinalAttempt(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	workerDSN := os.Getenv("JANDIBAT_TEST_WORKER_DATABASE_URL")
	if adminDSN == "" || workerDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_WORKER_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	worker, err := pgxpool.New(ctx, workerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Close)
	var role string
	if err := worker.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != "jandibat_worker" {
		t.Fatalf("worker role=%q err=%v", role, err)
	}
	outboxID, eventID, claimID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	before := now.Add(-time.Minute)
	_, err = admin.Exec(ctx, `INSERT INTO mutation_audit_outbox (
id,audit_event_id,request_id,occurred_at,actor_type,action,target_type,outcome,
metadata,status,attempts,available_at,lease_until,claim_token,created_at,updated_at
) VALUES ($1,$2,$3,$4,'system','maintenance.audit.test','database','failed',
'{}'::JSONB,'processing',5,$4,$4,$5,$4,$4)`, outboxID, eventID, "final-attempt-"+outboxID.String(), before, claimID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM mutation_audit_outbox WHERE id=$1`, outboxID)
	})
	store := &Store{pool: worker}
	claimed, err := store.ClaimMutationAudits(ctx, now, time.Minute, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range claimed {
		if item.ID == outboxID.String() {
			t.Fatal("exhausted outbox row was claimed again")
		}
	}
	var status, reason string
	var attempts int
	if err := admin.QueryRow(ctx, `SELECT status,attempts,terminal_reason FROM mutation_audit_outbox WHERE id=$1`, outboxID).Scan(&status, &attempts, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || attempts != 5 || reason != "attempts_exhausted" {
		t.Fatalf("terminal state=(%q,%d,%q), want dead/5/attempts_exhausted", status, attempts, reason)
	}
}
