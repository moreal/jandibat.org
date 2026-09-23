package cockroach

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestNewAcceptsPGXPoolAndConfiguresRedaction(t *testing.T) {
	pool := new(pgxpool.Pool)
	store, err := New(pool, "private_field")
	if err != nil {
		t.Fatalf("New(pool) error = %v", err)
	}
	if store == nil || store.pool != pool {
		t.Fatalf("New(pool) = %#v; want store using supplied pool", store)
	}
	metadata, err := store.redactor.RedactMetadata(map[string]any{"private_field": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata["private_field"] != operations.RedactedValue {
		t.Fatalf("extra sensitive audit field = %#v; want redacted", metadata)
	}
}

func TestNewRejectsNilPool(t *testing.T) {
	if store, err := New(nil); store != nil || !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) = %#v, %v; want ErrNilDB", store, err)
	}
}

func TestDeletionMethodsRequirePGXPool(t *testing.T) {
	store := &Store{}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	request := operations.DeletionRequest{
		RequestID: "request-1", TargetType: operations.DeletionTargetSubject,
		TargetID: "subject-1", Status: operations.DeletionRequested,
		RequestedAt: now, UpdatedAt: now,
	}
	event := operations.AuditEvent{
		ID: "018f0000-0000-7000-8000-000000000001", OccurredAt: now,
		Actor:  operations.AuditActor{Type: operations.AuditActorSystem},
		Action: "deletion.completed", Target: operations.AuditTarget{Type: "subject", ID: request.TargetID},
		Outcome: operations.AuditSucceeded, RequestID: request.RequestID,
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"create", func() error { _, err := store.CreateOrLoadDeletion(context.Background(), request); return err }},
		{"enqueue", func() error { _, err := store.EnqueueDeletion(context.Background(), request); return err }},
		{"load", func() error { _, err := store.LoadDeletion(context.Background(), request.RequestID); return err }},
		{"claim", func() error {
			_, err := store.ClaimDeletion(context.Background(), request.RequestID, now, now.Add(time.Minute))
			return err
		}},
		{"claim batch", func() error {
			_, err := store.ClaimDeletions(context.Background(), now, now.Add(time.Minute), 1)
			return err
		}},
		{"verify", func() error { _, err := store.VerifyDeletion(context.Background(), request); return err }},
		{"revoke", func() error {
			_, err := store.RevokeDeletionCredentials(context.Background(), request, now)
			return err
		}},
		{"delete", func() error { _, err := store.DeletePrimaryData(context.Background(), request, now); return err }},
		{"complete", func() error {
			_, err := store.CompleteDeletionWithAudit(context.Background(), request, event, now, now.Add(time.Hour))
			return err
		}},
		{"fail", func() error { return store.FailDeletion(context.Background(), request, "failed", now) }},
		{"defer", func() error { return store.DeferDeletionForLegalHold(context.Background(), request, now) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrNilDB) {
				t.Fatalf("error = %v; want PGX pool required", err)
			}
		})
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

func TestAuditMetadataCloneDoesNotMutateInput(t *testing.T) {
	metadata := map[string]any{"safe": "value"}
	cloned := cloneAuditMetadata(metadata)
	cloned["source_ip"] = "127.0.0.1"
	if _, changed := metadata["source_ip"]; changed {
		t.Fatal("metadata was mutated")
	}
}

func TestAuditSinkRedactsAndEncodesSourceIP(t *testing.T) {
	store := &Store{redactor: operations.NewRedactor()}
	event := operations.AuditEvent{
		ID: "018f0000-0000-7000-8000-000000000001", OccurredAt: time.Now(),
		Actor:  operations.AuditActor{Type: operations.AuditActorUser, ID: "user-1"},
		Action: "provider.rotate", Target: operations.AuditTarget{Type: "provider", ID: "provider-1"},
		Outcome: operations.AuditSucceeded, RequestID: "request-1", SourceIP: "127.0.0.1",
		Metadata: map[string]any{"api_key": "do-not-store"},
	}
	_, encoded, err := store.prepareAuditEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["api_key"] != operations.RedactedValue || metadata["source_ip"] != "127.0.0.1" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestRetentionDryRunUsesSameLegalHoldPredicateWithoutDelete(t *testing.T) {
	query, err := buildRetentionCountQuery(operations.RetentionActivityFacts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "DELETE") {
		t.Fatalf("dry run contains DELETE: %s", query)
	}
	for _, fragment := range []string{"SELECT count(*)", "legal_holds", "hold.expires_at > $2", "candidate.subject_id", "held_subject.owner_user_id"} {
		if !strings.Contains(query, fragment) {
			t.Errorf("dry-run query missing %q: %s", fragment, query)
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
	store := &Store{}
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

func TestDeletedIdentityHMACConfigurationIsVersionedAndCanonical(t *testing.T) {
	store := &Store{}
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
