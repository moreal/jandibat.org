package cockroach

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	authstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestCockroachAPIRolePasskeyFailureConsumesCeremonyWithDeniedOutbox(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = api.Close() })
	var currentUser string
	if err := api.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	ceremonyID := "a17d0f6a-04ae-4e72-b2f3-" + suffix
	eventID := "e17d0f6a-04ae-4e72-b2f3-" + suffix
	requestID := "passkey-denied-" + suffix
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM mutation_audit_outbox WHERE request_id=$1`, requestID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM auth_challenges WHERE id=$1`, ceremonyID)
	})
	authRepository, err := authstore.New(api)
	if err != nil {
		t.Fatal(err)
	}
	ceremony := coreauth.PasskeyCeremony{
		ID: ceremonyID, Kind: coreauth.CeremonyAuthentication, Challenge: "challenge-" + suffix,
		VerifierSession: []byte(`{"session":"bound"}`), CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := authRepository.SaveCeremony(ctx, ceremony); err != nil {
		t.Fatalf("save ceremony: %v", err)
	}
	operationStore, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	txCtx, transaction, err := operationStore.BeginMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authRepository.ConsumeCeremony(txCtx, ceremonyID, coreauth.CeremonyAuthentication, now.Add(time.Second)); err != nil {
		t.Fatalf("consume ceremony: %v", err)
	}
	if !operations.MarkMutationFailureCommit(txCtx) || !transaction.Active() || !transaction.CommitFailure() {
		t.Fatal("intentional security failure did not mark the active mutation")
	}
	event := operations.AuditEvent{
		ID: eventID, OccurredAt: now.Add(time.Second), Actor: operations.AuditActor{Type: operations.AuditActorAnonymous},
		Action: "post.v1.auth.passkey.sign-in.finish", Target: operations.AuditTarget{Type: "authentication"},
		Outcome: operations.AuditDenied, RequestID: requestID, Metadata: map[string]any{"phase": "outcome", "status": 401},
	}
	if err := transaction.Enqueue(txCtx, event); err != nil {
		t.Fatalf("enqueue denied outcome: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit consumed ceremony and outcome: %v", err)
	}
	if _, err := authRepository.ConsumeCeremony(ctx, ceremonyID, coreauth.CeremonyAuthentication, now.Add(2*time.Second)); !errors.Is(err, coreauth.ErrConsumed) {
		t.Fatalf("passkey retry error=%v, want ErrConsumed", err)
	}
	var consumed time.Time
	var outcome, status string
	if err := admin.QueryRowContext(ctx, `SELECT consumed_at FROM auth_challenges WHERE id=$1`, ceremonyID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT outcome,status FROM mutation_audit_outbox WHERE request_id=$1`, requestID).Scan(&outcome, &status); err != nil {
		t.Fatal(err)
	}
	if consumed.IsZero() || outcome != "denied" || status != "pending" {
		t.Fatalf("consumed=%v outcome=%q status=%q", consumed, outcome, status)
	}
}

func TestCockroachAPIRoleStateAndMutationAuditCommitOrRollbackTogether(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, _ := sql.Open("pgx", adminDSN)
	api, _ := sql.Open("pgx", apiDSN)
	t.Cleanup(func() { _ = admin.Close(); _ = api.Close() })
	var currentUser string
	if err := api.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")
	userID := "atomic-audit-user-" + suffix
	email := userID + "@example.invalid"
	requestID := "atomic-audit-request-" + suffix
	eventID := "d17d0f6a-04ae-4e72-b2f3-" + suffix[:12]
	if _, err := admin.ExecContext(ctx, `INSERT INTO users(id,primary_email,status,created_at,updated_at) VALUES($1,$2,'active',$3,$3)`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO user_settings(user_id,locale,timezone,theme,updated_at) VALUES($1,'en-US','UTC','system',$2)`, userID, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM mutation_audit_outbox WHERE request_id=$1`, requestID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})
	pool, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	operationStore, err := NewWithPGXPool(api, pool)
	if err != nil {
		t.Fatal(err)
	}
	subjectStore, _ := subjectstore.New(pool)
	txCtx, transaction, err := operationStore.BeginMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings := subjects.UserSettings{Locale: "ko-KR", Timezone: "Asia/Seoul", Theme: subjects.ThemeDark, UpdatedAt: now.Add(time.Second)}
	if err := subjectStore.SaveUserSettings(txCtx, userID, settings); err != nil {
		t.Fatal(err)
	}
	event := operations.AuditEvent{ID: eventID, OccurredAt: now.Add(time.Second), Actor: operations.AuditActor{Type: operations.AuditActorUser, ID: userID}, Action: "patch.v1.me.settings", Target: operations.AuditTarget{Type: "subject", ID: userID}, Outcome: operations.AuditSucceeded, RequestID: requestID, Metadata: map[string]any{"phase": "outcome"}}
	if err := transaction.Enqueue(txCtx, event); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	var locale string
	var outboxCount int
	if err := admin.QueryRowContext(ctx, `SELECT locale FROM user_settings WHERE user_id=$1`, userID).Scan(&locale); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE request_id=$1`, requestID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if locale != "ko-KR" || outboxCount != 1 {
		t.Fatalf("committed locale=%q outbox=%d", locale, outboxCount)
	}

	txCtx, transaction, err = operationStore.BeginMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Locale = "ja-JP"
	settings.UpdatedAt = now.Add(2 * time.Second)
	if err := subjectStore.SaveUserSettings(txCtx, userID, settings); err != nil {
		t.Fatal(err)
	}
	event.RequestID = "" // force outbox validation failure
	if err := transaction.Enqueue(txCtx, event); err == nil {
		t.Fatal("invalid audit outcome unexpectedly enqueued")
	}
	_ = transaction.Rollback()
	if err := admin.QueryRowContext(ctx, `SELECT locale FROM user_settings WHERE user_id=$1`, userID).Scan(&locale); err != nil || locale != "ko-KR" {
		t.Fatalf("state escaped audit rollback: locale=%q err=%v", locale, err)
	}
}

func TestCockroachMutationAuditOutboxWorkerRoleDeliveryAndFence(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	workerDSN := os.Getenv("JANDIBAT_TEST_WORKER_DATABASE_URL")
	if adminDSN == "" || workerDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_WORKER_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := sql.Open("pgx", workerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = worker.Close() })
	var currentUser string
	if err := worker.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_worker" {
		t.Fatalf("worker DSN current_user=%q err=%v", currentUser, err)
	}
	pool, err := pgxpool.New(ctx, workerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := NewWithPGXPool(worker, pool)
	if err != nil {
		t.Fatal(err)
	}
	wallNow := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(wallNow.Format("150405.000000000"), ".", "")
	// Keep this fixture strictly ahead of any ordinary pending work in the
	// shared integration database so a bounded limit=1 claim cannot select an
	// unrelated row and make the role/fence assertion false-green or flaky.
	now := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	outboxID := "b17d0f6a-04ae-4e72-b2f3-" + suffix[:12]
	eventID := "c17d0f6a-04ae-4e72-b2f3-" + suffix[:12]
	requestID := "mutation-audit-" + suffix
	_, err = admin.ExecContext(ctx, `INSERT INTO mutation_audit_outbox (
id,audit_event_id,request_id,occurred_at,actor_type,actor_id,action,target_type,target_id,outcome,metadata,status,attempts,available_at,created_at,updated_at
) VALUES ($1,$2,$3,$4,'user','user-1','patch.v1.me.settings','subject','subject-1','succeeded','{"phase":"outcome"}','pending',0,$4,$4,$4)`, outboxID, eventID, requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM mutation_audit_outbox WHERE id=$1`, outboxID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM audit_events WHERE id=$1`, eventID)
	})
	rollbackClaim := errors.New("rollback worker audit claim")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		jobs, err := store.ClaimMutationAudits(txctx, now.Add(time.Second), time.Minute, 1, 5)
		if err != nil || len(jobs) != 1 || jobs[0].ID != outboxID {
			return fmt.Errorf("transactional audit claim=%+v err=%v", jobs, err)
		}
		return rollbackClaim
	})
	if !errors.Is(err, rollbackClaim) {
		t.Fatalf("rollback audit claim = %v", err)
	}
	var pendingStatus string
	var pendingAttempts int
	if err := admin.QueryRowContext(ctx, `SELECT status,attempts FROM mutation_audit_outbox WHERE id=$1`, outboxID).Scan(&pendingStatus, &pendingAttempts); err != nil || pendingStatus != "pending" || pendingAttempts != 0 {
		t.Fatalf("audit claim escaped rollback = (%q, %d, %v)", pendingStatus, pendingAttempts, err)
	}
	claimed, err := store.ClaimMutationAudits(ctx, now.Add(time.Second), time.Minute, 1, 5)
	if err != nil || len(claimed) != 1 || claimed[0].ID != outboxID {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if err := store.DeliverMutationAudit(ctx, claimed[0].ID, "00000000-0000-4000-8000-000000000000", now.Add(2*time.Second)); !errors.Is(err, operations.ErrInvalidAuditOutbox) {
		t.Fatalf("stale claim error=%v", err)
	}
	rollbackDelivery := errors.New("rollback worker audit delivery")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := store.DeliverMutationAudit(txctx, claimed[0].ID, claimed[0].ClaimToken, now.Add(2*time.Second)); err != nil {
			return err
		}
		return rollbackDelivery
	})
	if !errors.Is(err, rollbackDelivery) {
		t.Fatalf("rollback audit delivery = %v", err)
	}
	var processingStatus string
	var preDeliveryAuditCount int
	if err := admin.QueryRowContext(ctx, `SELECT status FROM mutation_audit_outbox WHERE id=$1`, outboxID).Scan(&processingStatus); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, eventID).Scan(&preDeliveryAuditCount); err != nil {
		t.Fatal(err)
	}
	if processingStatus != "processing" || preDeliveryAuditCount != 0 {
		t.Fatalf("audit delivery escaped rollback: status=%s count=%d", processingStatus, preDeliveryAuditCount)
	}
	if err := store.DeliverMutationAudit(ctx, claimed[0].ID, claimed[0].ClaimToken, now.Add(2*time.Second)); err != nil {
		t.Fatalf("worker-role deliver: %v", err)
	}
	var outboxStatus string
	var auditCount int
	if err := admin.QueryRowContext(ctx, `SELECT status FROM mutation_audit_outbox WHERE id=$1`, outboxID).Scan(&outboxStatus); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, eventID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if outboxStatus != "delivered" || auditCount != 1 {
		t.Fatalf("outbox=%s audit count=%d", outboxStatus, auditCount)
	}
	if _, err := worker.ExecContext(ctx, `UPDATE audit_events SET action='tamper' WHERE id=$1`, eventID); err == nil {
		t.Fatal("worker unexpectedly updated append-only audit sink")
	}
}

func TestCockroachMutationAuditOutboxLeaseReclaimFencesPreviousWorker(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	workerDSN := os.Getenv("JANDIBAT_TEST_WORKER_DATABASE_URL")
	if adminDSN == "" || workerDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_WORKER_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := sql.Open("pgx", workerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = worker.Close() })
	var currentUser string
	if err := worker.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_worker" {
		t.Fatalf("worker DSN current_user=%q err=%v", currentUser, err)
	}
	pool, err := pgxpool.New(ctx, workerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := NewWithPGXPool(worker, pool)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")[:12]
	outboxID := "d27d0f6a-04ae-4e72-b2f3-" + suffix
	eventID := "e27d0f6a-04ae-4e72-b2f3-" + suffix
	requestID := "mutation-audit-reclaim-" + suffix
	base := time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)
	_, err = admin.ExecContext(ctx, `INSERT INTO mutation_audit_outbox (
id,audit_event_id,request_id,occurred_at,actor_type,actor_id,action,target_type,target_id,outcome,metadata,status,attempts,available_at,created_at,updated_at
) VALUES ($1,$2,$3,$4,'user','user-reclaim','patch.v1.me.settings','subject','subject-reclaim','succeeded','{"phase":"outcome"}','pending',0,$4,$4,$4)`, outboxID, eventID, requestID, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM mutation_audit_outbox WHERE id=$1`, outboxID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM audit_events WHERE id=$1`, eventID)
	})

	first, err := store.ClaimMutationAudits(ctx, base.Add(time.Second), time.Second, 1, 5)
	if err != nil || len(first) != 1 || first[0].ID != outboxID {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	second, err := store.ClaimMutationAudits(ctx, base.Add(3*time.Second), time.Minute, 1, 5)
	if err != nil || len(second) != 1 || second[0].ID != outboxID || second[0].ClaimToken == first[0].ClaimToken {
		t.Fatalf("reclaim=%#v first=%#v err=%v", second, first, err)
	}
	if err := store.DeliverMutationAudit(ctx, outboxID, first[0].ClaimToken, base.Add(4*time.Second)); !errors.Is(err, operations.ErrInvalidAuditOutbox) {
		t.Fatalf("stale deliver error=%v", err)
	}
	if _, err := store.RetryMutationAudit(ctx, outboxID, first[0].ClaimToken, base.Add(4*time.Second), base.Add(5*time.Second), 5, "delivery_failed"); !errors.Is(err, operations.ErrInvalidAuditOutbox) {
		t.Fatalf("stale retry error=%v", err)
	}
	rollbackRetry := errors.New("rollback worker audit retry")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		status, err := store.RetryMutationAudit(txctx, outboxID, second[0].ClaimToken, base.Add(4*time.Second), base.Add(5*time.Second), 5, "delivery_failed")
		if err != nil || status != "pending" {
			return fmt.Errorf("transactional audit retry=%q err=%v", status, err)
		}
		return rollbackRetry
	})
	if !errors.Is(err, rollbackRetry) {
		t.Fatalf("rollback audit retry = %v", err)
	}
	var retryStatus string
	if err := admin.QueryRowContext(ctx, `SELECT status FROM mutation_audit_outbox WHERE id=$1`, outboxID).Scan(&retryStatus); err != nil || retryStatus != "processing" {
		t.Fatalf("audit retry escaped rollback: status=%q err=%v", retryStatus, err)
	}
	if err := store.DeliverMutationAudit(ctx, outboxID, second[0].ClaimToken, base.Add(4*time.Second)); err != nil {
		t.Fatalf("current deliver: %v", err)
	}
	var status string
	var attempts, auditCount int
	if err := admin.QueryRowContext(ctx, `SELECT status,attempts FROM mutation_audit_outbox WHERE id=$1`, outboxID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, eventID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 2 || auditCount != 1 {
		t.Fatalf("status=%s attempts=%d audit_count=%d", status, attempts, auditCount)
	}
}

func TestCockroachMaintenanceCheckpointJoinsPGXTransaction(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if adminDSN == "" || maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := sql.Open("pgx", maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _ = maintenance.Close(); _ = admin.Close() })
	store, err := NewWithPGXPool(maintenance, pool)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := operations.MaintenanceCheckpoint{
		Operation: operations.MaintenanceRetention,
		Scope:     "scythe-checkpoint-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", ""),
		Payload:   []byte(`{"cursor":"one"}`),
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM maintenance_checkpoints WHERE operation=$1 AND scope=$2`, checkpoint.Operation, checkpoint.Scope)
	})
	rollback := errors.New("rollback maintenance checkpoint")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := store.SaveCheckpoint(txctx, checkpoint); err != nil {
			return err
		}
		loaded, found, err := store.LoadCheckpoint(txctx, checkpoint.Operation, checkpoint.Scope)
		var payload map[string]string
		if err == nil {
			err = json.Unmarshal(loaded.Payload, &payload)
		}
		if err != nil || !found || loaded.Operation != checkpoint.Operation || loaded.Scope != checkpoint.Scope || payload["cursor"] != "one" {
			return fmt.Errorf("transactional checkpoint=%+v found=%t err=%v", loaded, found, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("checkpoint rollback = %v", err)
	}
	var count int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_checkpoints WHERE operation=$1 AND scope=$2`, checkpoint.Operation, checkpoint.Scope).Scan(&count); err != nil || count != 0 {
		t.Fatalf("checkpoint escaped rollback: count=%d err=%v", count, err)
	}
	if err := store.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.LoadCheckpoint(ctx, checkpoint.Operation, checkpoint.Scope)
	var payload map[string]string
	if err == nil {
		err = json.Unmarshal(loaded.Payload, &payload)
	}
	if err != nil || !found || loaded.Operation != checkpoint.Operation || loaded.Scope != checkpoint.Scope || payload["cursor"] != "one" {
		t.Fatalf("committed checkpoint=%+v found=%t err=%v", loaded, found, err)
	}
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := store.DeleteCheckpoint(txctx, checkpoint.Operation, checkpoint.Scope); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("checkpoint delete rollback = %v", err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM maintenance_checkpoints WHERE operation=$1 AND scope=$2`, checkpoint.Operation, checkpoint.Scope).Scan(&count); err != nil || count != 1 {
		t.Fatalf("checkpoint delete escaped rollback: count=%d err=%v", count, err)
	}
	if err := store.DeleteCheckpoint(ctx, checkpoint.Operation, checkpoint.Scope); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadCheckpoint(ctx, checkpoint.Operation, checkpoint.Scope); err != nil || found {
		t.Fatalf("checkpoint after delete: found=%t err=%v", found, err)
	}
}

func TestCockroachMaintenanceAuditSinkJoinsPGXTransaction(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if adminDSN == "" || maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := sql.Open("pgx", maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _ = maintenance.Close(); _ = admin.Close() })
	store, err := NewWithPGXPool(maintenance, pool)
	if err != nil {
		t.Fatal(err)
	}
	unique := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")[:12]
	event := operations.AuditEvent{
		ID:         "a37d0f6a-04ae-4e72-b2f3-" + unique,
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
		Actor:      operations.AuditActor{Type: operations.AuditActorUser, ID: "maintenance-user"},
		Action:     "maintenance.audit.test", Target: operations.AuditTarget{Type: "database", ID: "isolated"},
		Outcome: operations.AuditSucceeded, RequestID: "maintenance-audit-" + unique,
		SourceIP: "127.0.0.1", Metadata: map[string]any{"api_key": "sensitive-test-value"},
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM audit_events WHERE id=$1`, event.ID)
	})
	rollback := errors.New("rollback maintenance audit")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := store.WriteAuditEvent(txctx, event); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("audit rollback = %v", err)
	}
	var count int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, event.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit escaped rollback: count=%d err=%v", count, err)
	}
	if err := store.WriteAuditEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	var metadata []byte
	if err := admin.QueryRowContext(ctx, `SELECT metadata FROM audit_events WHERE id=$1`, event.ID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["api_key"] != operations.RedactedValue || decoded["source_ip"] != "127.0.0.1" {
		t.Fatalf("audit metadata redaction failed: keys=%d", len(decoded))
	}
}

func TestCockroachOperationsSchemaReadiness(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	configureTestDeletedIdentityHMAC(t, store)
	if err := store.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCockroachDeletionRevokesEmailOnlyMagicLinksAndPreservesValidOAuthRevocation(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	configureTestDeletedIdentityHMAC(t, store)
	suffix := time.Now().UTC().Format("150405.000000000")
	suffix = strings.ReplaceAll(suffix, ".", "")
	userID, subjectID := "ops_user_"+suffix, "ops_subject_"+suffix
	environmentID, malformedEnvironmentID, handle := "ops:env:"+suffix, "ops:malformed:"+suffix, "ops-"+suffix
	connectionID := "018f0000-0000-7000-8000-" + suffix[len(suffix)-12:]
	malformedConnectionID := "038f0000-0000-7000-8000-" + suffix[len(suffix)-12:]
	requestID := "ops-delete-" + suffix
	email := handle + "@example.invalid"
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, []any{userID, email}},
		{`INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $3, 'UTC')`, []any{subjectID, userID, handle}},
		{`INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $1, $1, 'subject', $2)`, []any{environmentID, subjectID}},
		{`INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $1, $1, 'subject', $2)`, []any{malformedEnvironmentID, subjectID}},
		{`INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status, access_token_ciphertext, access_token_key_id, sync_cursor)
VALUES ($1::UUID, $2, $3, 'oauth2', 'active', $4, 'old-key', jsonb_build_object('provider_id','github'))`, []any{connectionID, subjectID, environmentID, []byte("opaque-token")}},
		{`INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status, access_token_ciphertext, access_token_key_id, sync_cursor)
VALUES ($1::UUID, $2, $3, 'oauth2', 'active', $4, 'old-key', '{}'::JSONB)`, []any{malformedConnectionID, subjectID, malformedEnvironmentID, []byte("malformed-token")}},
		{`INSERT INTO magic_link_tokens (email, token_hash, purpose, expires_at) VALUES ($1, $2, 'signin', $3)`, []any{email, []byte("email-only-" + suffix), now.Add(time.Hour)}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanup, `DELETE FROM provider_token_revocation_jobs WHERE connection_id = $1::UUID OR provider_id = 'github' AND token_ciphertext = $2`, connectionID, []byte("opaque-token"))
		_, _ = db.ExecContext(cleanup, `DELETE FROM deleted_identity_tombstones_v2 WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM deleted_identity_tombstones WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM audit_events WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM deletion_requests WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	request, err := store.CreateOrLoadDeletion(ctx, operations.DeletionRequest{
		RequestID: requestID, TargetType: operations.DeletionTargetAccount, TargetID: userID,
		Status: operations.DeletionRequested, LastCompletedStage: operations.DeletionStageRequested,
		RequestedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateOrLoadDeletion(ctx, operations.DeletionRequest{
		RequestID: requestID, TargetType: operations.DeletionTargetAccount, TargetID: "different-target",
		Status: operations.DeletionRequested, LastCompletedStage: operations.DeletionStageRequested,
		RequestedAt: now, UpdatedAt: now,
	}); !errors.Is(err, operations.ErrInvalidDeletionRequest) {
		t.Fatalf("request ID target mismatch error = %v", err)
	}
	request, err = store.RevokeDeletionCredentials(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	var providerID string
	var token []byte
	if err := db.QueryRowContext(ctx, `SELECT provider_id, token_ciphertext FROM provider_token_revocation_jobs WHERE connection_id = $1::UUID`, connectionID).Scan(&providerID, &token); err != nil {
		t.Fatal(err)
	}
	if providerID != "github" || string(token) != "opaque-token" {
		t.Fatalf("revocation job provider=%q token=%q", providerID, token)
	}
	var malformedJobs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM provider_token_revocation_jobs WHERE connection_id = $1::UUID`, malformedConnectionID).Scan(&malformedJobs); err != nil || malformedJobs != 0 {
		t.Fatalf("malformed provider revocation jobs = %d, %v", malformedJobs, err)
	}
	var magicLinks int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM magic_link_tokens WHERE email = $1`, email).Scan(&magicLinks); err != nil || magicLinks != 0 {
		t.Fatalf("email-only magic links = %d, %v", magicLinks, err)
	}
	if len(request.SubjectIDs) != 1 || request.SubjectIDs[0] != subjectID {
		t.Fatalf("frozen subjects = %#v", request.SubjectIDs)
	}

	completedAt := now.Add(48 * time.Hour)
	audit, err := operations.NewAuditRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := operations.NewDeletionWorkflow(store, integrationClock{now: completedAt}, audit, bytes.Repeat([]byte{'p'}, 32))
	if err != nil {
		t.Fatal(err)
	}
	request, err = store.ClaimDeletion(ctx, requestID, completedAt, completedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	completed, err := workflow.RunClaimed(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != operations.DeletionCompleted || completed.BackupExpiryAt == nil || completed.AuditEventID == "" {
		t.Fatalf("completed request = %#v", completed)
	}
	var tombstoneKeyID string
	var tombstoneDigest []byte
	var tombstoneExpiry time.Time
	if err := db.QueryRowContext(ctx, `SELECT identity_key_id, identity_digest, expires_at FROM deleted_identity_tombstones_v2
WHERE deletion_request_id = $1::UUID`, completed.ID).Scan(&tombstoneKeyID, &tombstoneDigest, &tombstoneExpiry); err != nil {
		t.Fatal(err)
	}
	wantDigest := hmac.New(sha256.New, bytes.Repeat([]byte{'i'}, 32))
	_, _ = wantDigest.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	if tombstoneKeyID != "identity-test" || !hmac.Equal(tombstoneDigest, wantDigest.Sum(nil)) {
		t.Fatalf("identity tombstone key=%q digest=%x", tombstoneKeyID, tombstoneDigest)
	}
	var legacyTombstones int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deleted_identity_tombstones
WHERE deletion_request_id = $1::UUID`, completed.ID).Scan(&legacyTombstones); err != nil || legacyTombstones != 0 {
		t.Fatalf("new deletion wrote legacy SHA tombstones=%d error=%v", legacyTombstones, err)
	}
	if tombstoneExpiry.Before(*completed.BackupExpiryAt) {
		t.Fatalf("tombstone expires %s before backup expiry %s", tombstoneExpiry, *completed.BackupExpiryAt)
	}
	var residualTotal int64
	residuals, err := store.VerifyDeletion(ctx, completed)
	if err != nil {
		t.Fatal(err)
	}
	residualTotal = residuals.Total()
	if residualTotal != 0 {
		t.Fatalf("deletion residual total = %d (%#v)", residualTotal, residuals)
	}
}

func TestCockroachAccountDeletionFailsClosedWithoutIdentityHMAC(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	userID, requestID := "unkeyed_user_"+suffix, "unkeyed-delete-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM deletion_requests WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := store.CreateOrLoadDeletion(ctx, operations.DeletionRequest{
		RequestID: requestID, TargetType: operations.DeletionTargetAccount, TargetID: userID,
		Status: operations.DeletionRequested, LastCompletedStage: operations.DeletionStageRequested,
		RequestedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	request, err := store.ClaimDeletion(ctx, requestID, now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeDeletionCredentials(ctx, request, now); !errors.Is(err, ErrInvalidDeletedIdentityHMAC) {
		t.Fatalf("unconfigured account deletion error = %v", err)
	}
	var status string
	var tombstones int
	if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deleted_identity_tombstones_v2
WHERE deletion_request_id = $1::UUID`, request.ID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if status != "active" || tombstones != 0 {
		t.Fatalf("unkeyed deletion partially committed status=%q tombstones=%d", status, tombstones)
	}
}

func TestCockroachDeletionClaimLeasePreventsDuplicateAndReclaimsAfterCrash(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	now := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	requestID := "ops-claim-" + suffix
	request, err := store.CreateOrLoadDeletion(ctx, operations.DeletionRequest{
		RequestID: requestID, TargetType: operations.DeletionTargetSubject, TargetID: "missing-subject-" + suffix,
		Status: operations.DeletionRequested, LastCompletedStage: operations.DeletionStageRequested,
		RequestedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO deletion_request_claims (deletion_request_id, available_at, updated_at)
VALUES ($1::UUID, $2, $2) ON CONFLICT (deletion_request_id) DO UPDATE SET available_at = excluded.available_at`,
		request.ID, time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM deletion_requests WHERE request_id = $1`, requestID)
	})
	firstClaim, err := store.ClaimDeletion(ctx, requestID, now, now.Add(time.Minute))
	if err != nil || firstClaim.RequestID != request.RequestID || firstClaim.ClaimToken == "" || firstClaim.Attempts != 1 {
		t.Fatalf("first claim = %#v, %v", firstClaim, err)
	}
	first := []operations.DeletionRequest{firstClaim}
	stageTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockDeletionLease(ctx, stageTx, first[0]); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		requests []operations.DeletionRequest
		err      error
	}
	reclaimResult := make(chan claimResult, 1)
	go func() {
		request, claimErr := store.ClaimDeletion(ctx, requestID, now.Add(time.Minute), now.Add(2*time.Minute))
		requests := []operations.DeletionRequest(nil)
		if claimErr == nil {
			requests = append(requests, request)
		}
		reclaimResult <- claimResult{requests: requests, err: claimErr}
	}()
	var reclaimed []operations.DeletionRequest
	select {
	case result := <-reclaimResult:
		if result.err != nil || len(result.requests) != 0 {
			t.Fatalf("claim crossed active stage fence: %#v, %v", result.requests, result.err)
		}
		if err := stageTx.Commit(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		if err := stageTx.Commit(); err != nil {
			t.Fatal(err)
		}
		result := <-reclaimResult
		if result.err == nil {
			reclaimed = result.requests
		}
	}
	if _, err := store.ClaimDeletion(ctx, requestID, now.Add(30*time.Second), now.Add(90*time.Second)); !errors.Is(err, operations.ErrDeletionLeaseLost) {
		t.Fatalf("concurrent claim error = %v", err)
	}
	if len(reclaimed) == 0 {
		request, claimErr := store.ClaimDeletion(ctx, requestID, now.Add(time.Minute), now.Add(2*time.Minute))
		err = claimErr
		if claimErr == nil {
			reclaimed = []operations.DeletionRequest{request}
		}
	}
	if err != nil || len(reclaimed) != 1 || reclaimed[0].RequestID != request.RequestID ||
		reclaimed[0].ClaimToken == first[0].ClaimToken || reclaimed[0].Attempts != 2 {
		t.Fatalf("reclaimed = %#v, %v", reclaimed, err)
	}
	if _, err := store.DeletePrimaryData(ctx, first[0], now.Add(time.Minute)); !errors.Is(err, operations.ErrDeletionLeaseLost) {
		t.Fatalf("stale executor stage error = %v", err)
	}
	if _, err := operations.NewDeletionWorkflow(store, integrationClock{now: now.Add(time.Minute)}, mustAuditRecorder(t, store), bytes.Repeat([]byte{'p'}, 32)); err != nil {
		t.Fatal(err)
	}
}

func TestCockroachDeletionInboxPromotionAndLegalHoldResume(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	userID, subjectID := "inbox_user_"+suffix, "inbox_subject_"+suffix
	requestID := "ops-inbox-" + suffix
	requestedAt := time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)
	holdAsOf := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
	holdExpiry := holdAsOf.Add(2 * time.Hour)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, []any{userID, userID + "@example.invalid"}},
		{`INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $1, 'UTC')`, []any{subjectID, userID}},
		{`INSERT INTO legal_holds (target_type, target_id, approval_ref, reason, expires_at)
VALUES ('subject', $1, 'integration', 'long deletion hold', $2)`, []any{subjectID, holdExpiry}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = db.ExecContext(cleanup, `DELETE FROM legal_holds WHERE target_type = 'subject' AND target_id = $1`, subjectID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM deletion_request_claims WHERE deletion_request_id IN (SELECT id FROM deletion_requests WHERE request_id = $1)`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM audit_events WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM deletion_requests WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM deletion_request_inbox WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	queued, err := store.EnqueueDeletion(ctx, operations.DeletionRequest{
		RequestID: requestID, TargetType: operations.DeletionTargetSubject, TargetID: subjectID,
		Status: operations.DeletionRequested, LastCompletedStage: operations.DeletionStageRequested,
		RequestedAt: requestedAt, UpdatedAt: requestedAt,
	})
	if err != nil || queued.ID == "" {
		t.Fatalf("enqueue = %#v, %v", queued, err)
	}
	var durableBefore bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deletion_requests WHERE request_id = $1)`, requestID).Scan(&durableBefore); err != nil || durableBefore {
		t.Fatalf("API enqueue wrote maintenance table: exists=%t err=%v", durableBefore, err)
	}
	claim, err := store.ClaimDeletion(ctx, requestID, holdAsOf, holdAsOf.Add(time.Minute))
	if err != nil || claim.RequestID != requestID {
		t.Fatalf("promoted claim = %#v, %v", claim, err)
	}
	claims := []operations.DeletionRequest{claim}
	workflow, _ := operations.NewDeletionWorkflow(store, integrationClock{now: holdAsOf}, mustAuditRecorder(t, store), bytes.Repeat([]byte{'p'}, 32))
	_, heldErr := workflow.RunClaimed(ctx, claims[0])
	if !errors.Is(heldErr, operations.ErrLegalHoldActive) || errors.Is(heldErr, operations.ErrDeletionLeaseLost) {
		t.Fatalf("held run error = %v", heldErr)
	}
	var attempts int64
	var availableAt time.Time
	var claimToken sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT attempts, available_at, claim_token::STRING
FROM deletion_request_claims WHERE deletion_request_id = $1::UUID`, claims[0].ID).Scan(&attempts, &availableAt, &claimToken); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || availableAt.Before(holdExpiry) || claimToken.Valid {
		t.Fatalf("held claim attempts=%d available=%s token=%#v run_error=%v", attempts, availableAt, claimToken, heldErr)
	}
	if _, err := store.ClaimDeletion(ctx, requestID, holdExpiry.Add(-time.Microsecond), holdExpiry.Add(time.Minute)); !errors.Is(err, operations.ErrDeletionLeaseLost) {
		t.Fatalf("claim before hold expiry error = %v", err)
	}
	afterExpiry, err := store.ClaimDeletion(ctx, requestID, holdExpiry, holdExpiry.Add(time.Minute))
	if err != nil || afterExpiry.RequestID != requestID {
		t.Fatalf("claim after hold expiry = %#v, %v", afterExpiry, err)
	}
	workflow, _ = operations.NewDeletionWorkflow(store, integrationClock{now: holdExpiry}, mustAuditRecorder(t, store), bytes.Repeat([]byte{'p'}, 32))
	completed, err := workflow.RunClaimed(ctx, afterExpiry)
	if err != nil || completed.Status != operations.DeletionCompleted {
		t.Fatalf("resumed completion = %#v, %v", completed, err)
	}
}

type integrationClock struct{ now time.Time }

func (clock integrationClock) Now() time.Time { return clock.now }

func mustAuditRecorder(t *testing.T, store operations.AuditEventSink) *operations.AuditRecorder {
	t.Helper()
	recorder, err := operations.NewAuditRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func configureTestDeletedIdentityHMAC(t *testing.T, store *Store) {
	t.Helper()
	if err := store.ConfigureDeletedIdentityHMAC("identity-test", bytes.Repeat([]byte{'i'}, 32)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.ClearDeletedIdentityHMAC)
}

func TestCockroachReencryptionDryRunAndExecuteReportSourceKey(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	userID, subjectID := "rekey_user_"+suffix, "rekey_subject_"+suffix
	environmentID, handle := "rekey:env:"+suffix, "rekey-"+suffix
	connectionID := "028f0000-0000-7000-8000-" + suffix[len(suffix)-12:]
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, []any{userID, handle + "@example.invalid"}},
		{`INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $3, 'UTC')`, []any{subjectID, userID, handle}},
		{`INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $1, $1, 'subject', $2)`, []any{environmentID, subjectID}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	oldCipher, _ := integrations.NewAESGCMCipher(bytes.Repeat([]byte{'o'}, 32))
	newCipher, _ := integrations.NewAESGCMCipher(bytes.Repeat([]byte{'n'}, 32))
	oldKeyring, _ := operations.NewKeyringCipher("old-key", []operations.CipherKey{{ID: "old-key", Cipher: oldCipher}})
	rotationKeyring, _ := operations.NewKeyringCipher("new-key", []operations.CipherKey{
		{ID: "old-key", Cipher: oldCipher}, {ID: "new-key", Cipher: newCipher},
	})
	ciphertext, err := oldKeyring.Encrypt(ctx, []byte("integration-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_connections (
id, subject_id, environment_id, auth_method, status, access_token_ciphertext, access_token_key_id, sync_cursor
) VALUES ($1::UUID, $2, $3, 'token', 'active', $4, 'old-key', '{}'::JSONB)`, connectionID, subjectID, environmentID, ciphertext); err != nil {
		t.Fatal(err)
	}
	worker, _ := operations.NewReencryptionWorker(store, rotationKeyring, 1)
	dryRun, err := worker.RunOperator(ctx, nil, operations.ReencryptionOperatorOptions{DryRun: true})
	if dryRun.Pending < 1 || dryRun.Rotated != 0 || dryRun.ByKey["old-key"].Pending < 1 {
		t.Fatalf("dry-run = %#v, %v", dryRun, err)
	}
	var unchanged []byte
	if err := db.QueryRowContext(ctx, `SELECT access_token_ciphertext FROM provider_connections WHERE id = $1::UUID`, connectionID).Scan(&unchanged); err != nil || !bytes.Equal(unchanged, ciphertext) {
		t.Fatalf("dry-run mutated ciphertext: equal=%t err=%v", bytes.Equal(unchanged, ciphertext), err)
	}
	executed, err := worker.RunOperator(ctx, store, operations.ReencryptionOperatorOptions{Scope: "integration-" + suffix})
	if executed.Rotated < 1 || executed.ByKey["old-key"].Rotated < 1 {
		t.Fatalf("execute = %#v, %v", executed, err)
	}
	var keyID string
	var rotated []byte
	if err := db.QueryRowContext(ctx, `SELECT access_token_key_id, access_token_ciphertext FROM provider_connections WHERE id = $1::UUID`, connectionID).Scan(&keyID, &rotated); err != nil {
		t.Fatal(err)
	}
	plaintext, err := rotationKeyring.Decrypt(ctx, rotated)
	if err != nil || keyID != "new-key" || string(plaintext) != "integration-secret" {
		t.Fatalf("rotated key=%q plaintext=%q err=%v", keyID, plaintext, err)
	}
}

func TestCockroachRetentionDryRunAndExecuteRespectLegalHoldBoundary(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldEventTime := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	requestID := "ops-held-audit-" + strings.ReplaceAll(now.Format("150405.000000000"), ".", "")
	var auditID string
	if err := db.QueryRowContext(ctx, `INSERT INTO audit_events (
id, occurred_at, actor_type, action, target_type, outcome, request_id, metadata
) VALUES (gen_random_uuid(), $1, 'system', 'integration.fixture', 'maintenance', 'succeeded', $2, '{}'::JSONB)
RETURNING id::STRING`, oldEventTime, requestID).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO legal_holds (target_type, target_id, approval_ref, reason, expires_at)
VALUES ('audit_event', $1, 'integration', 'retention boundary test', $2)`, auditID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM legal_holds WHERE target_type = 'audit_event' AND target_id = $1`, auditID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM audit_events WHERE id = $1::UUID`, auditID)
	})
	request := operations.RetentionPurgeRequest{Dataset: operations.RetentionAuditEvents, Before: cutoff, AsOf: now, Limit: 10}
	count, err := store.CountExpired(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if count < 0 {
		t.Fatalf("dry-run count = %d", count)
	}
	deleted, err := store.PurgeExpired(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_ = deleted // other old fixtures may be eligible; the held row must remain.
	var remains bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM audit_events WHERE id = $1::UUID)`, auditID).Scan(&remains); err != nil || !remains {
		t.Fatalf("active legal hold row remains=%t error=%v", remains, err)
	}
}

func TestCockroachMagicLinkMailOutboxRetentionIsTerminalBoundedAndHeld(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_MAINTENANCE_DATABASE_URL to the migrated CockroachDB maintenance role")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	maintenanceDB, err := sql.Open("pgx", maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	var maintenanceUser string
	if err := maintenanceDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&maintenanceUser); err != nil {
		t.Fatalf("identify maintenance database role: %v", err)
	}
	if maintenanceUser != "jandibat_maintenance" {
		t.Fatalf("maintenance database role = %q, want jandibat_maintenance", maintenanceUser)
	}
	store, err := New(maintenanceDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatalf("maintenance-role operations readiness: %v", err)
	}
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	heldUser := "outbox_held_user_" + suffix
	heldEmail, freeEmail := heldUser+"@example.invalid", "outbox-free-"+suffix+"@example.invalid"
	old := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(1950, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, heldUser, heldEmail); err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		name, email, status string
		updatedAt           time.Time
		terminalAt          any
		terminalReason      any
		tokenHash           any
		tokenExpiresAt      any
	}
	fixtures := []fixture{
		{"free-sent", freeEmail, "sent", old, nil, nil, bytes.Repeat([]byte{1}, 32), asOf.Add(time.Hour)},
		{"held-dead", heldEmail, "dead", old, old, "max_attempts", nil, nil},
		{"active-pending", freeEmail, "pending", old, nil, nil, nil, nil},
		{"recent-superseded", freeEmail, "superseded", recent, recent, "newer_link", nil, nil},
	}
	ids := make(map[string]string, len(fixtures))
	for _, fixture := range fixtures {
		var outboxID string
		if err := db.QueryRowContext(ctx, `INSERT INTO magic_link_mail_outbox (
token_hash, token_expires_at, consumed_at, recipient_email, redirect_uri,
purpose, status, attempts, available_at, created_at, updated_at, terminal_at, terminal_reason
) VALUES ($1, $2, NULL, $3, '/after', 'signin', $4, 0, $5, $5, $6, $7, $8)
RETURNING id::STRING`, fixture.tokenHash, fixture.tokenExpiresAt, fixture.email, fixture.status, old, fixture.updatedAt, fixture.terminalAt, fixture.terminalReason).Scan(&outboxID); err != nil {
			t.Fatal(err)
		}
		ids[fixture.name] = outboxID
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO legal_holds (
target_type, target_id, approval_ref, reason, expires_at, created_at
) VALUES ('account', $1, 'integration', 'mail outbox retention', $2, $3)`, heldUser, asOf.Add(time.Hour), cutoff); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM legal_holds WHERE target_type = 'account' AND target_id = $1`, heldUser)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM magic_link_mail_outbox WHERE recipient_email IN ($1, $2)`, heldEmail, freeEmail)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, heldUser)
	})
	request := operations.RetentionPurgeRequest{
		Dataset: operations.RetentionMagicLinkMailOutbox, Before: cutoff, AsOf: asOf, Limit: 100,
	}
	count, err := store.CountExpired(ctx, request)
	if err != nil || count < 1 {
		t.Fatalf("mail outbox dry-run count = %d, %v", count, err)
	}
	for name, id := range ids {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM magic_link_mail_outbox WHERE id = $1::UUID)`, id).Scan(&exists); err != nil || !exists {
			t.Fatalf("dry-run mutated %s exists=%t err=%v", name, exists, err)
		}
	}
	deleted, err := store.PurgeExpired(ctx, request)
	if err != nil || deleted < 1 {
		t.Fatalf("magic link mail outbox execute deleted = %d, %v", deleted, err)
	}
	for name, wantExists := range map[string]bool{
		"free-sent": false, "held-dead": true, "active-pending": true, "recent-superseded": true,
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM magic_link_mail_outbox WHERE id = $1::UUID)`, ids[name]).Scan(&exists); err != nil || exists != wantExists {
			t.Fatalf("after purge %s exists=%t want=%t err=%v", name, exists, wantExists, err)
		}
	}
}

func TestCockroachMutationAuditOutboxRetentionUsesMaintenanceRoleAndLegalHoldBoundary(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_MAINTENANCE_DATABASE_URL to the migrated CockroachDB maintenance role")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	maintenanceDB, err := sql.Open("pgx", maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	var maintenanceUser string
	if err := maintenanceDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&maintenanceUser); err != nil {
		t.Fatalf("identify maintenance database role: %v", err)
	}
	if maintenanceUser != "jandibat_maintenance" {
		t.Fatalf("maintenance database role = %q, want jandibat_maintenance", maintenanceUser)
	}
	store, err := New(maintenanceDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatalf("maintenance-role operations readiness: %v", err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	freeTarget, heldTarget := "audit-free-"+suffix, "audit-held-"+suffix
	old := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(1950, 1, 1, 0, 0, 0, 0, time.UTC)
	type fixture struct {
		name, target, status string
		occurredAt           time.Time
		deliveredAt          any
		terminalAt           any
		terminalReason       any
	}
	fixtures := []fixture{
		{"free-delivered", freeTarget, "delivered", old, old, nil, nil},
		{"held-dead", heldTarget, "dead", old, nil, old, "max_attempts_exhausted"},
		{"active-pending", freeTarget, "pending", old, nil, nil, nil},
		{"recent-delivered", freeTarget, "delivered", recent, recent, nil, nil},
	}
	ids := make(map[string]string, len(fixtures))
	for _, fixture := range fixtures {
		var outboxID string
		if err := db.QueryRowContext(ctx, `INSERT INTO mutation_audit_outbox (
audit_event_id, request_id, occurred_at, actor_type, action, target_type, target_id,
outcome, metadata, status, attempts, available_at, created_at, updated_at,
delivered_at, terminal_at, terminal_reason
) VALUES (
gen_random_uuid(), $1, $2, 'system', 'integration.mutation', 'subject', $3,
'succeeded', '{}'::JSONB, $4, 0, $2, $2, $2, $5, $6, $7
) RETURNING id::STRING`, "audit-retention-"+suffix+"-"+fixture.name, fixture.occurredAt,
			fixture.target, fixture.status, fixture.deliveredAt, fixture.terminalAt, fixture.terminalReason).Scan(&outboxID); err != nil {
			t.Fatal(err)
		}
		ids[fixture.name] = outboxID
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO legal_holds (
target_type, target_id, approval_ref, reason, expires_at, created_at
) VALUES
('subject', $1, 'integration', 'active audit outbox hold', $3, $2),
('subject', $4, 'integration', 'expired audit outbox hold', $5, $2)`, heldTarget, cutoff, asOf.Add(time.Hour), freeTarget, asOf); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM legal_holds WHERE target_type = 'subject' AND target_id IN ($1, $2)`, heldTarget, freeTarget)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM mutation_audit_outbox WHERE target_type = 'subject' AND target_id IN ($1, $2)`, heldTarget, freeTarget)
	})

	request := operations.RetentionPurgeRequest{
		Dataset: operations.RetentionMutationAuditOutbox, Before: cutoff, AsOf: asOf, Limit: 100,
	}
	count, err := store.CountExpired(ctx, request)
	if err != nil || count < 1 {
		t.Fatalf("mutation audit outbox dry-run count = %d, %v", count, err)
	}
	for name, id := range ids {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM mutation_audit_outbox WHERE id = $1::UUID)`, id).Scan(&exists); err != nil || !exists {
			t.Fatalf("dry-run mutated %s exists=%t err=%v", name, exists, err)
		}
	}
	deleted, err := store.PurgeExpired(ctx, request)
	if err != nil || deleted < 1 {
		t.Fatalf("mutation audit outbox execute deleted = %d, %v", deleted, err)
	}
	for name, wantExists := range map[string]bool{
		"free-delivered": false, "held-dead": true, "active-pending": true, "recent-delivered": true,
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM mutation_audit_outbox WHERE id = $1::UUID)`, ids[name]).Scan(&exists); err != nil || exists != wantExists {
			t.Fatalf("after purge %s exists=%t want=%t err=%v", name, exists, wantExists, err)
		}
	}
}

func TestCockroachOrphanedPrivateEnvironmentRetentionUsesMaintenanceRoleAndAllReferences(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_MAINTENANCE_DATABASE_URL to the migrated CockroachDB maintenance role")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	maintenanceDB, err := sql.Open("pgx", maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	var maintenanceUser string
	if err := maintenanceDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&maintenanceUser); err != nil {
		t.Fatalf("identify maintenance database role: %v", err)
	}
	if maintenanceUser != "jandibat_maintenance" {
		t.Fatalf("maintenance database role = %q, want jandibat_maintenance", maintenanceUser)
	}
	store, err := New(maintenanceDB)
	if err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}
	freeUser, subjectHeldUser, accountHeldUser := "orphan_free_user_"+suffix, "orphan_subject_hold_user_"+suffix, "orphan_account_hold_user_"+suffix
	freeSubject, subjectHeldSubject, accountHeldSubject := "orphan_free_subject_"+suffix, "orphan_subject_hold_"+suffix, "orphan_account_hold_"+suffix
	old := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, fixture := range []struct{ userID, subjectID string }{
		{freeUser, freeSubject}, {subjectHeldUser, subjectHeldSubject}, {accountHeldUser, accountHeldSubject},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, fixture.userID, fixture.userID+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $1, 'UTC')`, fixture.subjectID, fixture.userID); err != nil {
			t.Fatal(err)
		}
	}

	freeConnection := "connection:orphan-free-" + suffix
	freeCustom := "custom-provider:orphan-free-" + suffix
	subjectHeld := "custom-provider:subject-held-" + suffix
	accountHeld := "connection:account-held-" + suffix
	ageBoundary := "custom-provider:age-boundary-" + suffix
	legacyCustom := "custom:retained-" + suffix
	globalPrivatePrefix := "custom-provider:global-retained-" + suffix
	referenced := map[string]string{
		"provider_connection":   "connection:referenced-connection-" + suffix,
		"custom_provider":       "custom-provider:referenced-provider-" + suffix,
		"provider_sync_job":     "connection:referenced-job-" + suffix,
		"custom_activity_event": "custom-provider:referenced-event-" + suffix,
		"activity_fact":         "connection:referenced-fact-" + suffix,
		"activity_refresh":      "custom-provider:referenced-refresh-" + suffix,
		"job_connection":        "connection:job-owner-" + suffix,
		"event_provider":        "custom-provider:event-owner-" + suffix,
	}
	type environmentFixture struct {
		id, scope, owner string
		updatedAt        time.Time
	}
	environments := []environmentFixture{
		{freeConnection, "subject", freeSubject, old}, {freeCustom, "subject", freeSubject, old},
		{subjectHeld, "subject", subjectHeldSubject, old}, {accountHeld, "subject", accountHeldSubject, old},
		{ageBoundary, "subject", freeSubject, cutoff},
		{legacyCustom, "subject", freeSubject, old}, {globalPrivatePrefix, "global", "", old},
	}
	for _, environmentID := range referenced {
		environments = append(environments, environmentFixture{environmentID, "subject", freeSubject, old})
	}
	allEnvironmentIDs := make([]string, 0, len(environments))
	for _, fixture := range environments {
		allEnvironmentIDs = append(allEnvironmentIDs, fixture.id)
		var owner any
		if fixture.owner != "" {
			owner = fixture.owner
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO environments (
id, key, name, scope, owner_subject_id, created_at, updated_at
) VALUES ($1, $1, $1, $2, $3, $4, $4)`, fixture.id, fixture.scope, owner, fixture.updatedAt); err != nil {
			t.Fatal(err)
		}
	}

	connectionID := "a18f0000-0000-7000-8000-" + suffix
	jobConnectionID := "b18f0000-0000-7000-8000-" + suffix
	for _, fixture := range []struct{ id, environmentID string }{
		{connectionID, referenced["provider_connection"]}, {jobConnectionID, referenced["job_connection"]},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO provider_connections (
id, subject_id, environment_id, auth_method, status
) VALUES ($1::UUID, $2, $3, 'none', 'active')`, fixture.id, freeSubject, fixture.environmentID); err != nil {
			t.Fatal(err)
		}
	}
	customProviderID := "c18f0000-0000-7000-8000-" + suffix
	eventProviderID := "d18f0000-0000-7000-8000-" + suffix
	for index, fixture := range []struct{ id, environmentID string }{
		{customProviderID, referenced["custom_provider"]}, {eventProviderID, referenced["event_provider"]},
	} {
		ingestTokenHash := sha256.Sum256([]byte("orphan-ref-" + fixture.id))
		if _, err := db.ExecContext(ctx, `INSERT INTO custom_providers (
id, owner_user_id, subject_id, environment_id, slug, name
) VALUES ($1::UUID, $2, $3, $4, $5, $5)`, fixture.id, freeUser, freeSubject, fixture.environmentID,
			"orphan-ref-"+suffix+string(rune('a'+index))); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO custom_provider_secrets (provider_id, ingest_token_hash) VALUES ($1::UUID, $2::BYTES)`, fixture.id, ingestTokenHash[:]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_sync_jobs (
provider_connection_id, subject_id, environment_id, status
) VALUES ($1::UUID, $2, $3, 'queued')`, jobConnectionID, freeSubject, referenced["provider_sync_job"]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO custom_activity_events (
custom_provider_id, subject_id, environment_id, event_id, activity_date, action, metric_name, metric_value
) VALUES ($1::UUID, $2, $3, $4, '1800-01-01', 'integration', 'count', 1)`, eventProviderID, freeSubject,
		referenced["custom_activity_event"], "orphan-event-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO activity_facts (
subject_id, environment_id, activity_date, action, metric_name, metric_value, metadata
) VALUES ($1, $2, '1800-01-01', 'integration', 'count', 1, '{}'::JSONB)`, freeSubject, referenced["activity_fact"]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO activity_refresh_cache (
subject_id, environment_id, activity_date, fetched_at
) VALUES ($1, $2, '1800-01-01', $3)`, freeSubject, referenced["activity_refresh"], old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO legal_holds (
target_type, target_id, approval_ref, reason, expires_at, created_at
) VALUES
('subject', $1, 'integration', 'orphan environment subject hold', $4, $3),
('account', $2, 'integration', 'orphan environment account hold', $4, $3),
('subject', $5, 'integration', 'orphan environment expired boundary', $6, $3)`,
		subjectHeldSubject, accountHeldUser, cutoff, asOf.Add(time.Hour), freeSubject, asOf); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM legal_holds WHERE target_id IN ($1, $2, $3)`, subjectHeldSubject, accountHeldUser, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM activity_refresh_cache WHERE subject_id = $1`, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM custom_activity_events WHERE subject_id = $1`, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM activity_facts WHERE subject_id = $1`, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM provider_sync_jobs WHERE subject_id = $1`, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM provider_connections WHERE subject_id = $1`, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM custom_providers WHERE subject_id = $1`, freeSubject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id IN ($1, $2, $3)`, freeUser, subjectHeldUser, accountHeldUser)
	})

	request := operations.RetentionPurgeRequest{
		Dataset: operations.RetentionOrphanedProviderEnvironments, Before: cutoff, AsOf: asOf, Limit: 100,
	}
	count, err := store.CountExpired(ctx, request)
	if err != nil || count < 2 {
		t.Fatalf("orphan environment dry-run count = %d, %v", count, err)
	}
	for _, environmentID := range allEnvironmentIDs {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM environments WHERE id = $1)`, environmentID).Scan(&exists); err != nil || !exists {
			t.Fatalf("dry-run mutated environment %q exists=%t err=%v", environmentID, exists, err)
		}
	}
	deleted, err := store.PurgeExpired(ctx, request)
	if err != nil || deleted < 2 {
		t.Fatalf("orphan environment execute deleted = %d, %v", deleted, err)
	}
	wantExists := map[string]bool{
		freeConnection: false, freeCustom: false, subjectHeld: true, accountHeld: true,
		ageBoundary: true, legacyCustom: true, globalPrivatePrefix: true,
	}
	for _, environmentID := range referenced {
		wantExists[environmentID] = true
	}
	for environmentID, want := range wantExists {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM environments WHERE id = $1)`, environmentID).Scan(&exists); err != nil || exists != want {
			t.Fatalf("after purge environment %q exists=%t want=%t err=%v", environmentID, exists, want, err)
		}
	}
}

func TestCockroachCompletedDeletionRetentionWaitsForBackupExpiryAndLegalHold(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	heldTarget, freeTarget := "held_deleted_"+suffix, "free_deleted_"+suffix
	heldRequest, freeRequest := "held-request-"+suffix, "free-request-"+suffix
	requestedAt := time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)
	completedAt := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	backupExpiry := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, fixture := range []struct{ requestID, targetID string }{{heldRequest, heldTarget}, {freeRequest, freeTarget}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO deletion_requests (
request_id, target_type, target_id, status, last_completed_stage, subject_ids,
requested_at, updated_at, completed_at, backup_expiry_at
) VALUES ($1, 'account', $2, 'completed', 'completed', ARRAY[]::STRING[], $3, $4, $4, $5)`,
			fixture.requestID, fixture.targetID, requestedAt, completedAt, backupExpiry); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(fixture.requestID))
		if _, err := db.ExecContext(ctx, `INSERT INTO deleted_identity_tombstones_v2 (
identity_key_id, identity_digest, deletion_request_id, created_at, expires_at
) SELECT 'identity-test', $2, id, $3, $4 FROM deletion_requests WHERE request_id = $1`,
			fixture.requestID, digest[:], completedAt, backupExpiry); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO legal_holds (target_type, target_id, approval_ref, reason, expires_at, created_at)
VALUES ('account', $1, 'integration', 'completed deletion retention', $2, $3)`, heldTarget, asOf.Add(time.Hour), backupExpiry); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM legal_holds WHERE target_type = 'account' AND target_id = $1`, heldTarget)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM deleted_identity_tombstones_v2 WHERE deletion_request_id IN (SELECT id FROM deletion_requests WHERE request_id IN ($1, $2))`, heldRequest, freeRequest)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM deletion_requests WHERE request_id IN ($1, $2)`, heldRequest, freeRequest)
	})
	tombstonePurge := operations.RetentionPurgeRequest{Dataset: operations.RetentionDeletedIdentityTombstonesV2, Before: asOf, AsOf: asOf, Limit: 100}
	tombstoneCount, err := store.CountExpired(ctx, tombstonePurge)
	if err != nil || tombstoneCount < 1 {
		t.Fatalf("HMAC tombstone dry-run count = %d, %v", tombstoneCount, err)
	}
	var tombstonesBefore int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deleted_identity_tombstones_v2
WHERE deletion_request_id IN (SELECT id FROM deletion_requests WHERE request_id IN ($1, $2))`, heldRequest, freeRequest).Scan(&tombstonesBefore); err != nil || tombstonesBefore != 2 {
		t.Fatalf("HMAC tombstone dry-run mutated rows: count=%d err=%v", tombstonesBefore, err)
	}
	if _, err := store.PurgeExpired(ctx, tombstonePurge); err != nil {
		t.Fatal(err)
	}
	var heldTombstone, freeTombstone bool
	if err := db.QueryRowContext(ctx, `SELECT
EXISTS (SELECT 1 FROM deleted_identity_tombstones_v2 WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1)),
EXISTS (SELECT 1 FROM deleted_identity_tombstones_v2 WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $2))`, heldRequest, freeRequest).Scan(&heldTombstone, &freeTombstone); err != nil {
		t.Fatal(err)
	}
	if !heldTombstone || freeTombstone {
		t.Fatalf("HMAC tombstone retention boundary held=%t free=%t", heldTombstone, freeTombstone)
	}
	purge := operations.RetentionPurgeRequest{Dataset: operations.RetentionDeletionRequests, Before: asOf, AsOf: asOf, Limit: 100}
	count, err := store.CountExpired(ctx, purge)
	if err != nil || count < 1 {
		t.Fatalf("deletion retention dry-run count = %d, %v", count, err)
	}
	var before int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deletion_requests WHERE request_id IN ($1, $2)`, heldRequest, freeRequest).Scan(&before); err != nil || before != 2 {
		t.Fatalf("dry-run mutated rows: count=%d err=%v", before, err)
	}
	if _, err := store.PurgeExpired(ctx, purge); err != nil {
		t.Fatal(err)
	}
	var held, free bool
	if err := db.QueryRowContext(ctx, `SELECT
EXISTS (SELECT 1 FROM deletion_requests WHERE request_id = $1),
EXISTS (SELECT 1 FROM deletion_requests WHERE request_id = $2)`, heldRequest, freeRequest).Scan(&held, &free); err != nil {
		t.Fatal(err)
	}
	if !held || free {
		t.Fatalf("retention boundary held=%t free=%t", held, free)
	}
}
