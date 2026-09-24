package cockroach

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestCockroachVerifiedDeletionReplayCommitsAuditOnlyTransaction(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set isolated Cockroach admin and API-role test DSNs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	api, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	store, err := New(api)
	if err != nil {
		t.Fatal(err)
	}

	targetID := "audit-replay-" + uuid.NewString()
	firstID := "first-" + uuid.NewString()
	replayID := "replay-" + uuid.NewString()
	auditRequestID := "audit-" + uuid.NewString()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, `DELETE FROM mutation_audit_outbox WHERE request_id = $1`, auditRequestID)
		_, _ = admin.Exec(cleanup, `DELETE FROM deletion_request_inbox WHERE target_type = 'subject' AND target_id = $1`, targetID)
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := operations.DeletionRequest{RequestID: firstID, TargetType: operations.DeletionTargetSubject, TargetID: targetID,
		Status: operations.DeletionRequested, RequestedAt: now, UpdatedAt: now}
	if _, err := store.EnqueueDeletion(ctx, first); err != nil {
		t.Fatal(err)
	}

	replayCtx, replayTx, err := store.BeginMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer replayTx.Rollback()
	replay := first
	replay.RequestID = replayID
	got, err := store.EnqueueDeletion(replayCtx, replay)
	if err != nil || got.RequestID != firstID {
		t.Fatalf("replay = (%+v, %v), want durable first request ID %q", got, err, firstID)
	}
	if replayTx.Active() {
		t.Fatal("read-only replay began transaction before audit enqueue")
	}
	event := operations.AuditEvent{ID: uuid.NewString(), RequestID: auditRequestID, OccurredAt: now,
		Actor:  operations.AuditActor{Type: operations.AuditActorUser, ID: "actor-1"},
		Action: "post.graphql", Target: operations.AuditTarget{Type: "graphql"}, Outcome: operations.AuditSucceeded}
	if err := replayTx.Enqueue(replayCtx, event); err != nil {
		t.Fatalf("enqueue verified replay outcome: %v", err)
	}
	if !replayTx.Active() {
		t.Fatal("audit enqueue did not join an audit-only transaction")
	}
	if err := replayTx.Commit(); err != nil {
		t.Fatalf("commit replay outcome: %v", err)
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE request_id = $1 AND outcome = 'succeeded'`, auditRequestID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable replay audit count = %d, %v; want 1", count, err)
	}
}

func TestCockroachUnverifiedReadOnlyMutationStillRejectsSuccessAudit(t *testing.T) {
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if apiDSN == "" {
		t.Skip("set isolated Cockroach API-role test DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	api, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	store, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	readCtx, tx, err := store.BeginMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var one int
	if err := appdb.PGXExecutorFor(readCtx, api).QueryRow(readCtx, "SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
	event := operations.AuditEvent{ID: uuid.NewString(), RequestID: "unverified-" + uuid.NewString(), OccurredAt: time.Now().UTC(),
		Actor:  operations.AuditActor{Type: operations.AuditActorUser, ID: "actor-1"},
		Action: "post.graphql", Target: operations.AuditTarget{Type: "graphql"}, Outcome: operations.AuditSucceeded}
	if err := tx.Enqueue(readCtx, event); !errors.Is(err, operations.ErrInvalidAuditOutbox) {
		t.Fatalf("unverified read-only audit = %v, want ErrInvalidAuditOutbox", err)
	}
}
