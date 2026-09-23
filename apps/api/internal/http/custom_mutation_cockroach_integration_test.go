package apihttp_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type failingEnqueueCoordinator struct {
	next operations.MutationAuditCoordinator
}

func (coordinator failingEnqueueCoordinator) BeginMutation(ctx context.Context) (context.Context, operations.MutationAuditTransaction, error) {
	ctx, transaction, err := coordinator.next.BeginMutation(ctx)
	return ctx, failingEnqueueTransaction{MutationAuditTransaction: transaction}, err
}

type failingEnqueueTransaction struct {
	operations.MutationAuditTransaction
}

func (failingEnqueueTransaction) Enqueue(context.Context, operations.AuditEvent) error {
	return errors.New("forced audit outbox failure")
}

func TestCockroachCustomAndConnectionHTTPMutationsShareAuditTransaction(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, _ := sql.Open("pgx", adminDSN)
	apiDB, _ := sql.Open("pgx", apiDSN)
	t.Cleanup(func() { _ = admin.Close(); _ = apiDB.Close() })
	var currentUser string
	if err := apiDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	userID := "custom-atomic-user-" + suffix
	subjectID := "custom-atomic-subject-" + suffix
	handle := "custom-atomic-" + suffix
	email := userID + "@example.invalid"
	otherUserID := "custom-other-user-" + suffix
	otherSubjectID := "custom-other-subject-" + suffix
	otherHandle := "custom-other-" + suffix
	otherEmail := otherUserID + "@example.invalid"
	if _, err := admin.ExecContext(ctx, `INSERT INTO users(id,primary_email,status,created_at,updated_at) VALUES($1,$2,'active',$3,$3)`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO subjects(id,owner_user_id,handle,timezone,is_public,created_at,updated_at) VALUES($1,$2,$3,'UTC',false,$4,$4)`, subjectID, userID, handle, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO users(id,primary_email,status,created_at,updated_at) VALUES($1,$2,'active',$3,$3)`, otherUserID, otherEmail, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO subjects(id,owner_user_id,handle,timezone,is_public,created_at,updated_at) VALUES($1,$2,$3,'UTC',false,$4,$4)`, otherSubjectID, otherUserID, otherHandle, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = admin.ExecContext(cleanup, `DELETE FROM mutation_audit_outbox WHERE actor_id IN ($1,$2) OR target_id LIKE $3`, userID, otherUserID, "%"+suffix+"%")
		_, _ = admin.ExecContext(cleanup, `DELETE FROM audit_events WHERE actor_id IN ($1,$2) OR target_id LIKE $3`, userID, otherUserID, "%"+suffix+"%")
		_, _ = admin.ExecContext(cleanup, `DELETE FROM provider_connections WHERE subject_id=$1`, subjectID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM custom_providers WHERE subject_id IN ($1,$2)`, subjectID, otherSubjectID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM environments WHERE owner_subject_id IN ($1,$2)`, subjectID, otherSubjectID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM subjects WHERE id IN ($1,$2)`, subjectID, otherSubjectID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM users WHERE id IN ($1,$2)`, userID, otherUserID)
	})

	integrationDB, _ := integrationstore.New(apiDB)
	activityDB := newPGXActivityStore(t, ctx, apiDSN)
	subjectDB := newPGXSubjectStore(t, ctx, apiDSN)
	subjectService, _ := subjects.NewService(subjectDB, subjects.Config{})
	cipher, _ := integrations.NewAESGCMCipher(bytes.Repeat([]byte{0x45}, 32))
	customService, _ := integrations.NewCustomProviderService(integrationDB, cipher, activityDB, nil, nil)
	connectionService, _ := integrations.NewConnectionService(integrationDB, cipher, nil, nil, activityDB)
	syncService, _ := integrations.NewSyncService(integrationDB, integrationDB, activityDB, cipher, nil, nil, nil)
	operationStore, _ := operationsstore.New(apiDB)
	recorder, _ := operations.NewAuditRecorder(operationStore)
	user := auth.User{ID: userID, PrimaryEmail: email, Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	dependencies := apihttp.Dependencies{
		Auth: fixedDeletionUserAuth{user: user}, SubjectAuthorizer: subjectService, SubjectResolver: subjectService,
		CustomProviders: customService, Connections: connectionService, Sync: syncService,
		Audit: recorder, AuditSourceKey: []byte("custom-atomic-audit-source"), MutationAudits: operationStore,
	}
	router := apihttp.NewRouter(dependencies)

	create := performJSONRequest(t, router, http.MethodPost, "/v1/subjects/"+handle+"/custom-providers", `{"key":"custom_`+suffix+`","name":"Original","allowedActions":["read"]}`, nil)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var created struct {
		Provider struct {
			ID            string `json:"id"`
			EnvironmentID string `json:"environmentId"`
		} `json:"provider"`
		IngestionKey string `json:"ingestionKey"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.Provider.ID == "" || created.IngestionKey == "" {
		t.Fatalf("created payload=%#v err=%v", created, err)
	}
	updatePath := "/v1/subjects/" + handle + "/custom-providers/" + created.Provider.ID
	update := performJSONRequest(t, router, http.MethodPatch, updatePath, `{"name":"Updated"}`, nil)
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	ingestPath := "/v1/custom-providers/" + created.Provider.ID + "/activities:ingest"
	ingestHeaders := map[string]string{"X-Jandibat-Provider-Key": created.IngestionKey, "Idempotency-Key": "success-" + suffix}
	ingestBody := `{"schemaVersion":"1.0","events":[{"eventId":"success-` + suffix + `","date":"` + now.Format(time.DateOnly) + `","action":"read","metric":{"name":"count","value":3}}]}`
	ingest := performJSONRequest(t, router, http.MethodPost, ingestPath, ingestBody, ingestHeaders)
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("ingest status=%d body=%s", ingest.Code, ingest.Body.String())
	}
	var providers, environments, events, facts, successOutboxes, canonicalCreateOutboxes int
	scanCount := func(destination *int, query string, args ...any) {
		t.Helper()
		if err := admin.QueryRowContext(ctx, query, args...).Scan(destination); err != nil {
			t.Fatalf("count query failed: %v; query=%s", err, query)
		}
	}
	queryCounts := func() {
		scanCount(&providers, `SELECT count(*) FROM custom_providers WHERE id=$1`, created.Provider.ID)
		scanCount(&environments, `SELECT count(*) FROM environments WHERE id=$1`, created.Provider.EnvironmentID)
		scanCount(&events, `SELECT count(*) FROM custom_activity_events WHERE custom_provider_id=$1`, created.Provider.ID)
		scanCount(&facts, `SELECT count(*) FROM activity_facts WHERE custom_provider_id=$1`, created.Provider.ID)
		scanCount(&successOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE actor_id IN ($1,$2) AND outcome='succeeded'`, userID, created.Provider.ID)
		scanCount(&canonicalCreateOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.subjects.subject.custom-providers' AND target_type='custom_provider' AND target_id=$1 AND outcome='succeeded'`, created.Provider.ID)
	}
	queryCounts()
	if providers != 1 || environments != 1 || events != 1 || facts != 1 || successOutboxes != 3 || canonicalCreateOutboxes != 1 {
		t.Fatalf("success state providers=%d env=%d events=%d facts=%d outboxes=%d canonical-create=%d", providers, environments, events, facts, successOutboxes, canonicalCreateOutboxes)
	}

	deleteCreate := performJSONRequest(t, router, http.MethodPost, "/v1/subjects/"+handle+"/custom-providers", `{"key":"delete_`+suffix+`","name":"Delete Me","allowedActions":["read"]}`, nil)
	if deleteCreate.Code != http.StatusCreated {
		t.Fatalf("delete fixture create status=%d body=%s", deleteCreate.Code, deleteCreate.Body.String())
	}
	var deleteFixture struct {
		Provider struct {
			ID            string `json:"id"`
			EnvironmentID string `json:"environmentId"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(deleteCreate.Body.Bytes(), &deleteFixture); err != nil || deleteFixture.Provider.ID == "" {
		t.Fatalf("delete fixture payload=%#v err=%v", deleteFixture, err)
	}
	deleted := performJSONRequest(t, router, http.MethodDelete, "/v1/subjects/"+handle+"/custom-providers/"+deleteFixture.Provider.ID, "", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	var deletedProviders, retainedEnvironments, deleteOutboxes int
	scanCount(&deletedProviders, `SELECT count(*) FROM custom_providers WHERE id=$1`, deleteFixture.Provider.ID)
	scanCount(&retainedEnvironments, `SELECT count(*) FROM environments WHERE id=$1 AND owner_subject_id=$2`, deleteFixture.Provider.EnvironmentID, subjectID)
	scanCount(&deleteOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE action='delete.v1.subjects.subject.custom-providers.customProviderId' AND target_id=$1 AND outcome='succeeded'`, deleteFixture.Provider.ID)
	if deletedProviders != 0 || retainedEnvironments != 1 || deleteOutboxes != 1 {
		t.Fatalf("delete state provider=%d retained environment=%d outboxes=%d", deletedProviders, retainedEnvironments, deleteOutboxes)
	}
	otherUser := auth.User{ID: otherUserID, PrimaryEmail: otherEmail, Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	otherDependencies := dependencies
	otherDependencies.Auth = fixedDeletionUserAuth{user: otherUser}
	otherRouter := apihttp.NewRouter(otherDependencies)
	otherCreate := performJSONRequest(t, otherRouter, http.MethodPost, "/v1/subjects/"+otherHandle+"/custom-providers", `{"key":"other_`+suffix+`","name":"Other","allowedActions":["read"]}`, nil)
	if otherCreate.Code != http.StatusCreated {
		t.Fatalf("other create status=%d body=%s", otherCreate.Code, otherCreate.Body.String())
	}
	var otherFixture struct {
		Provider struct {
			ID string `json:"id"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(otherCreate.Body.Bytes(), &otherFixture); err != nil || otherFixture.Provider.ID == "" {
		t.Fatalf("other fixture payload=%#v err=%v", otherFixture, err)
	}
	crossOwnerDelete := performJSONRequest(t, router, http.MethodDelete, "/v1/subjects/"+otherHandle+"/custom-providers/"+otherFixture.Provider.ID, "", nil)
	if crossOwnerDelete.Code != http.StatusForbidden {
		t.Fatalf("cross-owner delete status=%d body=%s", crossOwnerDelete.Code, crossOwnerDelete.Body.String())
	}
	var otherProviders int
	scanCount(&otherProviders, `SELECT count(*) FROM custom_providers WHERE id=$1`, otherFixture.Provider.ID)
	if otherProviders != 1 {
		t.Fatalf("cross-owner delete changed provider count=%d", otherProviders)
	}

	publicConnection := performJSONRequest(t, router, http.MethodPost, "/v1/subjects/"+handle+"/provider-connections", `{"providerId":"github","authMethod":"none","includePrivate":false}`, nil)
	if publicConnection.Code != http.StatusCreated {
		t.Fatalf("public connection status=%d body=%s", publicConnection.Code, publicConnection.Body.String())
	}
	var publicFixture struct {
		Connection struct {
			ID string `json:"id"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(publicConnection.Body.Bytes(), &publicFixture); err != nil || publicFixture.Connection.ID == "" {
		t.Fatalf("public connection payload=%#v err=%v", publicFixture, err)
	}
	syncPath := "/v1/subjects/" + handle + "/provider-connections/" + publicFixture.Connection.ID + "/sync"
	syncHeaders := map[string]string{"Idempotency-Key": "manual-sync-" + suffix}
	firstSync := performJSONRequest(t, router, http.MethodPost, syncPath, `{}`, syncHeaders)
	secondSync := performJSONRequest(t, router, http.MethodPost, syncPath, `{}`, syncHeaders)
	var firstSyncBody, secondSyncBody struct {
		ID string `json:"id"`
	}
	firstDecodeErr := json.Unmarshal(firstSync.Body.Bytes(), &firstSyncBody)
	secondDecodeErr := json.Unmarshal(secondSync.Body.Bytes(), &secondSyncBody)
	if firstSync.Code != http.StatusAccepted || secondSync.Code != http.StatusAccepted || firstDecodeErr != nil || secondDecodeErr != nil || firstSyncBody.ID == "" || firstSyncBody.ID != secondSyncBody.ID {
		t.Fatalf("idempotent sync first=%d/%s second=%d/%s", firstSync.Code, firstSync.Body.String(), secondSync.Code, secondSync.Body.String())
	}
	var syncJobs, syncOutboxes int
	scanCount(&syncJobs, `SELECT count(*) FROM provider_sync_jobs WHERE provider_connection_id=$1`, publicFixture.Connection.ID)
	scanCount(&syncOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.subjects.subject.provider-connections.connectionId.sync' AND target_id=$1 AND outcome='succeeded'`, publicFixture.Connection.ID)
	if syncJobs != 1 || syncOutboxes != 2 {
		t.Fatalf("idempotent sync jobs=%d succeeded outboxes=%d", syncJobs, syncOutboxes)
	}

	failedDependencies := dependencies
	failedDependencies.MutationAudits = failingEnqueueCoordinator{next: operationStore}
	failedRouter := apihttp.NewRouter(failedDependencies)
	failedUpdate := performJSONRequest(t, failedRouter, http.MethodPatch, updatePath, `{"name":"Must Roll Back"}`, nil)
	if failedUpdate.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed update status=%d body=%s", failedUpdate.Code, failedUpdate.Body.String())
	}
	var providerName, environmentName string
	if err := admin.QueryRowContext(ctx, `SELECT name FROM custom_providers WHERE id=$1`, created.Provider.ID).Scan(&providerName); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT name FROM environments WHERE id=$1`, created.Provider.EnvironmentID).Scan(&environmentName); err != nil {
		t.Fatal(err)
	}
	if providerName != "Updated" || environmentName != "Updated" {
		t.Fatalf("failed update escaped rollback provider=%q environment=%q", providerName, environmentName)
	}

	failedCreate := performJSONRequest(t, failedRouter, http.MethodPost, "/v1/subjects/"+handle+"/custom-providers", `{"key":"rollback_`+suffix+`","name":"Rollback","allowedActions":["read"]}`, nil)
	if failedCreate.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed create status=%d body=%s", failedCreate.Code, failedCreate.Body.String())
	}
	var rolledBackProviders, rolledBackEnvironments int
	scanCount(&rolledBackProviders, `SELECT count(*) FROM custom_providers WHERE subject_id=$1 AND slug=$2`, subjectID, "rollback_"+suffix)
	scanCount(&rolledBackEnvironments, `SELECT count(*) FROM environments WHERE owner_subject_id=$1 AND key=$2`, subjectID, "rollback_"+suffix)
	if rolledBackProviders != 0 || rolledBackEnvironments != 0 {
		t.Fatalf("failed create escaped rollback providers=%d environments=%d", rolledBackProviders, rolledBackEnvironments)
	}

	failedIngestHeaders := map[string]string{"X-Jandibat-Provider-Key": created.IngestionKey, "Idempotency-Key": "rollback-" + suffix}
	failedEventID := "rollback-" + suffix
	failedIngestBody := `{"schemaVersion":"1.0","events":[{"eventId":"` + failedEventID + `","date":"` + now.Format(time.DateOnly) + `","action":"read","metric":{"name":"count","value":7}}]}`
	failedIngest := performJSONRequest(t, failedRouter, http.MethodPost, ingestPath, failedIngestBody, failedIngestHeaders)
	if failedIngest.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed ingest status=%d body=%s", failedIngest.Code, failedIngest.Body.String())
	}
	var failedEvents, failedFacts, failedReservations int
	scanCount(&failedEvents, `SELECT count(*) FROM custom_activity_events WHERE custom_provider_id=$1 AND event_id=$2`, created.Provider.ID, failedEventID)
	scanCount(&failedFacts, `SELECT count(*) FROM activity_facts WHERE custom_provider_id=$1 AND metadata->>'provider_event_id'=$2`, created.Provider.ID, failedEventID)
	scanCount(&failedReservations, `SELECT count(*) FROM ingest_idempotency_keys WHERE custom_provider_id=$1 AND request_hash IS NOT NULL AND response_status=0`, created.Provider.ID)
	if failedEvents != 0 || failedFacts != 0 || failedReservations != 0 {
		t.Fatalf("failed ingest escaped rollback events=%d facts=%d reservations=%d", failedEvents, failedFacts, failedReservations)
	}

	connectionBody := `{"providerId":"gitlab","authMethod":"token","token":"unused-public-token","includePrivate":false}`
	failedConnection := performJSONRequest(t, failedRouter, http.MethodPost, "/v1/subjects/"+handle+"/provider-connections", connectionBody, nil)
	if failedConnection.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed connection status=%d body=%s", failedConnection.Code, failedConnection.Body.String())
	}
	var failedConnections, failedConnectionEnvironments int
	scanCount(&failedConnections, `SELECT count(*) FROM provider_connections WHERE subject_id=$1 AND sync_cursor->>'provider_id'='gitlab'`, subjectID)
	scanCount(&failedConnectionEnvironments, `SELECT count(*) FROM environments WHERE owner_subject_id=$1 AND metadata->>'provider_id'='gitlab'`, subjectID)
	if failedConnections != 0 || failedConnectionEnvironments != 0 {
		t.Fatalf("failed connection escaped rollback connections=%d environments=%d", failedConnections, failedConnectionEnvironments)
	}
}

func performJSONRequest(t *testing.T, router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer session-token")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
