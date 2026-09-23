package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// Purging facts changes a snapshot even after the source rows have disappeared.
// A second, empty purge must not change that provenance.
func TestCockroachRetentionPreservesActivitySnapshotDeletionProvenance(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if adminDSN == "" || maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	pool, err := pgxpool.New(ctx, maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	userID := "retention-provenance-user-" + suffix
	subjectID := "retention-provenance-subject-" + suffix
	publicID := "retention-provenance-public-" + suffix
	privateID := "retention-provenance-private-" + suffix
	globalID := "retention-provenance-global-" + suffix
	for _, fixture := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id,primary_email,status) VALUES ($1,$2,'active')`, []any{userID, userID + "@example.invalid"}},
		{`INSERT INTO subjects (id,owner_user_id,handle,timezone) VALUES ($1,$2,$3,'UTC')`, []any{subjectID, userID, "retention-" + suffix}},
		{`INSERT INTO environments (id,key,name,scope,owner_subject_id) VALUES ($1,$1,$1,'subject',$2)`, []any{publicID, subjectID}},
		{`INSERT INTO environments (id,key,name,scope,owner_subject_id,metadata) VALUES ($1,$1,$1,'subject',$2,'{"visibility":"private"}'::JSONB)`, []any{privateID, subjectID}},
		{`INSERT INTO environments (id,key,name,scope,metadata) VALUES ($1,$1,$1,'global','{"visibility":"private"}'::JSONB)`, []any{globalID}},
		{`INSERT INTO activity_facts (subject_id,environment_id,activity_date,action,metric_name,metric_value) VALUES ($1,$2,'0001-01-01','integration','count',1)`, []any{subjectID, publicID}},
		{`INSERT INTO activity_facts (subject_id,environment_id,activity_date,action,metric_name,metric_value) VALUES ($1,$2,'0001-01-01','integration','count',1)`, []any{subjectID, privateID}},
		{`INSERT INTO activity_facts (subject_id,environment_id,activity_date,action,metric_name,metric_value) VALUES ($1,$2,'0001-01-01','integration','count',1)`, []any{subjectID, globalID}},
		{`INSERT INTO activity_facts (subject_id,environment_id,activity_date,action,metric_name,metric_value) VALUES ($1,$2,'0002-01-01','integration','count',1)`, []any{subjectID, publicID}},
	} {
		if _, err := admin.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM activity_facts WHERE subject_id=$1`, subjectID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM environments WHERE id IN ($1,$2,$3)`, publicID, privateID, globalID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM subjects WHERE id=$1`, subjectID)
		_, _ = admin.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	request := operations.RetentionPurgeRequest{
		Dataset: operations.RetentionActivityFacts,
		Before:  time.Date(2, 1, 1, 0, 0, 0, 0, time.UTC),
		AsOf:    time.Now().UTC(),
		Limit:   3,
	}
	deleted, err := store.PurgeExpired(ctx, request)
	if err != nil || deleted != 3 {
		t.Fatalf("purge deleted=%d err=%v, want 3", deleted, err)
	}
	var remaining int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM activity_facts WHERE subject_id=$1`, subjectID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("remaining facts=%d err=%v, want 1", remaining, err)
	}
	markerTimes := make(map[string]time.Time)
	rows, err := admin.QueryContext(ctx, `SELECT environment_id,visibility_scope,changed_at FROM activity_snapshot_changes WHERE subject_id=$1 AND activity_date='0001-01-01'`, subjectID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var environmentID, scope string
		var changedAt time.Time
		if err := rows.Scan(&environmentID, &scope, &changedAt); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		markerTimes[environmentID+":"+scope] = changedAt
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(markerTimes) != 3 || markerTimes[publicID+":public"].IsZero() || markerTimes[privateID+":private"].IsZero() || markerTimes[globalID+":public"].IsZero() {
		t.Fatalf("deletion provenance=%v, want public, private, and globally public markers", markerTimes)
	}
	if deleted, err := store.PurgeExpired(ctx, request); err != nil || deleted != 0 {
		t.Fatalf("empty purge deleted=%d err=%v, want 0", deleted, err)
	}
	for key, want := range markerTimes {
		parts := strings.SplitN(key, ":", 2)
		var got time.Time
		if err := admin.QueryRowContext(ctx, `SELECT changed_at FROM activity_snapshot_changes WHERE subject_id=$1 AND environment_id=$2 AND activity_date='0001-01-01' AND visibility_scope=$3`, subjectID, parts[0], parts[1]).Scan(&got); err != nil || !got.Equal(want) {
			t.Fatalf("unchanged marker %s changed_at=%v want=%v err=%v", key, got, want, err)
		}
	}
	var future time.Time
	if err := admin.QueryRowContext(ctx, `UPDATE activity_snapshot_changes SET changed_at=now() + INTERVAL '1 hour' WHERE subject_id=$1 AND environment_id=$2 AND activity_date='0001-01-01' AND visibility_scope='public' RETURNING changed_at`, subjectID, publicID).Scan(&future); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO activity_facts (subject_id,environment_id,activity_date,action,metric_name,metric_value) VALUES ($1,$2,'0001-01-01','integration-again','count',1)`, subjectID, publicID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.PurgeExpired(ctx, request); err != nil || deleted != 1 {
		t.Fatalf("second actual purge deleted=%d err=%v, want 1", deleted, err)
	}
	var advanced time.Time
	if err := admin.QueryRowContext(ctx, `SELECT changed_at FROM activity_snapshot_changes WHERE subject_id=$1 AND environment_id=$2 AND activity_date='0001-01-01' AND visibility_scope='public'`, subjectID, publicID).Scan(&advanced); err != nil || !advanced.After(future) {
		t.Fatalf("second actual deletion did not advance public marker: before=%v after=%v err=%v", future, advanced, err)
	}
	var privateUnchanged time.Time
	if err := admin.QueryRowContext(ctx, `SELECT changed_at FROM activity_snapshot_changes WHERE subject_id=$1 AND environment_id=$2 AND activity_date='0001-01-01' AND visibility_scope='private'`, subjectID, privateID).Scan(&privateUnchanged); err != nil || !privateUnchanged.Equal(markerTimes[privateID+":private"]) {
		t.Fatalf("private marker changed during public deletion: got=%v err=%v", privateUnchanged, err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO activity_facts (subject_id,environment_id,activity_date,action,metric_name,metric_value) VALUES ($1,$2,'0001-01-01','integration-rollback','count',1)`, subjectID, publicID); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback provenance purge")
	err = appdb.InTx(ctx, pool, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		deleted, err := store.PurgeExpired(txctx, request)
		if err != nil || deleted != 1 {
			return fmt.Errorf("transactional purge deleted=%d err=%v, want 1", deleted, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback error=%v, want sentinel", err)
	}
	var rolledBackFacts int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM activity_facts WHERE subject_id=$1 AND action='integration-rollback'`, subjectID).Scan(&rolledBackFacts); err != nil || rolledBackFacts != 1 {
		t.Fatalf("rolled-back fact count=%d err=%v, want 1", rolledBackFacts, err)
	}
	var rolledBackMarker time.Time
	if err := admin.QueryRowContext(ctx, `SELECT changed_at FROM activity_snapshot_changes WHERE subject_id=$1 AND environment_id=$2 AND activity_date='0001-01-01' AND visibility_scope='public'`, subjectID, publicID).Scan(&rolledBackMarker); err != nil || !rolledBackMarker.Equal(advanced) {
		t.Fatalf("marker escaped rollback: got=%v want=%v err=%v", rolledBackMarker, advanced, err)
	}
}
