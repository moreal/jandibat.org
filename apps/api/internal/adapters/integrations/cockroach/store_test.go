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
