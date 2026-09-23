package cockroach

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

func TestNewWithPGXPoolAcceptsPoolWithoutSQLHandle(t *testing.T) {
	pool := new(pgxpool.Pool)
	store, err := NewWithPGXPool(nil, pool)
	if err != nil {
		t.Fatalf("NewWithPGXPool(nil, pool) error = %v", err)
	}
	if store == nil || store.pool != pool {
		t.Fatalf("NewWithPGXPool(nil, pool) = %#v; want store using supplied pool", store)
	}
}

func TestNewWithPGXPoolRejectsNilPool(t *testing.T) {
	if store, err := NewWithPGXPool(nil, nil); store != nil || !errors.Is(err, ErrNilDB) {
		t.Fatalf("NewWithPGXPool(nil, nil) = %#v, %v; want ErrNilDB", store, err)
	}
}

func TestPersistenceErrorMapsMalformedUUIDToInvalidIdentifier(t *testing.T) {
	err := persistenceError(&pgconn.PgError{Code: "22P02"}, nil)
	if !errors.Is(err, integrations.ErrInvalidIdentifier) {
		t.Fatalf("persistenceError(22P02) = %v", err)
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

func TestSyncExecutionRequiresPGXPool(t *testing.T) {
	db := fakedb.New().Open()
	defer db.Close()
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	connectionID := "018f0000-0000-7000-8000-000000000001"
	_, _, err = store.TryAcquireSyncExecution(context.Background(), connectionID)
	if !errors.Is(err, ErrNilDB) {
		t.Fatalf("TryAcquireSyncExecution() error = %v, want %v", err, ErrNilDB)
	}
	if err := store.ReleaseSyncExecution(context.Background(), connectionID, "claim-token"); !errors.Is(err, ErrNilDB) {
		t.Fatalf("ReleaseSyncExecution() error = %v, want %v", err, ErrNilDB)
	}
}
