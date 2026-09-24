package apihttp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	graph "github.com/moreal/jandibat.org/apps/api/internal/graphql"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
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

type fixedDeletionSession struct {
	handlers.SessionService
	userID string
}

func (service fixedDeletionSession) CurrentSession(context.Context, string) (auth.Session, error) {
	return auth.Session{ID: "00000000-0000-4000-8000-000000000001", UserID: service.userID}, nil
}

func (service fixedDeletionSession) GetSessionByID(context.Context, string, string) (auth.Session, error) {
	return auth.Session{ID: "00000000-0000-4000-8000-000000000001", UserID: service.userID}, nil
}

func (fixedDeletionSession) ListSessionsPage(context.Context, string, *auth.SessionCursor, int) ([]auth.Session, error) {
	return nil, nil
}

type unusedGraphQLAuthAccounts struct{ graph.AuthAccountService }
type unusedGraphQLPasskeys struct{ graph.PasskeyMutationService }

type unusedGraphQLActivity struct{}

func (unusedGraphQLActivity) ExecuteSnapshot(context.Context, appactivity.SnapshotInput) (appactivity.SnapshotOutput, error) {
	return appactivity.SnapshotOutput{}, errors.New("unused GraphQL activity port")
}

type unusedGraphQLDeletions struct{}

func (unusedGraphQLDeletions) Request(context.Context, string, operations.DeletionTargetType, string) (operations.DeletionRequest, error) {
	return operations.DeletionRequest{}, errors.New("unused GraphQL deletion port")
}

type unusedGraphQLAuthFailureReporter struct{}

func (unusedGraphQLAuthFailureReporter) ReportMagicLinkRequestFailure(context.Context)    {}
func (unusedGraphQLAuthFailureReporter) ReportSessionCompensationFailure(context.Context) {}

func cockroachGraphQLDependencies(subjectsPort *subjects.Service, connectionPort *integrations.ConnectionService, customPort *integrations.CustomProviderService, syncPort *integrations.SyncService, deletionPort *operations.DeletionRequester, sessionPort fixedDeletionSession) apihttp.GraphQLDependencies {
	graphDeletions := graph.SubjectMutationServices{Subjects: subjectsPort, Deletions: unusedGraphQLDeletions{}}
	if deletionPort != nil {
		graphDeletions.Deletions = deletionPort
	}
	return apihttp.GraphQLDependencies{
		NodeServices: graph.NodeServices{
			ViewerUsers: subjectsPort, SessionPages: sessionPort, Subjects: subjectsPort,
			Connections: connectionPort, CustomProviders: customPort, SyncJobs: syncPort,
			Sessions: sessionPort, Activity: unusedGraphQLActivity{},
		},
		SubjectQueries:          graph.SubjectQueryServices{Pages: subjectsPort, UserSettings: subjectsPort, SubjectSettings: subjectsPort},
		SubjectMutations:        graphDeletions,
		IntegrationQueries:      graph.IntegrationQueryServices{Connections: connectionPort, CustomProviders: customPort},
		ConnectionMutations:     graph.ConnectionMutationServices{Connections: connectionPort, Sync: syncPort},
		CustomProviderMutations: graph.CustomProviderMutationServices{Providers: customPort},
		SyncJobs:                syncPort, AuthAccounts: unusedGraphQLAuthAccounts{}, Passkeys: unusedGraphQLPasskeys{},
		AuthFailureReporter: unusedGraphQLAuthFailureReporter{},
	}
}

func performGraphQLIntegrationRequest(t *testing.T, router http.Handler, query string, variables map[string]any, headers ...map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer session-token")
	for _, group := range headers {
		for key, value := range group {
			request.Header.Set(key, value)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
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
		t.Skip("set JANDIBAT_TEST_API_DATABASE_URL to the isolated API-role database")
	}
	apiDB, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = apiDB.Close() })
	if err := apiDB.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB as jandibat_api: %v", err)
	}
	var currentUser string
	if err := apiDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}

	suffix := deletionIntegrationSuffix(t)
	userID := "it_delete_user_" + suffix
	subjectID := "550e8400-e29b-41d4-a716-" + suffix
	failedSubjectID := "550e8401-e29b-41d4-a716-" + suffix
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
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deletion_requests WHERE target_id IN ($1,$2)`, subjectID, failedSubjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM deletion_request_inbox WHERE target_id IN ($1,$2)`, subjectID, failedSubjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM subjects WHERE owner_user_id = $1`, userID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	pool, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	subjectRepository, err := subjectstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	subjectService, err := subjects.NewService(subjectRepository, subjects.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := operationsstore.New(pool)
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
	integrationDB, err := integrationstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	activityDB, err := activitystore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := integrations.NewAESGCMCipher(bytes.Repeat([]byte{0x45}, 32))
	if err != nil {
		t.Fatal(err)
	}
	customService, err := integrations.NewCustomProviderService(integrationDB, cipher, activityDB, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	connectionService, err := integrations.NewConnectionService(integrationDB, cipher, nil, nil, activityDB)
	if err != nil {
		t.Fatal(err)
	}
	syncService, err := integrations.NewSyncService(integrationDB, integrationDB, activityDB, cipher, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	user := auth.User{ID: userID, PrimaryEmail: email, Status: auth.UserStatusActive, EmailVerifiedAt: &now, CreatedAt: now, UpdatedAt: now}
	deps := apihttp.Dependencies{
		Auth: fixedDeletionUserAuth{user: user}, Sessions: fixedDeletionSession{userID: userID},
		Connections: connectionService, CustomProviders: customService,
		Audit: auditRecorder, AuditSourceKey: []byte("deletion-integration-audit-source"), MutationAudits: operationStore,
		RateLimiter: handlers.DefaultRateLimiter(),
	}
	graphDeps := cockroachGraphQLDependencies(subjectService, connectionService, customService, syncService, requester, fixedDeletionSession{userID: userID})
	router, err := apihttp.NewRouterWithGraphQL(deps, graphDeps)
	if err != nil {
		t.Fatal(err)
	}

	handle := "it-delete-" + suffix
	insertDeletionIntegrationSubject(t, ctx, db, subjectID, userID, handle, now)
	mutation := `mutation RequestDeletion($subjectID: ID!) { requestSubjectDeletion(input: {subjectID: $subjectID}) { errors { code } request { requestID status } } }`
	variables := map[string]any{"subjectID": relayid.Encode(relayid.Subject, subjectID)}
	correlationID := "http-correlation-must-not-be-deletion-id-" + suffix
	response := performGraphQLIntegrationRequest(t, router, mutation, variables, map[string]string{"X-Request-ID": correlationID})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			RequestSubjectDeletion struct {
				Errors []struct {
					Code string `json:"code"`
				} `json:"errors"`
				Request struct {
					RequestID string `json:"requestID"`
					Status    string `json:"status"`
				} `json:"request"`
			} `json:"requestSubjectDeletion"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode deletion response: %v", err)
	}
	requestID := body.Data.RequestSubjectDeletion.Request.RequestID
	if requestID == "" || requestID == correlationID || response.Header().Get("X-Request-ID") != correlationID || body.Data.RequestSubjectDeletion.Request.Status != string(operations.DeletionRequested) || len(body.Data.RequestSubjectDeletion.Errors) != 0 {
		t.Fatalf("deletion response = %#v; correlation=%q response-correlation=%q", body, correlationID, response.Header().Get("X-Request-ID"))
	}

	var status, targetType, targetID string
	var completedAt, backupExpiryAt, auditEventID sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT status, target_type, target_id, promoted_at::STRING, NULL::STRING, NULL::STRING
FROM deletion_request_inbox WHERE request_id = $1`, requestID).Scan(
		&status, &targetType, &targetID, &completedAt, &backupExpiryAt, &auditEventID,
	); err != nil {
		t.Fatalf("load queued deletion: %v", err)
	}
	if status != string(operations.DeletionRequested) || targetType != string(operations.DeletionTargetSubject) || targetID != subjectID ||
		completedAt.Valid || backupExpiryAt.Valid || auditEventID.Valid {
		t.Fatalf("queued deletion = status=%q type=%q target=%q completed=%v backup=%v audit=%v", status, targetType, targetID, completedAt, backupExpiryAt, auditEventID)
	}
	var privilegedRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deletion_requests WHERE request_id = $1`, requestID).Scan(&privilegedRows); err != nil || privilegedRows != 0 {
		t.Fatalf("API bypassed inbox into maintenance queue: count=%d error=%v", privilegedRows, err)
	}
	var subjectsLeft int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM subjects WHERE id = $1`, subjectID).Scan(&subjectsLeft); err != nil || subjectsLeft != 1 {
		t.Fatalf("enqueue unexpectedly deleted subject: count=%d error=%v", subjectsLeft, err)
	}

	replayResponse := performGraphQLIntegrationRequest(t, router, mutation, variables)
	if replayResponse.Code != http.StatusOK {
		t.Fatalf("repeated delete status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	var replayBody struct {
		Data struct {
			RequestSubjectDeletion struct {
				Request struct {
					RequestID string `json:"requestID"`
				} `json:"request"`
			} `json:"requestSubjectDeletion"`
		} `json:"data"`
	}
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayBody); err != nil || replayBody.Data.RequestSubjectDeletion.Request.RequestID != requestID {
		t.Fatalf("repeated delete body=%#v err=%v", replayBody, err)
	}
	var inboxRows, outboxRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deletion_request_inbox WHERE target_type='subject' AND target_id=$1`, subjectID).Scan(&inboxRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.graphql' AND outcome='succeeded' AND actor_id=$1 AND target_type='subject' AND target_id=$2`, userID, subjectID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if inboxRows != 1 || outboxRows != 2 {
		t.Fatalf("repeat durability: inbox=%d success_outboxes=%d", inboxRows, outboxRows)
	}
	failedHandle := "it-delete-failed-" + suffix
	insertDeletionIntegrationSubject(t, ctx, db, failedSubjectID, userID, failedHandle, now)
	failedDeps := deps
	failedDeps.MutationAudits = failingEnqueueCoordinator{next: operationStore}
	failedRouter, err := apihttp.NewRouterWithGraphQL(failedDeps, graphDeps)
	if err != nil {
		t.Fatal(err)
	}
	failedResponse := performGraphQLIntegrationRequest(t, failedRouter, mutation, map[string]any{"subjectID": relayid.Encode(relayid.Subject, failedSubjectID)})
	if failedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed audit delete status=%d body=%s", failedResponse.Code, failedResponse.Body.String())
	}
	var escapedInbox int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deletion_request_inbox WHERE target_id=$1`, failedSubjectID).Scan(&escapedInbox); err != nil || escapedInbox != 0 {
		t.Fatalf("failed audit left deletion inbox rows=%d err=%v", escapedInbox, err)
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
