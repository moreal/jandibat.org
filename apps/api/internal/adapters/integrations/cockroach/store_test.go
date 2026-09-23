package cockroach

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestNewRejectsNilDatabase(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) error = %v, want %v", err, ErrNilDB)
	}
	if store != nil {
		t.Fatal("New(nil) returned non-nil store")
	}
}

func TestPersistenceErrorMapsMalformedUUIDToInvalidIdentifier(t *testing.T) {
	err := persistenceError(&pgconn.PgError{Code: "22P02"}, nil)
	if !errors.Is(err, integrations.ErrInvalidIdentifier) {
		t.Fatalf("persistenceError(22P02) = %v", err)
	}
}

func TestDeleteCustomProviderAggregatePurgesStateButRetainsEnvironmentForMaintenance(t *testing.T) {
	id := "018f0000-0000-7000-8000-000000000001"
	environmentID := "custom-provider:" + id
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"subject_id", "environment_id"}, Rows: [][]driver.Value{{"subject-1", environmentID}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 2},
		fakedb.Step{Operation: fakedb.Exec, Affected: 3},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	if err := store.DeleteCustomProviderAggregate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	want := []string{"FOR UPDATE", "DELETE FROM activity_refresh_cache", "DELETE FROM activity_facts", "DELETE FROM custom_providers"}
	if len(calls) != 6 {
		t.Fatalf("calls = %#v", calls)
	}
	for index, fragment := range want {
		if !strings.Contains(calls[index+1].Query, fragment) {
			t.Fatalf("call %d missing %q: %s", index+1, fragment, calls[index+1].Query)
		}
	}
	for _, call := range calls {
		if strings.Contains(call.Query, "DELETE FROM environments") {
			t.Fatalf("API aggregate attempted privileged environment cleanup: %s", call.Query)
		}
	}
}

func TestScanConnection(t *testing.T) {
	created := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	expires := created.Add(time.Hour)
	row := valueScanner{values: []any{
		"018f0000-0000-7000-8000-000000000001", "subject-1", "github", "github-env",
		"oauth2", "account-1", "octocat", "pending", []byte(`["repo","user"]`),
		[]byte{1, 2, 3}, []byte{4, 5, 6}, sql.NullTime{Time: expires, Valid: true},
		sql.NullTime{}, sql.NullTime{}, sql.NullTime{}, int64(0), int64(0), "", created, created, true,
	}}
	got, err := scanConnection(row)
	if err != nil {
		t.Fatalf("scanConnection() error = %v", err)
	}
	if got.Connection.ProviderID != "github" || got.Connection.Status != integrations.ConnectionPending || got.Connection.ExternalAccountLogin != "octocat" {
		t.Fatalf("scanConnection() identity/state = %#v", got.Connection)
	}
	if !got.Connection.PrivateDataEnabled {
		t.Fatal("private data consent was not restored")
	}
	if !reflect.DeepEqual(got.Connection.Scopes, []string{"repo", "user"}) {
		t.Fatalf("scopes = %#v", got.Connection.Scopes)
	}
	if got.Connection.TokenExpiresAt == nil || !got.Connection.TokenExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %v, want %v", got.Connection.TokenExpiresAt, expires)
	}
	if !reflect.DeepEqual(got.Credentials.AccessToken, []byte{1, 2, 3}) ||
		!reflect.DeepEqual(got.Credentials.RefreshToken, []byte{4, 5, 6}) {
		t.Fatalf("encrypted credentials changed: %#v", got.Credentials)
	}
}

func TestScanCustomProvider(t *testing.T) {
	now := time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC)
	row := valueScanner{values: []any{
		"018f0000-0000-7000-8000-000000000002", "subject-1", "reading-env",
		"reading", "Reading", "Books read", "active",
		[]byte(`{"allowed_actions":["read"],"allowed_metrics":["pages"]}`),
		[]byte{9, 8, 7}, now, now,
	}}
	got, err := scanCustomProvider(row)
	if err != nil {
		t.Fatalf("scanCustomProvider() error = %v", err)
	}
	if !reflect.DeepEqual(got.Provider.AllowedActions, []string{"read"}) ||
		!reflect.DeepEqual(got.Provider.AllowedMetrics, []string{"pages"}) {
		t.Fatalf("configuration = %#v", got.Provider)
	}
	if !reflect.DeepEqual(got.EncryptedIngestSecret, []byte{9, 8, 7}) {
		t.Fatalf("encrypted secret changed: %v", got.EncryptedIngestSecret)
	}
	listQuery, args := buildListCustomProvidersQuery("subject-1")
	if !reflect.DeepEqual(args, []any{"subject-1"}) {
		t.Fatalf("custom provider list args = %#v", args)
	}
	for _, fragment := range []string{"ORDER BY provider.subject_id, provider.slug, provider.id", "configuration = COALESCE", "secrets.ingest_token_hash"} {
		if !strings.Contains(upsertCustomProviderQuery+listQuery, fragment) {
			t.Errorf("custom provider SQL missing %q", fragment)
		}
	}
	if !strings.Contains(upsertCustomProviderSecretQuery, "INSERT INTO custom_provider_secrets") {
		t.Fatal("provider digest must be written to the restricted table")
	}
}

func TestGeneratedSyncJobMapsQueuedAndPayload(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	started := now.Add(time.Second)
	expires := now.Add(24 * time.Hour)
	row := generated.GetSyncJobByIdRow{
		Id:           "018f0000-0000-7000-8000-000000000003",
		ConnectionId: "018f0000-0000-7000-8000-000000000001",
		Status:       "queued", Attempt: 1, AvailableAt: now, StartedAt: &started,
		Payload:   `{"trigger":"scheduled","force":true,"timezone":"Asia/Seoul","failure_policy":"purge","facts_written":7,"idempotency_key_hash":"0102","request_hash":"0304","idempotency_expires_at":"2026-08-13T12:00:00Z"}`,
		CreatedAt: now, UpdatedAt: started,
	}
	got, err := syncJobFromGenerated(row)
	if err != nil {
		t.Fatalf("syncJobFromGenerated() error = %v", err)
	}
	if got.Status != integrations.SyncJobPending || got.Trigger != integrations.SyncTriggerScheduled || got.FactsWritten != 7 ||
		!got.Force || got.Timezone != "Asia/Seoul" || got.FailurePolicy != "purge" || !reflect.DeepEqual(got.IdempotencyKeyHash, []byte{1, 2}) ||
		got.IdempotencyExpires == nil || !got.IdempotencyExpires.Equal(expires) {
		t.Fatalf("syncJobFromGenerated() = %#v", got)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("startedAt = %v", got.StartedAt)
	}
}

func TestSyncExecutionLeaseUsesSyncCursor(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"claim_token"}, Rows: [][]driver.Value{{"11111111-1111-4111-8111-111111111111"}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
	)
	db := script.Open()
	defer db.Close()
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	connectionID := "018f0000-0000-7000-8000-000000000001"
	claimToken, acquired, err := store.TryAcquireSyncExecution(context.Background(), connectionID)
	if err != nil || !acquired {
		t.Fatalf("TryAcquireSyncExecution() = %q, %v, %v", claimToken, acquired, err)
	}
	if err := store.ReleaseSyncExecution(context.Background(), connectionID, claimToken); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 2 || !strings.Contains(calls[0].Query, "sync_execution_expires_at") ||
		!strings.Contains(calls[0].Query, "sync_execution_claim_token") || !strings.Contains(calls[1].Query, "sync_execution_claim_token' = $2") {
		t.Fatalf("lease queries = %#v", calls)
	}
}

func TestSyncExecutionLeaseRejectsRevokedConnections(t *testing.T) {
	for _, fragment := range []string{"connection_status", "IN ('active', 'error')"} {
		if !strings.Contains(acquireSyncExecutionQuery, fragment) {
			t.Errorf("acquire query missing %q: %s", fragment, acquireSyncExecutionQuery)
		}
	}
}

func TestCustomActivityPersistenceUsesDedicatedTableAndScansCanonicalRow(t *testing.T) {
	ingestedAt := time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)
	observedAt := ingestedAt.Add(-time.Minute)
	row := valueScanner{values: []any{
		"018f0000-0000-7000-8000-000000000002", "subject-1", "event-1",
		"2026-08-12", "read", "pages", 42,
		[]byte(`{"book":"The Left Hand of Darkness"}`),
		sql.NullTime{Time: observedAt, Valid: true}, ingestedAt,
	}}
	got, err := scanIngestedActivity(row)
	if err != nil {
		t.Fatalf("scanIngestedActivity() error = %v", err)
	}
	want := integrations.IngestedActivity{
		ProviderID: "018f0000-0000-7000-8000-000000000002",
		SubjectID:  "subject-1",
		ExternalID: "event-1",
		Date:       "2026-08-12",
		Action:     "read",
		Metric:     "pages",
		Value:      42,
		Metadata:   map[string]string{"book": "The Left Hand of Darkness"},
		ObservedAt: &observedAt,
		IngestedAt: ingestedAt,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanIngestedActivity() = %#v, want %#v", got, want)
	}
	for _, fragment := range []string{
		"INSERT INTO custom_activity_events",
		"provider.subject_id, provider.environment_id",
		"ON CONFLICT (custom_provider_id, event_id) DO NOTHING",
		"observed_at",
		"RETURNING",
	} {
		if !strings.Contains(insertCustomActivityQuery, fragment) {
			t.Errorf("custom activity insert SQL missing %q", fragment)
		}
	}
}

func TestSaveIngestedActivitiesDeduplicatesPerProviderAndReturnsDatabaseRows(t *testing.T) {
	providerID := "018f0000-0000-7000-8000-000000000002"
	ingestedAt := time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)
	observedAt := ingestedAt.Add(-time.Minute)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns:   []string{"id"},
			Rows:      [][]driver.Value{{providerID}},
		},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns: []string{
				"custom_provider_id", "subject_id", "event_id", "activity_date",
				"action", "metric_name", "metric_value", "metadata", "observed_at",
				"ingested_at",
			},
			Rows: [][]driver.Value{{
				providerID, "canonical-subject", "event-1", "2026-08-12",
				"read", "pages", int64(42), []byte(`{"canonical":"true"}`),
				observedAt, ingestedAt,
			}},
		},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns: []string{
				"custom_provider_id", "subject_id", "event_id", "activity_date",
				"action", "metric_name", "metric_value", "metadata", "observed_at",
				"ingested_at",
			},
		},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	input := []integrations.IngestedActivity{
		{
			ProviderID: providerID, SubjectID: "untrusted-subject", ExternalID: "event-1",
			Date: "2026-08-12", Action: "read", Metric: "pages", Value: 42,
			Metadata: map[string]string{"canonical": "false"}, ObservedAt: &observedAt,
			IngestedAt: ingestedAt,
		},
		{
			ProviderID: providerID, SubjectID: "untrusted-subject", ExternalID: "event-1",
			Date: "2026-08-12", Action: "read", Metric: "pages", Value: 42,
			IngestedAt: ingestedAt,
		},
	}
	accepted, err := store.SaveIngestedActivities(context.Background(), input)
	if err != nil {
		t.Fatalf("SaveIngestedActivities() error = %v", err)
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted = %#v, want one canonical row", accepted)
	}
	if accepted[0].SubjectID != "canonical-subject" ||
		!reflect.DeepEqual(accepted[0].Metadata, map[string]string{"canonical": "true"}) ||
		accepted[0].ObservedAt == nil || !accepted[0].ObservedAt.Equal(observedAt) {
		t.Fatalf("accepted canonical row = %#v", accepted[0])
	}
	calls := script.Calls()
	if len(calls) != 6 || calls[0].Operation != fakedb.Begin || calls[5].Operation != fakedb.Commit {
		t.Fatalf("database calls = %#v", calls)
	}
	for _, call := range calls[2:4] {
		if !strings.Contains(call.Query, "custom_activity_events") ||
			!strings.Contains(call.Query, "ON CONFLICT (custom_provider_id, event_id) DO NOTHING") {
			t.Fatalf("insert query = %s", call.Query)
		}
		if len(call.Args) != 9 || call.Args[0].Value != providerID {
			t.Fatalf("insert args = %#v", call.Args)
		}
	}
}

func TestListIngestedActivitiesReadsDedicatedTableInCanonicalOrder(t *testing.T) {
	providerID := "018f0000-0000-7000-8000-000000000002"
	ingestedAt := time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{
			Operation: fakedb.Query,
			Columns:   []string{"id"},
			Rows:      [][]driver.Value{{providerID}},
		},
		fakedb.Step{
			Operation: fakedb.Query,
			Columns: []string{
				"custom_provider_id", "subject_id", "event_id", "activity_date",
				"action", "metric_name", "metric_value", "metadata", "observed_at",
				"ingested_at",
			},
			Rows: [][]driver.Value{{
				providerID, "subject-1", "event-1", "2026-08-12", "read", "pages",
				int64(42), []byte(`{}`), nil, ingestedAt,
			}},
		},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListIngestedActivities(context.Background(), providerID)
	if err != nil {
		t.Fatalf("ListIngestedActivities() error = %v", err)
	}
	if len(items) != 1 || items[0].ExternalID != "event-1" || items[0].ObservedAt != nil {
		t.Fatalf("items = %#v", items)
	}
	calls := script.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Query, "FROM custom_activity_events") ||
		!strings.Contains(calls[1].Query, "ORDER BY activity_date, event_id") {
		t.Fatalf("list calls = %#v", calls)
	}
}

func TestIngestIdempotencyReservationCompletionAndReleaseUseCompareAndSwap(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	record := integrations.IngestIdempotencyRecord{
		ProviderID: "018f0000-0000-7000-8000-000000000002",
		KeyHash:    []byte{1, 2}, RequestHash: []byte{3, 4},
		ResponseStatus: 0, ResponseBody: []byte(`{}`),
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIngestIdempotencyKey(context.Background(), record)
	if err != nil || !created {
		t.Fatalf("first reservation = %v, %v", created, err)
	}
	created, err = store.CreateIngestIdempotencyKey(context.Background(), record)
	if err != nil || created {
		t.Fatalf("contended reservation = %v, %v", created, err)
	}
	record.ResponseStatus = 202
	record.ResponseBody = []byte(`{"accepted":1,"duplicate":0,"rejected":0,"rejections":[]}`)
	completed, err := store.CompleteIngestIdempotencyKey(context.Background(), record)
	if err != nil || !completed {
		t.Fatalf("completion = %v, %v", completed, err)
	}
	if err := store.ReleaseIngestIdempotencyKey(
		context.Background(), record.ProviderID, record.KeyHash, record.RequestHash,
	); err != nil {
		t.Fatalf("release = %v", err)
	}
	calls := script.Calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %#v", calls)
	}
	for _, index := range []int{0, 1} {
		if !strings.Contains(calls[index].Query, "ON CONFLICT (custom_provider_id, key_hash) DO UPDATE") ||
			!strings.Contains(calls[index].Query, "expires_at <= now()") || len(calls[index].Args) != 7 {
			t.Fatalf("reservation call = %#v", calls[index])
		}
	}
	if !strings.Contains(calls[2].Query, "request_hash = $3") ||
		!strings.Contains(calls[2].Query, "response_status = 0") || len(calls[2].Args) != 6 {
		t.Fatalf("completion call = %#v", calls[2])
	}
	if !strings.Contains(calls[3].Query, "DELETE FROM ingest_idempotency_keys") ||
		!strings.Contains(calls[3].Query, "response_status = 0") || len(calls[3].Args) != 3 {
		t.Fatalf("release call = %#v", calls[3])
	}
}

type valueScanner struct {
	values []any
}

func (s valueScanner) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return errors.New("destination count mismatch")
	}
	for index, target := range dest {
		destination := reflect.ValueOf(target)
		if destination.Kind() != reflect.Pointer || destination.IsNil() {
			return errors.New("destination is not a pointer")
		}
		value := reflect.ValueOf(s.values[index])
		field := destination.Elem()
		if value.Type().AssignableTo(field.Type()) {
			field.Set(value)
		} else if value.Type().ConvertibleTo(field.Type()) {
			field.Set(value.Convert(field.Type()))
		} else {
			return errors.New("value is not assignable")
		}
	}
	return nil
}
