package cockroach

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestEnqueueDeletionExistingTargetParticipatesInLazyAuditTransaction(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 17), Rows: [][]driver.Value{{
			"018f0000-0000-7000-8000-000000000001", "existing-request", "subject", "subject-1",
			"requested", "requested", "", []byte("[]"), now, now, nil, nil, "", int64(0), now, nil, "",
		}}},
		fakedb.Step{Operation: fakedb.Rollback},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	ctx, lazy := appdb.WithLazyTransaction(context.Background(), db)
	got, err := store.EnqueueDeletion(ctx, operations.DeletionRequest{
		RequestID: "new-request", TargetType: operations.DeletionTargetSubject,
		TargetID: "subject-1", Status: operations.DeletionRequested, RequestedAt: now, UpdatedAt: now,
	})
	if err != nil || got.RequestID != "existing-request" {
		t.Fatalf("existing enqueue=%#v err=%v", got, err)
	}
	if _, active := lazy.Transaction(); !active {
		t.Fatal("idempotent deletion success did not join the lazy audit transaction")
	}
	if err := lazy.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsNilDB(t *testing.T) {
	if store, err := New(nil); store != nil || !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) = %#v, %v", store, err)
	}
}

func TestCheckValidatesOperationalSchema(t *testing.T) {
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query, Columns: []string{"required_columns"},
		Rows: [][]driver.Value{{int64(requiredOperationsSchemaColumns)}},
	})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	if err := store.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	for _, fragment := range []string{"audit_events", "api_rate_limit_buckets", "access_token_key_id", "refresh_token_key_id", "provider_token_revocation_jobs", "token_ciphertext", "deleted_identity_tombstones_v2", "identity_key_id", "identity_digest", "magic_link_mail_outbox", "token_hash", "token_expires_at", "consumed_at", "recipient_email", "purpose", "mutation_audit_outbox", "audit_event_id", "delivered_at", "terminal_at"} {
		if !strings.Contains(calls[0].Query, fragment) {
			t.Errorf("schema check missing %q: %s", fragment, calls[0].Query)
		}
	}
	for _, forbidden := range []string{"FROM audit_events", "FROM api_rate_limit_buckets", "FROM provider_connections"} {
		if strings.Contains(calls[0].Query, forbidden) {
			t.Errorf("schema check requires table data SELECT via %q: %s", forbidden, calls[0].Query)
		}
	}
}

func TestCheckRejectsIncompleteOperationalSchema(t *testing.T) {
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query, Columns: []string{"required_columns"},
		Rows: [][]driver.Value{{int64(requiredOperationsSchemaColumns - 1)}},
	})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	if err := store.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "required columns") {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestRetentionQueriesAreBoundedAndAllowlisted(t *testing.T) {
	wantFilters := map[operations.RetentionDataset][]string{
		operations.RetentionAuditEvents:             nil,
		operations.RetentionActivityFacts:           nil,
		operations.RetentionCustomActivities:        nil,
		operations.RetentionRevokedProviderMetadata: {"DELETE FROM provider_connections", "updated_at < $1", "status = 'revoked'"},
		operations.RetentionOrphanedProviderEnvironments: {
			"DELETE FROM environments", "updated_at < $1", "candidate.scope = 'subject'", "candidate.owner_subject_id IS NOT NULL",
			"candidate.id LIKE 'connection:%'", "candidate.id LIKE 'custom-provider:%'",
			"provider_connections.environment_id = candidate.id", "custom_providers.environment_id = candidate.id",
			"provider_sync_jobs.environment_id = candidate.id", "custom_activity_events.environment_id = candidate.id",
			"activity_facts.environment_id = candidate.id", "activity_refresh_cache.environment_id = candidate.id",
		},
		operations.RetentionSuccessfulSyncJobs:          {"DELETE FROM provider_sync_jobs", "finished_at < $1", "status = 'succeeded'"},
		operations.RetentionFailedSyncJobs:              {"DELETE FROM provider_sync_jobs", "finished_at < $1", "status IN ('failed', 'cancelled')"},
		operations.RetentionSessions:                    nil,
		operations.RetentionMagicLinks:                  nil,
		operations.RetentionMagicLinkMailOutbox:         nil,
		operations.RetentionMutationAuditOutbox:         nil,
		operations.RetentionAuthChallenges:              nil,
		operations.RetentionIdempotencyKeys:             nil,
		operations.RetentionTimelineCache:               nil,
		operations.RetentionActivityRefresh:             nil,
		operations.RetentionRateLimitBuckets:            nil,
		operations.RetentionDeletedIdentityTombstones:   nil,
		operations.RetentionDeletedIdentityTombstonesV2: nil,
		operations.RetentionDeletionRequests:            nil,
		operations.RetentionDeletionRequestInbox:        nil,
	}
	for dataset := range retentionSQLSpecs {
		query, err := buildRetentionPurgeQuery(dataset)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{"DELETE FROM", " < $1", "ORDER BY", "LIMIT $2"} {
			if !strings.Contains(query, fragment) {
				t.Errorf("query for %s missing %q: %s", dataset, fragment, query)
			}
		}
		for _, fragment := range wantFilters[dataset] {
			if !strings.Contains(query, fragment) {
				t.Errorf("query for %s missing policy filter %q: %s", dataset, fragment, query)
			}
		}
	}
	if _, err := buildRetentionPurgeQuery("unknown"); !errors.Is(err, ErrInvalidPurgeRequest) {
		t.Fatalf("buildRetentionPurgeQuery(unknown) error = %v", err)
	}
	orphanQuery, err := buildRetentionPurgeQuery(operations.RetentionOrphanedProviderEnvironments)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(orphanQuery, "candidate.id LIKE 'custom:%'") {
		t.Fatalf("orphan cleanup includes untrusted legacy custom namespace: %s", orphanQuery)
	}
	for _, dataset := range []operations.RetentionDataset{
		operations.RetentionSuccessfulSyncJobs,
		operations.RetentionFailedSyncJobs,
	} {
		jobQuery, _ := buildRetentionPurgeQuery(dataset)
		if strings.Contains(jobQuery, "'queued'") || strings.Contains(jobQuery, "'running'") {
			t.Fatalf("sync job purge can delete active jobs: %s", jobQuery)
		}
	}
}

func FuzzRetentionRejectsUnlistedDatasetBeforeExecution(f *testing.F) {
	for _, value := range []string{
		"", "unknown", "audit_events; DELETE FROM users", "audit_events --", "audit_events/*x*/",
		"public.audit_events", " audit_events", "audit_events ", "audit_events\n", "ＡＵＤＩＴ", "' OR true --",
	} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		dataset := operations.RetentionDataset(value)
		if _, allowed := retentionSQLSpecs[dataset]; allowed {
			return
		}
		if _, err := buildRetentionPurgeQuery(dataset); !errors.Is(err, ErrInvalidPurgeRequest) {
			t.Fatalf("purge query accepted %q: %v", value, err)
		}
		if _, err := buildRetentionCountQuery(dataset); !errors.Is(err, ErrInvalidPurgeRequest) {
			t.Fatalf("count query accepted %q: %v", value, err)
		}
		store := &Store{} // any query execution would fail; rejection must happen first
		request := operations.RetentionPurgeRequest{Dataset: dataset, Before: time.Unix(1, 0), AsOf: time.Unix(2, 0), Limit: 1}
		if _, err := store.PurgeExpired(context.Background(), request); !errors.Is(err, ErrInvalidPurgeRequest) {
			t.Fatalf("purge executed unlisted dataset %q: %v", value, err)
		}
		if _, err := store.CountExpired(context.Background(), request); !errors.Is(err, ErrInvalidPurgeRequest) {
			t.Fatalf("count executed unlisted dataset %q: %v", value, err)
		}
	})
}

func TestAuditQueryMatchesExistingSchema(t *testing.T) {
	for _, fragment := range []string{"INSERT INTO audit_events", "actor_type", "target_type", "request_id", "$10::JSONB"} {
		if !strings.Contains(insertAuditEventQuery, fragment) {
			t.Errorf("audit query missing %q", fragment)
		}
	}
	metadata := map[string]any{"safe": "value"}
	cloned := cloneAuditMetadata(metadata)
	cloned["source_ip"] = "127.0.0.1"
	if _, changed := metadata["source_ip"]; changed {
		t.Fatal("metadata was mutated")
	}
}

func TestAuditSinkRedactsAndPersistsSourceIP(t *testing.T) {
	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 1})
	db := script.Open()
	defer db.Close()
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	event := operations.AuditEvent{
		ID: "018f0000-0000-7000-8000-000000000001", OccurredAt: time.Now(),
		Actor:  operations.AuditActor{Type: operations.AuditActorUser, ID: "user-1"},
		Action: "provider.rotate", Target: operations.AuditTarget{Type: "provider", ID: "provider-1"},
		Outcome: operations.AuditSucceeded, RequestID: "request-1", SourceIP: "127.0.0.1",
		Metadata: map[string]any{"api_key": "do-not-store"},
	}
	if err := store.WriteAuditEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 1 || calls[0].Operation != fakedb.Exec || len(calls[0].Args) != 10 {
		t.Fatalf("calls = %#v", calls)
	}
	encoded, ok := calls[0].Args[9].Value.([]byte)
	if !ok {
		t.Fatalf("metadata argument = %T", calls[0].Args[9].Value)
	}
	var metadata map[string]any
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["api_key"] != operations.RedactedValue || metadata["source_ip"] != "127.0.0.1" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestCockroachRetentionPort(t *testing.T) {
	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 2})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	deleted, err := store.PurgeExpired(context.Background(), operations.RetentionPurgeRequest{
		Dataset: operations.RetentionAuditEvents, Before: time.Now(), Limit: 100,
	})
	if err != nil || deleted != 2 {
		t.Fatalf("PurgeExpired() = %d, %v", deleted, err)
	}
}

func TestRetentionDryRunUsesSameLegalHoldPredicateWithoutDelete(t *testing.T) {
	script := fakedb.New(fakedb.Step{Operation: fakedb.Query, Columns: []string{"count"}, Rows: [][]driver.Value{{int64(3)}}})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	asOf := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	count, err := store.CountExpired(context.Background(), operations.RetentionPurgeRequest{
		Dataset: operations.RetentionActivityFacts, Before: asOf.Add(-400 * 24 * time.Hour), AsOf: asOf,
	})
	if err != nil || count != 3 {
		t.Fatalf("CountExpired() = %d, %v", count, err)
	}
	calls := script.Calls()
	if len(calls) != 1 || calls[0].Operation != fakedb.Query || strings.Contains(calls[0].Query, "DELETE") {
		t.Fatalf("dry run calls = %#v", calls)
	}
	for _, fragment := range []string{"SELECT count(*)", "legal_holds", "hold.expires_at > $2", "candidate.subject_id", "held_subject.owner_user_id"} {
		if !strings.Contains(calls[0].Query, fragment) {
			t.Errorf("dry-run query missing %q: %s", fragment, calls[0].Query)
		}
	}
}

func TestMagicLinkRetentionMapsEmailOnlyTokenToAccountLegalHold(t *testing.T) {
	query, err := buildRetentionCountQuery(operations.RetentionMagicLinks)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"candidate.user_id", "account.primary_email = candidate.email", "hold.target_type = 'account'", "hold.expires_at > $2"} {
		if !strings.Contains(query, fragment) {
			t.Errorf("magic-link legal hold query missing %q: %s", fragment, query)
		}
	}
}

func TestMagicLinkMailOutboxRetentionIsTerminalOnlyAndMapsAccountLegalHold(t *testing.T) {
	query, err := buildRetentionCountQuery(operations.RetentionMagicLinkMailOutbox)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"candidate.status IN ('sent', 'dead', 'superseded')",
		"CASE WHEN candidate.status = 'sent' THEN candidate.updated_at ELSE candidate.terminal_at END < $1",
		"account.primary_email = candidate.recipient_email",
		"hold.target_type = 'account'", "hold.expires_at > $2",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("magic-link mail outbox retention query missing %q: %s", fragment, query)
		}
	}
	for _, forbidden := range []string{"status = 'pending'", "status = 'processing'"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("magic-link mail outbox retention includes active status via %q: %s", forbidden, query)
		}
	}
}

func TestMutationAuditOutboxRetentionIsTerminalOnlyAndMapsLegalHolds(t *testing.T) {
	query, err := buildRetentionCountQuery(operations.RetentionMutationAuditOutbox)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"candidate.status IN ('delivered', 'dead')",
		"COALESCE(candidate.delivered_at, candidate.terminal_at) < $1",
		"hold.target_type = 'audit_event' AND hold.target_id = candidate.audit_event_id::STRING",
		"candidate.target_type = 'subject'", "candidate.target_type IN ('account', 'user')",
		"candidate.actor_type = 'user'", "hold.expires_at > $2",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("mutation audit outbox retention query missing %q: %s", fragment, query)
		}
	}
	for _, forbidden := range []string{"status = 'pending'", "status = 'processing'"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("mutation audit outbox retention includes active status via %q: %s", forbidden, query)
		}
	}
}

func TestDeletedIdentityTombstoneRetentionMapsBothVersionsToAccountLegalHold(t *testing.T) {
	for _, dataset := range []operations.RetentionDataset{
		operations.RetentionDeletedIdentityTombstones,
		operations.RetentionDeletedIdentityTombstonesV2,
	} {
		query, err := buildRetentionCountQuery(dataset)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{"deletion_requests", "candidate.deletion_request_id", "request.target_type = 'account'", "hold.target_type = 'account'"} {
			if !strings.Contains(query, fragment) {
				t.Errorf("%s retention legal hold query missing %q: %s", dataset, fragment, query)
			}
		}
	}
}

func TestAuditRetentionMapsEventSubjectAndAccountLegalHolds(t *testing.T) {
	query, err := buildRetentionCountQuery(operations.RetentionAuditEvents)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"hold.target_type = 'audit_event'", "candidate.id::STRING", "hold.target_type = 'subject'",
		"candidate.target_type = 'subject'", "hold.target_type = 'account'", "candidate.actor_type = 'user'",
		"candidate.target_type IN ('account', 'user')",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("audit legal hold query missing %q: %s", fragment, query)
		}
	}
}

func TestCheckpointRequiresPGXPool(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"operation", "scope", "payload", "updated_at"}, Rows: [][]driver.Value{{
			string(operations.MaintenanceRetention), "release-1", []byte(`{"rule_index":1}`), now,
		}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
	)
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	checkpoint := operations.MaintenanceCheckpoint{
		Operation: operations.MaintenanceRetention, Scope: "release-1", Payload: []byte(`{"rule_index":1}`), UpdatedAt: now,
	}
	if err := store.SaveCheckpoint(context.Background(), checkpoint); !errors.Is(err, errCheckpointPGXPoolRequired) {
		t.Errorf("SaveCheckpoint() error = %v; want pgx pool required", err)
	}
	loaded, found, err := store.LoadCheckpoint(context.Background(), checkpoint.Operation, checkpoint.Scope)
	if !errors.Is(err, errCheckpointPGXPoolRequired) {
		t.Errorf("LoadCheckpoint() = %#v, %t, %v; want pgx pool required", loaded, found, err)
	}
	if err := store.DeleteCheckpoint(context.Background(), checkpoint.Operation, checkpoint.Scope); !errors.Is(err, errCheckpointPGXPoolRequired) {
		t.Errorf("DeleteCheckpoint() error = %v; want pgx pool required", err)
	}
}

func TestDeletionSQLHasLegalHoldStagesRevocationAndTenResidualChecks(t *testing.T) {
	contents := []string{
		"hold.expires_at > $3", "hold.target_type = 'subject'", "hold.target_type = 'account'",
		"status = 'deletion_pending'", "INSERT INTO provider_token_revocation_jobs", "ON CONFLICT (connection_id) DO NOTHING",
		"INSERT INTO deleted_identity_tombstones_v2", "GREATEST(deleted_identity_tombstones_v2.expires_at",
		"connection.sync_cursor->>'provider_id'", "<> ''",
		"DELETE FROM provider_sync_jobs", "DELETE FROM activity_facts", "DELETE FROM provider_connections",
		"DELETE FROM custom_providers", "DELETE FROM environments", "DELETE FROM subjects", "DELETE FROM users",
	}
	combined := deletionAdapterSQLForTest()
	for _, fragment := range contents {
		if !strings.Contains(combined, fragment) {
			t.Errorf("deletion adapter missing %q", fragment)
		}
	}
	if got := strings.Count(deletionVerificationQueryForTest(), "SELECT count(*)"); got != 12 {
		t.Fatalf("verification residual count queries = %d, want 12", got)
	}
}

func TestDeletedIdentityHMACConfigurationIsVersionedAndCanonical(t *testing.T) {
	script := fakedb.New()
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	if err := store.ConfigureDeletedIdentityHMAC("", make([]byte, 32)); !errors.Is(err, ErrInvalidDeletedIdentityHMAC) {
		t.Fatalf("empty active key ID error = %v", err)
	}
	if err := store.ConfigureDeletedIdentityHMAC("identity-v2", []byte("short")); !errors.Is(err, ErrInvalidDeletedIdentityHMAC) {
		t.Fatalf("short key error = %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	if err := store.ConfigureDeletedIdentityHMAC("identity-v2", key); err != nil {
		t.Fatal(err)
	}
	keyID, got, err := store.deletedIdentityHMAC("  User@Example.Invalid ")
	if err != nil || keyID != "identity-v2" {
		t.Fatalf("deletedIdentityHMAC() key=%q error=%v", keyID, err)
	}
	want := hmac.New(sha256.New, key)
	_, _ = want.Write([]byte("user@example.invalid"))
	if !hmac.Equal(got, want.Sum(nil)) {
		t.Fatalf("digest = %x, want %x", got, want.Sum(nil))
	}
	store.ClearDeletedIdentityHMAC()
	if _, _, err := store.deletedIdentityHMAC("user@example.invalid"); !errors.Is(err, ErrInvalidDeletedIdentityHMAC) {
		t.Fatalf("cleared key error = %v", err)
	}
}

func TestCreateOrLoadDeletionRejectsRequestIDTargetMismatch(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{
			"id", "request_id", "target_type", "target_id", "status", "stage", "error", "subjects",
			"requested", "updated", "completed", "backup", "audit", "attempts", "available", "lease", "claim",
		}, Rows: [][]driver.Value{{
			"018f0000-0000-7000-8000-000000000001", "request-1", "subject", "subject-original",
			"requested", "requested", "", []byte(`[]`), now, now, nil, nil, "", int64(0), now, nil, "",
		}}},
	)
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	_, err := store.CreateOrLoadDeletion(context.Background(), operations.DeletionRequest{
		RequestID: "request-1", TargetType: operations.DeletionTargetSubject, TargetID: "subject-other",
		Status: operations.DeletionRequested, RequestedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, operations.ErrInvalidDeletionRequest) {
		t.Fatalf("CreateOrLoadDeletion mismatch error = %v", err)
	}
}
