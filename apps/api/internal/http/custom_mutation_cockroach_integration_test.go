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

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
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

	pool, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	integrationDB, err := integrationstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	activityDB, err := activitystore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	subjectDB, err := subjectstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	subjectService, _ := subjects.NewService(subjectDB, subjects.Config{})
	cipher, _ := integrations.NewAESGCMCipher(bytes.Repeat([]byte{0x45}, 32))
	customService, _ := integrations.NewCustomProviderService(integrationDB, cipher, activityDB, nil, nil)
	connectionService, _ := integrations.NewConnectionService(integrationDB, cipher, nil, nil, activityDB)
	syncService, _ := integrations.NewSyncService(integrationDB, integrationDB, activityDB, cipher, nil, nil, nil)
	operationStore, err := operationsstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := operations.NewAuditRecorder(operationStore)
	user := auth.User{ID: userID, PrimaryEmail: email, Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	dependencies := apihttp.Dependencies{
		Auth: fixedDeletionUserAuth{user: user}, Sessions: fixedDeletionSession{userID: userID},
		SubjectAuthorizer: subjectService, SubjectResolver: subjectService,
		CustomProviders: customService, Connections: connectionService,
		Audit: recorder, AuditSourceKey: []byte("custom-atomic-audit-source"), MutationAudits: operationStore,
		RateLimiter: handlers.DefaultRateLimiter(),
	}
	graphDeps := cockroachGraphQLDependencies(subjectService, connectionService, customService, syncService, nil, fixedDeletionSession{userID: userID})
	router, err := apihttp.NewRouterWithGraphQL(dependencies, graphDeps)
	if err != nil {
		t.Fatal(err)
	}
	subjectGlobalID := relayid.Encode(relayid.Subject, subjectID)
	createMutation := `mutation Create($input: CreateCustomProviderInput!) { createCustomProvider(input: $input) { errors { code } provider { id ingestProviderID environmentID } ingestionKey } }`
	createProvider := func(handler http.Handler, ownerSubjectID, slug, name string) (string, string, string, string, int) {
		t.Helper()
		response := performGraphQLIntegrationRequest(t, handler, createMutation, map[string]any{"input": map[string]any{
			"subjectID": ownerSubjectID, "slug": slug, "name": name, "allowedActions": []string{"read"},
		}})
		if response.Code != http.StatusOK {
			return "", "", "", "", response.Code
		}
		var payload struct {
			Data struct {
				CreateCustomProvider struct {
					Errors []struct {
						Code string `json:"code"`
					} `json:"errors"`
					Provider struct {
						ID               string `json:"id"`
						IngestProviderID string `json:"ingestProviderID"`
						EnvironmentID    string `json:"environmentID"`
					} `json:"provider"`
					IngestionKey string `json:"ingestionKey"`
				} `json:"createCustomProvider"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode custom provider mutation: %v", err)
		}
		result := payload.Data.CreateCustomProvider
		if len(result.Errors) != 0 || result.Provider.ID == "" || result.Provider.IngestProviderID == "" || result.IngestionKey == "" {
			t.Fatalf("create provider did not return an owner-scoped provider and one-time key: errors=%v hasID=%t hasIngestID=%t hasKey=%t", result.Errors, result.Provider.ID != "", result.Provider.IngestProviderID != "", result.IngestionKey != "")
		}
		decodedID, err := relayid.DecodeAs(relayid.CustomProvider, result.Provider.ID)
		if err != nil || decodedID != result.Provider.IngestProviderID {
			t.Fatalf("custom provider Relay and edge identity mismatch: %v", err)
		}
		return result.Provider.ID, result.Provider.IngestProviderID, result.Provider.EnvironmentID, result.IngestionKey, response.Code
	}

	createdID, createdRawID, createdEnvironmentID, createdKey, createStatus := createProvider(router, subjectGlobalID, "custom_"+suffix, "Original")
	if createStatus != http.StatusOK {
		t.Fatalf("create status=%d", createStatus)
	}
	var canonicalCreateOutboxes int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.graphql' AND target_type='custom_provider' AND target_id=$1 AND actor_id=$2 AND outcome='succeeded'`, createdRawID, userID).Scan(&canonicalCreateOutboxes); err != nil || canonicalCreateOutboxes != 1 {
		t.Fatalf("custom provider create canonical audit target count=%d err=%v", canonicalCreateOutboxes, err)
	}
	updateMutation := `mutation Update($input: UpdateCustomProviderInput!) { updateCustomProvider(input: $input) { errors { code } provider { id name } } }`
	update := performGraphQLIntegrationRequest(t, router, updateMutation, map[string]any{"input": map[string]any{"id": createdID, "name": "Updated"}})
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"name":"Updated"`) {
		t.Fatalf("update status=%d", update.Code)
	}
	ingestPath := "/v1/custom-providers/" + createdRawID + "/activities:ingest"
	// The ingest key is an HTTP edge credential, not a Relay Node field.
	ingestHeaders := map[string]string{"X-Jandibat-Provider-Key": createdKey, "Idempotency-Key": "success-" + suffix}
	ingestBody := `{"schemaVersion":"1.0","events":[{"eventId":"success-` + suffix + `","date":"` + now.Format(time.DateOnly) + `","action":"read","metric":{"name":"count","value":3}}]}`
	ingest := performJSONRequest(t, router, http.MethodPost, ingestPath, ingestBody, ingestHeaders)
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("ingest status=%d body=%s", ingest.Code, ingest.Body.String())
	}
	var providers, environments, events, facts, successOutboxes int
	scanCount := func(destination *int, query string, args ...any) {
		t.Helper()
		if err := admin.QueryRowContext(ctx, query, args...).Scan(destination); err != nil {
			t.Fatalf("count query failed: %v; query=%s", err, query)
		}
	}
	queryCounts := func() {
		scanCount(&providers, `SELECT count(*) FROM custom_providers WHERE id=$1`, createdRawID)
		scanCount(&environments, `SELECT count(*) FROM environments WHERE id=$1`, createdEnvironmentID)
		scanCount(&events, `SELECT count(*) FROM custom_activity_events WHERE custom_provider_id=$1`, createdRawID)
		scanCount(&facts, `SELECT count(*) FROM activity_facts WHERE custom_provider_id=$1`, createdRawID)
		scanCount(&successOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE actor_id IN ($1,$2) AND outcome='succeeded'`, userID, createdRawID)
	}
	queryCounts()
	if providers != 1 || environments != 1 || events != 1 || facts != 1 || successOutboxes != 3 {
		t.Fatalf("success state providers=%d env=%d events=%d facts=%d outboxes=%d", providers, environments, events, facts, successOutboxes)
	}

	deleteGlobalID, deleteRawID, deleteEnvironmentID, _, deleteCreateStatus := createProvider(router, subjectGlobalID, "delete_"+suffix, "Delete Me")
	if deleteCreateStatus != http.StatusOK {
		t.Fatalf("delete fixture create status=%d", deleteCreateStatus)
	}
	deleteMutation := `mutation Delete($input: DeleteCustomProviderInput!) { deleteCustomProvider(input: $input) { errors { code } deletedProviderID } }`
	deleted := performGraphQLIntegrationRequest(t, router, deleteMutation, map[string]any{"input": map[string]any{"id": deleteGlobalID}})
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deletedProviderID":"`+deleteGlobalID+`"`) {
		t.Fatalf("delete status=%d", deleted.Code)
	}
	var deletedProviders, retainedEnvironments, deleteOutboxes int
	scanCount(&deletedProviders, `SELECT count(*) FROM custom_providers WHERE id=$1`, deleteRawID)
	scanCount(&retainedEnvironments, `SELECT count(*) FROM environments WHERE id=$1 AND owner_subject_id=$2`, deleteEnvironmentID, subjectID)
	scanCount(&deleteOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.graphql' AND actor_id=$1 AND outcome='succeeded'`, userID)
	if deletedProviders != 0 || retainedEnvironments != 1 || deleteOutboxes != 4 {
		t.Fatalf("delete state provider=%d retained environment=%d outboxes=%d", deletedProviders, retainedEnvironments, deleteOutboxes)
	}
	otherUser := auth.User{ID: otherUserID, PrimaryEmail: otherEmail, Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	otherDependencies := dependencies
	otherDependencies.Auth = fixedDeletionUserAuth{user: otherUser}
	otherDependencies.Sessions = fixedDeletionSession{userID: otherUserID}
	otherGraphDeps := cockroachGraphQLDependencies(subjectService, connectionService, customService, syncService, nil, fixedDeletionSession{userID: otherUserID})
	otherRouter, err := apihttp.NewRouterWithGraphQL(otherDependencies, otherGraphDeps)
	if err != nil {
		t.Fatal(err)
	}
	otherGlobalID, otherRawID, _, _, otherCreateStatus := createProvider(otherRouter, relayid.Encode(relayid.Subject, otherSubjectID), "other_"+suffix, "Other")
	if otherCreateStatus != http.StatusOK {
		t.Fatalf("other create status=%d", otherCreateStatus)
	}
	crossOwnerDelete := performGraphQLIntegrationRequest(t, router, deleteMutation, map[string]any{"input": map[string]any{"id": otherGlobalID}})
	if crossOwnerDelete.Code != http.StatusOK || !strings.Contains(crossOwnerDelete.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("cross-owner delete status=%d", crossOwnerDelete.Code)
	}
	var otherProviders int
	scanCount(&otherProviders, `SELECT count(*) FROM custom_providers WHERE id=$1`, otherRawID)
	if otherProviders != 1 {
		t.Fatalf("cross-owner delete changed provider count=%d", otherProviders)
	}

	connectMutation := `mutation Connect($input: ConnectProviderInput!) { connectProvider(input: $input) { errors { code } connection { id providerID } } }`
	publicConnection := performGraphQLIntegrationRequest(t, router, connectMutation, map[string]any{"input": map[string]any{
		"subjectID": subjectGlobalID, "providerID": "github", "authMethod": "PUBLIC", "includePrivate": false,
	}})
	if publicConnection.Code != http.StatusOK {
		t.Fatalf("public connection status=%d", publicConnection.Code)
	}
	var publicFixture struct {
		Data struct {
			ConnectProvider struct {
				Connection struct {
					ID string `json:"id"`
				} `json:"connection"`
			} `json:"connectProvider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(publicConnection.Body.Bytes(), &publicFixture); err != nil || publicFixture.Data.ConnectProvider.Connection.ID == "" {
		t.Fatalf("public connection payload missing id; decode error=%v", err)
	}
	connectionGlobalID := publicFixture.Data.ConnectProvider.Connection.ID
	connectionRawID, err := relayid.DecodeAs(relayid.ProviderConnection, connectionGlobalID)
	if err != nil {
		t.Fatal(err)
	}
	syncMutation := `mutation Sync($input: EnqueueManualSyncInput!) { enqueueManualSync(input: $input) { errors { code } job { id } } }`
	syncVariables := map[string]any{"input": map[string]any{"connectionID": connectionGlobalID, "idempotencyKey": "manual-sync-" + suffix}}
	firstSync := performGraphQLIntegrationRequest(t, router, syncMutation, syncVariables)
	secondSync := performGraphQLIntegrationRequest(t, router, syncMutation, syncVariables)
	var firstSyncBody, secondSyncBody struct {
		Data struct {
			EnqueueManualSync struct {
				Job struct {
					ID string `json:"id"`
				} `json:"job"`
			} `json:"enqueueManualSync"`
		} `json:"data"`
	}
	firstDecodeErr := json.Unmarshal(firstSync.Body.Bytes(), &firstSyncBody)
	secondDecodeErr := json.Unmarshal(secondSync.Body.Bytes(), &secondSyncBody)
	firstJobID := firstSyncBody.Data.EnqueueManualSync.Job.ID
	secondJobID := secondSyncBody.Data.EnqueueManualSync.Job.ID
	if firstSync.Code != http.StatusOK || secondSync.Code != http.StatusOK || firstDecodeErr != nil || secondDecodeErr != nil || firstJobID == "" || firstJobID != secondJobID {
		t.Fatalf("idempotent sync first=%d second=%d firstID=%q secondID=%q decode=(%v,%v)", firstSync.Code, secondSync.Code, firstJobID, secondJobID, firstDecodeErr, secondDecodeErr)
	}
	var syncJobs, syncOutboxes int
	scanCount(&syncJobs, `SELECT count(*) FROM provider_sync_jobs WHERE provider_connection_id=$1`, connectionRawID)
	scanCount(&syncOutboxes, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.graphql' AND actor_id=$1 AND outcome='succeeded'`, userID)
	if syncJobs != 1 || syncOutboxes != 7 {
		t.Fatalf("idempotent sync jobs=%d succeeded outboxes=%d", syncJobs, syncOutboxes)
	}

	failedDependencies := dependencies
	failedDependencies.MutationAudits = failingEnqueueCoordinator{next: operationStore}
	failedRouter, err := apihttp.NewRouterWithGraphQL(failedDependencies, graphDeps)
	if err != nil {
		t.Fatal(err)
	}
	failedUpdate := performGraphQLIntegrationRequest(t, failedRouter, updateMutation, map[string]any{"input": map[string]any{"id": createdID, "name": "Must Roll Back"}})
	if failedUpdate.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed update status=%d body=%s", failedUpdate.Code, failedUpdate.Body.String())
	}
	var providerName, environmentName string
	if err := admin.QueryRowContext(ctx, `SELECT name FROM custom_providers WHERE id=$1`, createdRawID).Scan(&providerName); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT name FROM environments WHERE id=$1`, createdEnvironmentID).Scan(&environmentName); err != nil {
		t.Fatal(err)
	}
	if providerName != "Updated" || environmentName != "Updated" {
		t.Fatalf("failed update escaped rollback provider=%q environment=%q", providerName, environmentName)
	}

	failedCreate := performGraphQLIntegrationRequest(t, failedRouter, createMutation, map[string]any{"input": map[string]any{
		"subjectID": subjectGlobalID, "slug": "rollback_" + suffix, "name": "Rollback", "allowedActions": []string{"read"},
	}})
	if failedCreate.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed create status=%d body=%s", failedCreate.Code, failedCreate.Body.String())
	}
	var rolledBackProviders, rolledBackEnvironments int
	scanCount(&rolledBackProviders, `SELECT count(*) FROM custom_providers WHERE subject_id=$1 AND slug=$2`, subjectID, "rollback_"+suffix)
	scanCount(&rolledBackEnvironments, `SELECT count(*) FROM environments WHERE owner_subject_id=$1 AND key=$2`, subjectID, "rollback_"+suffix)
	if rolledBackProviders != 0 || rolledBackEnvironments != 0 {
		t.Fatalf("failed create escaped rollback providers=%d environments=%d", rolledBackProviders, rolledBackEnvironments)
	}

	failedIngestHeaders := map[string]string{"X-Jandibat-Provider-Key": createdKey, "Idempotency-Key": "rollback-" + suffix}
	failedEventID := "rollback-" + suffix
	failedIngestBody := `{"schemaVersion":"1.0","events":[{"eventId":"` + failedEventID + `","date":"` + now.Format(time.DateOnly) + `","action":"read","metric":{"name":"count","value":7}}]}`
	failedIngest := performJSONRequest(t, failedRouter, http.MethodPost, ingestPath, failedIngestBody, failedIngestHeaders)
	if failedIngest.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed ingest status=%d body=%s", failedIngest.Code, failedIngest.Body.String())
	}
	var failedEvents, failedFacts, failedReservations int
	scanCount(&failedEvents, `SELECT count(*) FROM custom_activity_events WHERE custom_provider_id=$1 AND event_id=$2`, createdRawID, failedEventID)
	scanCount(&failedFacts, `SELECT count(*) FROM activity_facts WHERE custom_provider_id=$1 AND metadata->>'provider_event_id'=$2`, createdRawID, failedEventID)
	scanCount(&failedReservations, `SELECT count(*) FROM ingest_idempotency_keys WHERE custom_provider_id=$1 AND request_hash IS NOT NULL AND response_status=0`, createdRawID)
	if failedEvents != 0 || failedFacts != 0 || failedReservations != 0 {
		t.Fatalf("failed ingest escaped rollback events=%d facts=%d reservations=%d", failedEvents, failedFacts, failedReservations)
	}

	failedConnection := performGraphQLIntegrationRequest(t, failedRouter, connectMutation, map[string]any{"input": map[string]any{
		"subjectID": subjectGlobalID, "providerID": "gitlab", "authMethod": "TOKEN", "token": "unused-public-token", "includePrivate": false,
	}})
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
