package cockroach

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func revocationConnectionRow(now time.Time, status integrations.ConnectionStatus) []driver.Value {
	return []driver.Value{
		"018f0000-0000-7000-8000-000000000001", "subject-1", "github", "connection:018f0000-0000-7000-8000-000000000001",
		"oauth2", "account-1", "octocat", string(status), []byte(`[]`), []byte("legacy-encrypted-token"), nil,
		nil, nil, nil, nil, int64(0), int64(0), "", now, now, true,
	}
}

func TestRevokeConnectionAggregateAtomicallyEnqueuesSanitizesAndPurges(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: make([]string, 21), Rows: [][]driver.Value{revocationConnectionRow(now, integrations.ConnectionActive)}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 4},
		fakedb.Step{Operation: fakedb.Exec, Affected: 2},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	connection, err := store.RevokeConnectionAggregate(context.Background(), "018f0000-0000-7000-8000-000000000001", now)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != integrations.ConnectionRevoked || connection.PrivateDataEnabled {
		t.Fatalf("revoked connection = status %q private data enabled %t", connection.Status, connection.PrivateDataEnabled)
	}
	calls := script.Calls()
	wantFragments := []string{"FOR UPDATE", "INSERT INTO provider_token_revocation_jobs", "access_token_ciphertext = NULL", "DELETE FROM provider_connection_private_consents", "DELETE FROM activity_facts", "DELETE FROM provider_sync_jobs"}
	for index, fragment := range wantFragments {
		if !strings.Contains(calls[index+1].Query, fragment) {
			t.Fatalf("call %d missing %q: %s", index+1, fragment, calls[index+1].Query)
		}
	}
	if strings.Contains(calls[2].Query, "ON CONFLICT") {
		t.Fatalf("API revocation enqueue requires queue conflict reads: %s", calls[2].Query)
	}
}

func TestRevokeConnectionAggregateRollbackRemainsRetryable(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	purgeFailure := errors.New("facts unavailable")
	steps := []fakedb.Step{
		{Operation: fakedb.Begin},
		{Operation: fakedb.Query, Columns: make([]string, 21), Rows: [][]driver.Value{revocationConnectionRow(now, integrations.ConnectionActive)}},
		{Operation: fakedb.Exec, Affected: 1}, {Operation: fakedb.Exec, Affected: 1},
		{Operation: fakedb.Exec, Affected: 1}, {Operation: fakedb.Exec, Err: purgeFailure}, {Operation: fakedb.Rollback},
		{Operation: fakedb.Begin},
		{Operation: fakedb.Query, Columns: make([]string, 21), Rows: [][]driver.Value{revocationConnectionRow(now, integrations.ConnectionActive)}},
		{Operation: fakedb.Exec, Affected: 1}, {Operation: fakedb.Exec, Affected: 1},
		{Operation: fakedb.Exec, Affected: 1}, {Operation: fakedb.Exec, Affected: 4}, {Operation: fakedb.Exec, Affected: 2}, {Operation: fakedb.Commit},
	}
	script := fakedb.New(steps...)
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	if _, err := store.RevokeConnectionAggregate(context.Background(), "018f0000-0000-7000-8000-000000000001", now); !errors.Is(err, purgeFailure) {
		t.Fatalf("first revoke error = %v", err)
	}
	if _, err := store.RevokeConnectionAggregate(context.Background(), "018f0000-0000-7000-8000-000000000001", now); err != nil {
		t.Fatalf("retry revoke error = %v", err)
	}
}

func TestCheckRevocationSchemaRequiresTerminalColumns(t *testing.T) {
	script := fakedb.New(fakedb.Step{Operation: fakedb.Query, Columns: []string{"count"}, Rows: [][]driver.Value{{int64(19)}}})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	if err := store.CheckRevocationSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := script.Calls()[0].Query
	for _, column := range []string{"terminal_at", "terminal_reason"} {
		if !strings.Contains(query, column) {
			t.Fatalf("schema query missing %q: %s", column, query)
		}
	}
}

func TestClaimOAuthTokenRevocationsExcludesDeadAndFencesClaim(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(2 * time.Minute)
	createdAt := now.Add(-time.Hour)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "connection_id", "provider_id", "token_ciphertext", "token_key_id", "claim_token", "attempts", "available_at", "lease_until", "created_at", "updated_at"},
		Rows: [][]driver.Value{{
			"018f0000-0000-7000-8000-000000000010", "", "github", []byte("ciphertext"), "key-1",
			"018f0000-0000-7000-8000-000000000011", int64(4), now, leaseUntil, createdAt, now,
		}},
	})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	jobs, err := store.ClaimOAuthTokenRevocations(context.Background(), now, leaseUntil, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Attempts != 4 || jobs[0].LeaseUntil == nil || *jobs[0].LeaseUntil != leaseUntil {
		t.Fatalf("claimed jobs = %+v", jobs)
	}
	query := script.Calls()[0].Query
	for _, fragment := range []string{"status = 'pending'", "status = 'processing'", "attempts = jobs.attempts + 1", "claim_token = gen_random_uuid()", "FOR UPDATE SKIP LOCKED"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("claim query missing %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "status = 'dead'") {
		t.Fatalf("claim query made dead jobs eligible: %s", query)
	}
}

func TestDeadLetterOAuthTokenRevocationPersistsObservableTerminalState(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 1})
	db := script.Open()
	defer db.Close()
	store, _ := New(db)
	if err := store.DeadLetterOAuthTokenRevocation(context.Background(), "018f0000-0000-7000-8000-000000000010", "018f0000-0000-7000-8000-000000000011", now); err != nil {
		t.Fatal(err)
	}
	call := script.Calls()[0]
	for _, fragment := range []string{"status = 'dead'", "lease_until = NULL", "claim_token = NULL", "terminal_at = $3", "terminal_reason = 'max_attempts_exhausted'", "status = 'processing'", "claim_token = $2::UUID"} {
		if !strings.Contains(call.Query, fragment) {
			t.Fatalf("dead-letter query missing %q: %s", fragment, call.Query)
		}
	}
	if len(call.Args) != 3 || call.Args[2].Value != now {
		t.Fatalf("dead-letter args = %+v", call.Args)
	}
}

func TestRevocationDispositionRejectsInvalidOrStaleClaims(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	validationDB := fakedb.New().Open()
	defer validationDB.Close()
	store, _ := New(validationDB)
	if err := store.RetryOAuthTokenRevocation(context.Background(), "job", "claim", time.Time{}); !errors.Is(err, integrations.ErrInvalidRevocationConfig) {
		t.Fatalf("zero retry time error = %v", err)
	}
	if err := store.DeadLetterOAuthTokenRevocation(context.Background(), "job", "claim", time.Time{}); !errors.Is(err, integrations.ErrInvalidRevocationConfig) {
		t.Fatalf("zero terminal time error = %v", err)
	}

	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 0})
	db := script.Open()
	defer db.Close()
	store, _ = New(db)
	if err := store.DeadLetterOAuthTokenRevocation(context.Background(), "018f0000-0000-7000-8000-000000000010", "018f0000-0000-7000-8000-000000000011", now); !errors.Is(err, integrations.ErrConflict) {
		t.Fatalf("stale dead-letter claim error = %v", err)
	}
}
