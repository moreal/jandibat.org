package cockroach

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestSaveConnectionActivityFencesAndTagsFactsInOneTransaction(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"subject_id", "environment_id"}, Rows: [][]driver.Value{{"sub-1", "connection:018f0000-0000-7000-8000-000000000001"}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	owner := activity.SubjectID("sub-1")
	environment := activity.Environment{
		ID: "connection:018f0000-0000-7000-8000-000000000001", Key: "github", Name: "GitHub",
		Scope: activity.EnvironmentScopeSubject, OwnerSubject: &owner,
	}
	fact := activity.Fact{
		Subject: owner, EnvironmentID: environment.ID, Date: "2026-08-12",
		Action: activity.ActionCommit, Metric: activity.Metric{Name: activity.MetricCount, Value: 1},
	}
	err := store.SaveConnectionActivity(context.Background(), "018f0000-0000-7000-8000-000000000001", "claim-1",
		activity.SaveEnvironmentsInput{Environments: []activity.Environment{environment}},
		activity.SaveFactsInput{Subject: owner, Facts: []activity.Fact{fact}},
	)
	if err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 6 || calls[0].Operation != fakedb.Begin || calls[5].Operation != fakedb.Commit {
		t.Fatalf("transaction calls = %#v", calls)
	}
	for _, fragment := range []string{"FOR UPDATE", "connection_status", "sync_execution_claim_token"} {
		if !strings.Contains(calls[1].Query, fragment) {
			t.Errorf("fence query missing %q: %s", fragment, calls[1].Query)
		}
	}
	if !strings.Contains(calls[3].Query, "provider_connection_id") || len(calls[3].Args) != 9 ||
		calls[3].Args[7].Value != "018f0000-0000-7000-8000-000000000001" {
		t.Fatalf("fact insert lacks connection FK: %#v", calls[3])
	}
}

func TestSaveConnectionActivityRejectsRevokedOrStaleClaimBeforeWrites(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"subject_id", "environment_id"}},
		fakedb.Step{Operation: fakedb.Rollback},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	err := store.SaveConnectionActivity(context.Background(), "018f0000-0000-7000-8000-000000000001", "stale-claim",
		activity.SaveEnvironmentsInput{}, activity.SaveFactsInput{Subject: "sub-1"})
	if !errors.Is(err, integrations.ErrInvalidConnectionStatus) {
		t.Fatalf("SaveConnectionActivity() error = %v", err)
	}
	if calls := script.Calls(); len(calls) != 3 || calls[2].Operation != fakedb.Rollback {
		t.Fatalf("stale writer performed a mutation: %#v", calls)
	}
}
