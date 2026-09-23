//go:build integration

package cockroach

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func TestFactChangesAdvanceSnapshotProvenanceOnlyWhenVisibleDataChanges(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to an isolated migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	subject := activity.SubjectID("snapshot-provenance-" + suffix)
	environment := activity.EnvironmentID("snapshot-provenance-env-" + suffix)
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, string(subject))
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM environments WHERE id = $1`, string(environment))
	})
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{{
		ID: environment, Key: string(environment), Name: "Provenance", Scope: activity.EnvironmentScopeGlobal,
	}}}); err != nil {
		t.Fatal(err)
	}
	fact := activity.Fact{Subject: subject, EnvironmentID: environment, Date: "2024-02-29", Action: activity.ActionCommit,
		Metric: activity.Metric{Name: activity.MetricCount, Value: 1}}
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatal(err)
	}
	first := readSnapshotChange(t, ctx, pool, subject, environment, fact.Date, "public")
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatal(err)
	}
	unchanged := readSnapshotChange(t, ctx, pool, subject, environment, fact.Date, "public")
	if !unchanged.Equal(first) {
		t.Fatalf("identical SaveFacts advanced provenance: first=%s unchanged=%s", first, unchanged)
	}
	fact.Metric.Value = 2
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatal(err)
	}
	modified := readSnapshotChange(t, ctx, pool, subject, environment, fact.Date, "public")
	if !modified.After(first) {
		t.Fatalf("changed fact did not advance provenance: first=%s modified=%s", first, modified)
	}
	filter := activity.LoadFactsInput{Subject: subject}
	if err := store.ReplaceFacts(ctx, filter, []activity.EnvironmentID{environment}, []activity.Fact{fact}); err != nil {
		t.Fatal(err)
	}
	if got := readSnapshotChange(t, ctx, pool, subject, environment, fact.Date, "public"); !got.Equal(modified) {
		t.Fatalf("identical replacement advanced provenance: modified=%s replacement=%s", modified, got)
	}
	if err := store.ReplaceFacts(ctx, filter, []activity.EnvironmentID{environment}, nil); err != nil {
		t.Fatal(err)
	}
	deleted := readSnapshotChange(t, ctx, pool, subject, environment, fact.Date, "public")
	if !deleted.After(modified) {
		t.Fatalf("deletion did not advance provenance: modified=%s deleted=%s", modified, deleted)
	}
}

func TestLoadSnapshotProjectionScopesPrivateFactsAndChangeTime(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to an isolated migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	subject := activity.SubjectID("snapshot-read-" + suffix)
	globalID := activity.EnvironmentID("snapshot-read-global-" + suffix)
	publicID := activity.EnvironmentID("snapshot-read-public-" + suffix)
	privateID := activity.EnvironmentID("snapshot-read-private-" + suffix)
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, string(subject))
		for _, id := range []activity.EnvironmentID{globalID, publicID, privateID} {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM environments WHERE id = $1`, string(id))
		}
	})
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{{
		ID: globalID, Key: string(globalID), Name: "Global", Scope: activity.EnvironmentScopeGlobal,
	}}}); err != nil {
		t.Fatal(err)
	}
	fact := func(environment activity.EnvironmentID, value int) activity.Fact {
		return activity.Fact{Subject: subject, EnvironmentID: environment, Date: "2024-02-29", Action: activity.ActionCommit,
			Metric: activity.Metric{Name: activity.MetricCount, Value: value}}
	}
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{fact(globalID, 1)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{
		{ID: publicID, Key: string(publicID), Name: "Public subject", Scope: activity.EnvironmentScopeSubject, OwnerSubject: &subject},
		{ID: privateID, Key: string(privateID), Name: "Private subject", Scope: activity.EnvironmentScopeSubject, OwnerSubject: &subject, Metadata: map[string]string{"visibility": "private"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{fact(publicID, 2), fact(privateID, 3)}}); err != nil {
		t.Fatal(err)
	}
	date := activity.Date("2024-02-29")
	filter := activity.LoadFactsInput{Subject: subject, From: &date, To: &date}
	publicFacts, publicEnvs, publicChanged, err := store.LoadSnapshotProjection(ctx, filter, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicFacts) != 2 || len(publicEnvs) != 2 || publicChanged == nil {
		t.Fatalf("public projection = (%d facts, %d environments, %v, %v)", len(publicFacts), len(publicEnvs), publicChanged, err)
	}
	ownerFacts, ownerEnvs, ownerChanged, err := store.LoadSnapshotProjection(ctx, filter, nil, true)
	if err != nil || len(ownerFacts) != 3 || len(ownerEnvs) != 3 || ownerChanged == nil {
		t.Fatalf("owner projection = (%d facts, %d environments, %v, %v)", len(ownerFacts), len(ownerEnvs), ownerChanged, err)
	}
	privateOnlyFacts, _, privateOnlyChanged, err := store.LoadSnapshotProjection(ctx, filter, []activity.EnvironmentID{privateID}, false)
	if err != nil || len(privateOnlyFacts) != 0 || privateOnlyChanged != nil {
		t.Fatalf("public private-only projection = (%d facts, %v, %v)", len(privateOnlyFacts), privateOnlyChanged, err)
	}
	next := activity.Date("2024-03-01")
	_, _, nextChanged, err := store.LoadSnapshotProjection(ctx, activity.LoadFactsInput{Subject: subject, From: &next, To: &next}, nil, true)
	if err != nil || nextChanged != nil {
		t.Fatalf("unseen date change time = (%v, %v)", nextChanged, err)
	}
}

func TestEnvironmentVisibilityTransitionMarksBothAudiences(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to an isolated migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	subject := activity.SubjectID("snapshot-visibility-" + suffix)
	environmentID := activity.EnvironmentID("snapshot-visibility-env-" + suffix)
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, string(subject))
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM environments WHERE id = $1`, string(environmentID))
	})
	if _, err := pool.Exec(ctx, `INSERT INTO subjects (id, handle, timezone, is_public) VALUES ($1, $1, 'UTC', true)`, string(subject)); err != nil {
		t.Fatal(err)
	}
	environment := activity.Environment{ID: environmentID, Key: string(environmentID), Name: "Public", Scope: activity.EnvironmentScopeSubject, OwnerSubject: &subject}
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	fact := activity.Fact{Subject: subject, EnvironmentID: environmentID, Date: "2024-02-29", Action: activity.ActionCommit,
		Metric: activity.Metric{Name: activity.MetricCount, Value: 1}}
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatal(err)
	}
	publicBefore := readSnapshotChange(t, ctx, pool, subject, environmentID, fact.Date, "public")
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	if got := readSnapshotChange(t, ctx, pool, subject, environmentID, fact.Date, "public"); !got.Equal(publicBefore) {
		t.Fatalf("identical environment advanced provenance: before=%s after=%s", publicBefore, got)
	}
	environment.Name = "Private"
	environment.Metadata = map[string]string{"visibility": "private"}
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	publicAfter := readSnapshotChange(t, ctx, pool, subject, environmentID, fact.Date, "public")
	privateAfter := readSnapshotChange(t, ctx, pool, subject, environmentID, fact.Date, "private")
	if !publicAfter.After(publicBefore) || privateAfter.IsZero() {
		t.Fatalf("visibility transition markers = (public before=%s after=%s, private=%s)", publicBefore, publicAfter, privateAfter)
	}
	date := fact.Date
	filter := activity.LoadFactsInput{Subject: subject, From: &date, To: &date}
	publicFacts, _, publicChanged, err := store.LoadSnapshotProjection(ctx, filter, nil, false)
	if err != nil || len(publicFacts) != 0 || publicChanged == nil || !publicChanged.Equal(publicAfter) {
		t.Fatalf("public after transition = (%d facts, %v, %v)", len(publicFacts), publicChanged, err)
	}
	ownerFacts, _, _, err := store.LoadSnapshotProjection(ctx, filter, nil, true)
	if err != nil || len(ownerFacts) != 1 {
		t.Fatalf("owner after transition = (%d facts, %v)", len(ownerFacts), err)
	}
}

func TestReplaceFactsDoesNotAdvanceUnchangedDay(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to an isolated migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	subject := activity.SubjectID("snapshot-partial-" + suffix)
	environmentID := activity.EnvironmentID("snapshot-partial-env-" + suffix)
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, string(subject))
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM environments WHERE id = $1`, string(environmentID))
	})
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{{
		ID: environmentID, Key: string(environmentID), Name: "Partial", Scope: activity.EnvironmentScopeGlobal,
	}}}); err != nil {
		t.Fatal(err)
	}
	makeFact := func(date activity.Date, count int) activity.Fact {
		return activity.Fact{Subject: subject, EnvironmentID: environmentID, Date: date, Action: activity.ActionCommit,
			Metric: activity.Metric{Name: activity.MetricCount, Value: count}}
	}
	firstDate, secondDate := activity.Date("2024-02-28"), activity.Date("2024-02-29")
	first, second := makeFact(firstDate, 1), makeFact(secondDate, 2)
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subject, Facts: []activity.Fact{first, second}}); err != nil {
		t.Fatal(err)
	}
	secondBefore := readSnapshotChange(t, ctx, pool, subject, environmentID, secondDate, "public")
	first.Metric.Value = 3
	filter := activity.LoadFactsInput{Subject: subject, From: &firstDate, To: &secondDate}
	if err := store.ReplaceFacts(ctx, filter, []activity.EnvironmentID{environmentID}, []activity.Fact{first, second}); err != nil {
		t.Fatal(err)
	}
	secondAfter := readSnapshotChange(t, ctx, pool, subject, environmentID, secondDate, "public")
	if !secondAfter.Equal(secondBefore) {
		t.Fatalf("unchanged day provenance advanced: before=%s after=%s", secondBefore, secondAfter)
	}
}

func readSnapshotChange(t *testing.T, ctx context.Context, pool *pgxpool.Pool, subject activity.SubjectID, environment activity.EnvironmentID, date activity.Date, scope string) time.Time {
	t.Helper()
	var changedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT changed_at FROM activity_snapshot_changes WHERE subject_id = $1 AND environment_id = $2 AND activity_date = $3::DATE AND visibility_scope = $4`, string(subject), string(environment), string(date), scope).Scan(&changedAt); err != nil {
		t.Fatalf("read snapshot change: %v", err)
	}
	return changedAt
}
