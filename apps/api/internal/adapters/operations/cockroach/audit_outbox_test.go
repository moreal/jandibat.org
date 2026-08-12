package cockroach

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
)

func TestDeliverMutationAuditUsesInsertOnlySinkAndFencedAtomicTransition(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	metadata := []byte(`{"phase":"outcome","status":200}`)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{
			"event_id", "occurred", "actor_type", "actor_id", "action", "target_type", "target_id", "outcome", "request_id", "metadata",
		}, Rows: [][]driver.Value{{
			"a31ca7e5-43bd-46de-ae62-8f39e11374cc", now, "user", "user-1",
			"patch.v1.me.settings", "subject", "subject-1", "succeeded", "request-1", metadata,
		}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	if err := store.DeliverMutationAudit(context.Background(), "c31ca7e5-43bd-46de-ae62-8f39e11374cc", "d31ca7e5-43bd-46de-ae62-8f39e11374cc", now); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 5 || !strings.Contains(calls[1].Query, "claim_token = $2::UUID") ||
		!strings.Contains(calls[2].Query, "INSERT INTO audit_events") ||
		strings.Contains(calls[2].Query, "ON CONFLICT") || strings.Contains(calls[2].Query, "RETURNING") ||
		!strings.Contains(calls[3].Query, "status = 'delivered'") ||
		!strings.Contains(calls[3].Query, "claim_token = $2::UUID") {
		t.Fatalf("delivery transaction = %#v", calls)
	}
}

func TestClaimMutationAuditRecoversFinalCrashBeforeSelecting(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 13)},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	items, err := store.ClaimMutationAudits(context.Background(), now, time.Minute, 25, 5)
	if err != nil || len(items) != 0 {
		t.Fatalf("claim = %#v, %v", items, err)
	}
	if calls := script.Calls(); !strings.Contains(calls[1].Query, "attempts >= $2") ||
		!strings.Contains(calls[1].Query, "status = 'dead'") || !strings.Contains(calls[2].Query, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("claim recovery = %#v", calls)
	}
}
