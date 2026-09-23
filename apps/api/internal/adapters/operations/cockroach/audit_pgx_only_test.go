package cockroach

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// Audit/outbox writes must not silently use the legacy database/sql handle:
// they participate in the same pgx transaction boundary as state mutations.
func TestAuditOutboxPublicMethodsRequirePGXPool(t *testing.T) {
	db := fakedb.New().Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	id := "a31ca7e5-43bd-46de-ae62-8f39e11374cc"
	claim := "d31ca7e5-43bd-46de-ae62-8f39e11374cc"
	event := operations.AuditEvent{
		ID: id, OccurredAt: now,
		Actor:   operations.AuditActor{Type: operations.AuditActorUser, ID: "user-1"},
		Action:  "maintenance.audit.test",
		Target:  operations.AuditTarget{Type: "subject", ID: "subject-1"},
		Outcome: operations.AuditSucceeded, RequestID: "request-1",
	}
	if err := store.WriteAuditEvent(ctx, event); !errors.Is(err, ErrNilDB) {
		t.Fatalf("WriteAuditEvent without pool = %v, want ErrNilDB", err)
	}
	if err := store.CheckMutationAuditOutboxSchema(ctx); !errors.Is(err, ErrNilDB) {
		t.Fatalf("CheckMutationAuditOutboxSchema without pool = %v, want ErrNilDB", err)
	}
	if _, _, err := store.BeginMutation(ctx); !errors.Is(err, ErrNilDB) {
		t.Fatalf("BeginMutation without pool = %v, want ErrNilDB", err)
	}
	if _, err := store.ClaimMutationAudits(ctx, now, time.Minute, 1, 5); !errors.Is(err, ErrNilDB) {
		t.Fatalf("ClaimMutationAudits without pool = %v, want ErrNilDB", err)
	}
	if err := store.DeliverMutationAudit(ctx, id, claim, now); !errors.Is(err, ErrNilDB) {
		t.Fatalf("DeliverMutationAudit without pool = %v, want ErrNilDB", err)
	}
	if _, err := store.RetryMutationAudit(ctx, id, claim, now, now.Add(time.Minute), 5, "delivery_failed"); !errors.Is(err, ErrNilDB) {
		t.Fatalf("RetryMutationAudit without pool = %v, want ErrNilDB", err)
	}
}
