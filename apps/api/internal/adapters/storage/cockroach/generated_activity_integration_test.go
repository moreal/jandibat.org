//go:build integration

package cockroach

import (
	"context"
	"os"
	"testing"
	"time"

	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestGeneratedActivityRepositoryRoundTrip(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := store.SubjectExists(ctx, activity.SubjectID("scythe-missing-subject"))
	if err != nil || exists {
		t.Fatalf("SubjectExists(missing) = (%v, %v)", exists, err)
	}
	emptySubject := activity.SubjectID("scythe-empty-" + uuid.NewString())
	if err := store.ReplaceFacts(ctx, activity.LoadFactsInput{Subject: emptySubject}, nil, nil); err != nil {
		t.Fatalf("ReplaceFacts(empty) = %v", err)
	}
	if exists, err := store.SubjectExists(ctx, emptySubject); err != nil || exists {
		t.Fatalf("empty replacement created subject = (%v, %v)", exists, err)
	}

	environmentID := activity.EnvironmentID("scythe-environment-" + uuid.NewString())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM environments WHERE id = $1`, string(environmentID))
	})
	input := activity.Environment{
		ID: environmentID, Key: string(environmentID), Name: "Scythe test",
		Scope: activity.EnvironmentScopeGlobal, Metadata: map[string]string{"source": "integration"},
	}
	if err := store.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{input}}); err != nil {
		t.Fatalf("SaveEnvironments() = %v", err)
	}
	loaded, err := store.LoadEnvironments(ctx, activity.LoadEnvironmentsInput{IDs: []activity.EnvironmentID{environmentID}})
	if err != nil || len(loaded) != 1 || loaded[0].ID != input.ID || loaded[0].Metadata["source"] != "integration" {
		t.Fatalf("LoadEnvironments() = (%+v, %v)", loaded, err)
	}

	subjectID := activity.SubjectID("scythe-activity-" + uuid.NewString())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, string(subjectID))
	})
	fact := activity.Fact{
		Subject: subjectID, Date: activity.Date("2026-09-23"), EnvironmentID: environmentID,
		Action: activity.ActionCommit, Metric: activity.Metric{Name: activity.MetricCount, Value: 3},
		Metadata: map[string]string{"source": "scythe"},
	}
	if err := store.SaveFacts(ctx, activity.SaveFactsInput{Subject: subjectID, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatalf("SaveFacts() = %v", err)
	}
	facts, err := store.LoadFacts(ctx, activity.LoadFactsInput{Subject: subjectID})
	if err != nil || len(facts) != 1 || facts[0].Metric.Value != 3 || facts[0].Metadata["source"] != "scythe" {
		t.Fatalf("LoadFacts() = (%+v, %v)", facts, err)
	}
	filter := activity.LoadFactsInput{Subject: subjectID}
	fact.Metric.Value = 5
	if err := store.ReplaceFacts(ctx, filter, []activity.EnvironmentID{environmentID}, []activity.Fact{fact}); err != nil {
		t.Fatalf("ReplaceFacts() = %v", err)
	}
	facts, err = store.LoadFacts(ctx, filter)
	if err != nil || len(facts) != 1 || facts[0].Metric.Value != 5 {
		t.Fatalf("LoadFacts(replaced) = (%+v, %v)", facts, err)
	}
	cacheAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.SaveCachedAt(ctx, subjectID, environmentID, []activity.Date{fact.Date}, cacheAt); err != nil {
		t.Fatalf("SaveCachedAt() = %v", err)
	}
	loadedAt, err := store.LoadCachedAt(ctx, subjectID, environmentID, fact.Date)
	if err != nil || loadedAt == nil || !loadedAt.Equal(cacheAt) {
		t.Fatalf("LoadCachedAt() = (%v, %v)", loadedAt, err)
	}
	if err := store.DeleteCachedAt(ctx, subjectID, environmentID, []activity.Date{fact.Date}); err != nil {
		t.Fatalf("DeleteCachedAt() = %v", err)
	}
	loadedAt, err = store.LoadCachedAt(ctx, subjectID, environmentID, fact.Date)
	if err != nil || loadedAt != nil {
		t.Fatalf("LoadCachedAt(deleted) = (%v, %v)", loadedAt, err)
	}
	connectionID := uuid.NewString()
	claimToken := "scythe-claim-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status, sync_cursor) VALUES ($1::UUID, $2, $3, 'none', 'active', jsonb_build_object('sync_execution_claim_token', $4::STRING))`, connectionID, string(subjectID), string(environmentID), claimToken); err != nil {
		t.Fatalf("create fenced connection: %v", err)
	}
	fact.Metric.Value = 7
	if err := store.SaveConnectionActivity(ctx, connectionID, claimToken, activity.SaveEnvironmentsInput{}, activity.SaveFactsInput{Subject: subjectID, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatalf("SaveConnectionActivity() = %v", err)
	}
	var savedConnectionID string
	if err := pool.QueryRow(ctx, `SELECT provider_connection_id::STRING FROM activity_facts WHERE subject_id = $1 AND environment_id = $2 LIMIT 1`, string(subjectID), string(environmentID)).Scan(&savedConnectionID); err != nil || savedConnectionID != connectionID {
		t.Fatalf("fenced fact connection = (%s, %v)", savedConnectionID, err)
	}
	fact.Metric.Value = 8
	if err := store.SaveConnectionActivity(ctx, connectionID, "stale-claim", activity.SaveEnvironmentsInput{}, activity.SaveFactsInput{Subject: subjectID, Facts: []activity.Fact{fact}}); !errors.Is(err, integrations.ErrInvalidConnectionStatus) {
		t.Fatalf("stale claim error = %v", err)
	}
	var metric int
	if err := pool.QueryRow(ctx, `SELECT metric_value FROM activity_facts WHERE subject_id=$1 AND environment_id=$2 LIMIT 1`, string(subjectID), string(environmentID)).Scan(&metric); err != nil || metric != 7 {
		t.Fatalf("stale claim changed metric = (%d, %v)", metric, err)
	}
}

func TestGeneratedFencedActivityWithWorkerRole(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	workerDSN := os.Getenv("JANDIBAT_TEST_WORKER_DATABASE_URL")
	if adminDSN == "" || workerDSN == "" {
		t.Skip("set admin and worker Cockroach test URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	worker, err := pgxpool.New(ctx, workerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	store, err := New(worker)
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	subjectID := activity.SubjectID("scythe-worker-" + suffix)
	environmentID := activity.EnvironmentID("scythe-worker-env-" + suffix)
	connectionID := uuid.NewString()
	claimToken := "worker-claim-" + suffix
	if _, err := admin.Exec(ctx, `INSERT INTO subjects (id, handle, timezone, is_public) VALUES ($1, $1, 'UTC', true)`, string(subjectID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM subjects WHERE id=$1`, string(subjectID))
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM environments WHERE id=$1`, string(environmentID))
	})
	if _, err := admin.Exec(ctx, `INSERT INTO environments (id, key, name, scope) VALUES ($1, $1, 'Worker test', 'global')`, string(environmentID)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status, sync_cursor) VALUES ($1::UUID, $2, $3, 'none', 'active', jsonb_build_object('sync_execution_claim_token', $4::STRING))`, connectionID, string(subjectID), string(environmentID), claimToken); err != nil {
		t.Fatal(err)
	}
	fact := activity.Fact{Subject: subjectID, EnvironmentID: environmentID, Date: "2026-09-23", Action: activity.ActionCommit, Metric: activity.Metric{Name: activity.MetricCount, Value: 1}}
	if err := store.SaveConnectionActivity(ctx, connectionID, claimToken, activity.SaveEnvironmentsInput{}, activity.SaveFactsInput{Subject: subjectID, Facts: []activity.Fact{fact}}); err != nil {
		t.Fatalf("worker fenced write = %v", err)
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM activity_facts WHERE subject_id=$1 AND provider_connection_id=$2::UUID`, string(subjectID), connectionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("worker fenced fact count = (%d, %v)", count, err)
	}
}
