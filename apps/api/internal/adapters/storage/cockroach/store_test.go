package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func TestNewRejectsNilDatabase(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) error = %v, want %v", err, ErrNilDB)
	}
	if store != nil {
		t.Fatal("New(nil) returned a non-nil store")
	}
}

func TestBuildLoadFactsQuery(t *testing.T) {
	from := activity.Date("2026-01-01")
	to := activity.Date("2026-01-31")
	query, args := buildLoadFactsQuery(activity.LoadFactsInput{
		Subject: "alice",
		From:    &from,
		To:      &to,
	})

	for _, fragment := range []string{
		"WHERE subject_id = $1",
		"activity_date >= $2",
		"activity_date <= $3",
		"ORDER BY activity_date, environment_id, action, metric_name, id",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query missing %q:\n%s", fragment, query)
		}
	}
	wantArgs := []any{activity.SubjectID("alice"), from, to}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestBuildLoadFactsQueryOptionalBounds(t *testing.T) {
	to := activity.Date("2026-01-31")
	query, args := buildLoadFactsQuery(activity.LoadFactsInput{Subject: "alice", To: &to})
	if strings.Contains(query, "activity_date >=") {
		t.Fatalf("query unexpectedly contains lower bound:\n%s", query)
	}
	if !strings.Contains(query, "activity_date <= $2") {
		t.Fatalf("query missing upper bound placeholder:\n%s", query)
	}
	if len(args) != 2 {
		t.Fatalf("len(args) = %d, want 2", len(args))
	}
}

func TestBuildDeleteFactsQueryScopesReplacement(t *testing.T) {
	from := activity.Date("2026-02-01")
	to := activity.Date("2026-02-28")
	query, args := buildDeleteFactsQuery(activity.LoadFactsInput{
		Subject: "alice", From: &from, To: &to,
	}, []activity.EnvironmentID{"github", "gitlab"})

	for _, fragment := range []string{
		"subject_id = $1",
		"activity_date >= $2",
		"activity_date <= $3",
		"environment_id IN ($4, $5)",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query missing %q:\n%s", fragment, query)
		}
	}
	wantArgs := []any{
		activity.SubjectID("alice"), from, to,
		activity.EnvironmentID("github"), activity.EnvironmentID("gitlab"),
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestUpsertFactAvoidsExpressionConflictTarget(t *testing.T) {
	if !strings.Contains(upsertFactQuery, "ON CONFLICT DO NOTHING") {
		t.Fatalf("upsert query must not target CockroachDB's expression index:\n%s", upsertFactQuery)
	}
	if strings.Contains(upsertFactQuery, "DO UPDATE") {
		t.Fatalf("upsert query unexpectedly updates without an arbiter:\n%s", upsertFactQuery)
	}
}

func TestSaveFactsTransactionallyEnsuresPublicShadowSubject(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	fact := activity.Fact{
		Subject: "octocat", Date: "2026-08-12", EnvironmentID: "github",
		Action: activity.ActionCommit, Metric: activity.Metric{Name: activity.MetricCount, Value: 1},
	}
	if err := store.SaveFacts(context.Background(), activity.SaveFactsInput{Subject: "octocat", Facts: []activity.Fact{fact}}); err != nil {
		t.Fatalf("SaveFacts() error = %v", err)
	}

	calls := script.Calls()
	if len(calls) != 5 || calls[0].Operation != fakedb.Begin || calls[4].Operation != fakedb.Commit {
		t.Fatalf("transaction calls = %#v", calls)
	}
	ensure := calls[1]
	for _, fragment := range []string{"INSERT INTO subjects", "owner_user_id", "NULL", "'UTC'", "true", "ON CONFLICT DO NOTHING"} {
		if !strings.Contains(ensure.Query, fragment) {
			t.Errorf("shadow subject query missing %q:\n%s", fragment, ensure.Query)
		}
	}
	if len(ensure.Args) != 1 || ensure.Args[0].Value != activity.SubjectID("octocat") {
		t.Fatalf("shadow subject args = %#v", ensure.Args)
	}
	if !strings.Contains(calls[2].Query, "INSERT INTO activity_facts") || !strings.Contains(calls[3].Query, "UPDATE activity_facts") {
		t.Fatalf("fact writes did not follow subject ensure: %#v", calls)
	}
	if remaining := script.Remaining(); remaining != 0 {
		t.Fatalf("remaining scripted operations = %d", remaining)
	}
}

func TestReplaceFactsDoesNotCreateShadowSubjectForEmptyProviderResult(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	date := activity.Date("2026-08-12")
	if err := store.ReplaceFacts(context.Background(), activity.LoadFactsInput{
		Subject: "octocat", From: &date, To: &date,
	}, []activity.EnvironmentID{"github"}, nil); err != nil {
		t.Fatalf("ReplaceFacts() error = %v", err)
	}

	calls := script.Calls()
	if len(calls) != 3 || strings.Contains(calls[1].Query, "INSERT INTO subjects") ||
		!strings.Contains(calls[1].Query, "DELETE FROM activity_facts") || calls[2].Operation != fakedb.Commit {
		t.Fatalf("replacement transaction calls = %#v", calls)
	}
}

func TestSaveFactsRollsBackWhenShadowSubjectCannotBeEnsured(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Err: errors.New("subject insert failed")},
		fakedb.Step{Operation: fakedb.Rollback},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	fact := activity.Fact{
		Subject: "octocat", Date: "2026-08-12", EnvironmentID: "github",
		Action: activity.ActionCommit, Metric: activity.Metric{Name: activity.MetricCount, Value: 1},
	}
	err = store.SaveFacts(context.Background(), activity.SaveFactsInput{Subject: "octocat", Facts: []activity.Fact{fact}})
	if err == nil || !strings.Contains(err.Error(), "ensure public subject") {
		t.Fatalf("SaveFacts() error = %v", err)
	}
	if remaining := script.Remaining(); remaining != 0 {
		t.Fatalf("remaining scripted operations = %d", remaining)
	}
}

func TestScanFact(t *testing.T) {
	row := valueScanner{values: []any{
		"alice",
		time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC),
		"github",
		"commit",
		"count",
		int64(7),
		[]byte(`{"repository":"jandibat.org"}`),
	}}

	got, err := scanFact(row)
	if err != nil {
		t.Fatalf("scanFact() error = %v", err)
	}
	want := activity.Fact{
		Subject:       "alice",
		Date:          "2026-01-03",
		EnvironmentID: "github",
		Action:        activity.ActionCommit,
		Metric:        activity.Metric{Name: activity.MetricCount, Value: 7},
		Metadata:      map[string]string{"repository": "jandibat.org"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanFact() = %#v, want %#v", got, want)
	}
}

func TestScanEnvironment(t *testing.T) {
	row := valueScanner{values: []any{
		"reading",
		"books",
		"Reading",
		"subject",
		sql.NullString{String: "alice", Valid: true},
		[]byte(`{"unit":"pages"}`),
	}}

	got, err := scanEnvironment(row)
	if err != nil {
		t.Fatalf("scanEnvironment() error = %v", err)
	}
	owner := activity.SubjectID("alice")
	want := activity.Environment{
		ID:           "reading",
		Key:          "books",
		Name:         "Reading",
		Scope:        activity.EnvironmentScopeSubject,
		OwnerSubject: &owner,
		Metadata:     map[string]string{"unit": "pages"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanEnvironment() = %#v, want %#v", got, want)
	}
}

func TestBuildLoadEnvironmentsQuery(t *testing.T) {
	query, args := buildLoadEnvironmentsQuery([]activity.EnvironmentID{"github", "reading"})
	if !strings.Contains(query, "WHERE id IN ($1, $2)") {
		t.Fatalf("query has wrong placeholders:\n%s", query)
	}
	if !strings.HasSuffix(query, "ORDER BY id") {
		t.Fatalf("query is not deterministic:\n%s", query)
	}
	wantArgs := []any{activity.EnvironmentID("github"), activity.EnvironmentID("reading")}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestDecodeMetadataRejectsNonStringValues(t *testing.T) {
	_, err := decodeMetadata([]byte(`{"count": 2}`))
	if err == nil {
		t.Fatal("decodeMetadata() succeeded for non-string metadata")
	}
}

type valueScanner struct {
	values []any
}

func (s valueScanner) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return errors.New("destination count mismatch")
	}
	for i, value := range s.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *time.Time:
			*target = value.(time.Time)
		case *int64:
			*target = value.(int64)
		case *[]byte:
			*target = append((*target)[:0], value.([]byte)...)
		case *sql.NullString:
			*target = value.(sql.NullString)
		default:
			return errors.New("unsupported destination type")
		}
	}
	return nil
}
