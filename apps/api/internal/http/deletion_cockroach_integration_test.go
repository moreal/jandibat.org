package apihttp_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type fixedDeletionUserAuth struct {
	fakeAuth
	user auth.User
}

func (service fixedDeletionUserAuth) AuthenticateSession(context.Context, string) (auth.User, error) {
	return service.user, nil
}

func TestCockroachSubjectDeleteHTTPEnqueuesDurableRequest(t *testing.T) {
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
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if apiDSN == "" {
		parsed, parseErr := url.Parse(dsn)
		if parseErr != nil {
			t.Fatalf("parse Cockroach integration DSN: %v", parseErr)
		}
		parsed.User = url.User("jandibat_api")
		apiDSN = parsed.String()
	}
	apiDB, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = apiDB.Close() })
	if err := apiDB.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB as jandibat_api: %v", err)
	}

	suffix := deletionIntegrationSuffix(t)
	userID := "it_delete_user_" + suffix
	email := userID + "@example.invalid"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $3, $3)`, userID, email, now); err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM mutation_audit_outbox WHERE target_id LIKE $1`, "%"+suffix)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM audit_events WHERE target_id LIKE $1`, "%"+suffix)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deletion_requests WHERE target_id LIKE $1`, "it_delete_subject_%"+suffix)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deletion_request_inbox WHERE target_id LIKE $1`, "it_delete_subject_%"+suffix)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM subjects WHERE owner_user_id = $1`, userID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	subjectRepository := newPGXSubjectStore(t, ctx, apiDSN)
	subjectService, err := subjects.NewService(subjectRepository, subjects.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := operationsstore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	requester, err := operations.NewDeletionRequester(operationStore, fixedDeletionClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	auditRecorder, err := operations.NewAuditRecorder(operationStore)
	if err != nil {
		t.Fatal(err)
	}
	user := auth.User{ID: userID, PrimaryEmail: email, Status: auth.UserStatusActive, EmailVerifiedAt: &now, CreatedAt: now, UpdatedAt: now}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fixedDeletionUserAuth{user: user}, Subjects: subjectService, SubjectDeletions: requester,
		Audit: auditRecorder, AuditSourceKey: []byte("deletion-integration-audit-source"), MutationAudits: operationStore,
	})

	subjectID := "it_delete_subject_" + suffix
	handle := "it-delete-" + suffix
	insertDeletionIntegrationSubject(t, ctx, db, subjectID, userID, handle, now)
	correlationID := "http-correlation-must-not-be-deletion-id-" + suffix
	request := httptest.NewRequest(http.MethodDelete, "/v1/subjects/"+handle, nil)
	request.Header.Set("Authorization", "Bearer session-token")
	request.Header.Set("X-Request-ID", correlationID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode deletion response: %v", err)
	}
	if body.RequestID == "" || body.RequestID == correlationID || body.Status != string(operations.DeletionRequested) {
		t.Fatalf("deletion response = %#v, correlation=%q", body, correlationID)
	}

	var status, targetType, targetID string
	var completedAt, backupExpiryAt, auditEventID sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT status, target_type, target_id, promoted_at::STRING, NULL::STRING, NULL::STRING
FROM deletion_request_inbox WHERE request_id = $1`, body.RequestID).Scan(
		&status, &targetType, &targetID, &completedAt, &backupExpiryAt, &auditEventID,
	); err != nil {
		t.Fatalf("load queued deletion: %v", err)
	}
	if status != string(operations.DeletionRequested) || targetType != string(operations.DeletionTargetSubject) || targetID != subjectID ||
		completedAt.Valid || backupExpiryAt.Valid || auditEventID.Valid {
		t.Fatalf("queued deletion = status=%q type=%q target=%q completed=%v backup=%v audit=%v", status, targetType, targetID, completedAt, backupExpiryAt, auditEventID)
	}
	var privilegedRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deletion_requests WHERE request_id = $1`, body.RequestID).Scan(&privilegedRows); err != nil || privilegedRows != 0 {
		t.Fatalf("API bypassed inbox into maintenance queue: count=%d error=%v", privilegedRows, err)
	}
	var subjectsLeft int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM subjects WHERE id = $1`, subjectID).Scan(&subjectsLeft); err != nil || subjectsLeft != 1 {
		t.Fatalf("enqueue unexpectedly deleted subject: count=%d error=%v", subjectsLeft, err)
	}

	replayRequest := httptest.NewRequest(http.MethodDelete, "/v1/subjects/"+handle, nil)
	replayRequest.Header.Set("Authorization", "Bearer session-token")
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("repeated delete status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	var replayBody struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayBody); err != nil || replayBody.RequestID != body.RequestID {
		t.Fatalf("repeated delete body=%#v err=%v", replayBody, err)
	}
	var inboxRows, outboxRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deletion_request_inbox WHERE target_type='subject' AND target_id=$1`, subjectID).Scan(&inboxRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='delete.v1.subjects.subject' AND target_id=$1 AND outcome='succeeded'`, handle).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if inboxRows != 1 || outboxRows != 2 {
		t.Fatalf("repeat durability: inbox=%d success_outboxes=%d", inboxRows, outboxRows)
	}
}

type fixedDeletionClock struct{ now time.Time }

func (clock fixedDeletionClock) Now() time.Time { return clock.now }

func insertDeletionIntegrationSubject(t *testing.T, ctx context.Context, db *sql.DB, id, userID, handle string, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public, created_at, updated_at)
VALUES ($1, $2, $3, 'UTC', true, $4, $4)`, id, userID, handle, now); err != nil {
		t.Fatalf("insert integration subject: %v", err)
	}
}

func deletionIntegrationSuffix(t *testing.T) string {
	t.Helper()
	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value[:])
}
