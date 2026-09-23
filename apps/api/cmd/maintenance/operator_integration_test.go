package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestMaintenanceDeleteCommandClaimsAndCompletesCanonicalRequest(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := operationsstore.NewWithPGXPool(db, pool)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := operations.NewAuditRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	userID, subjectID, requestID := "cli_user_"+suffix, "cli_subject_"+suffix, "cli-delete-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $1, 'UTC')`, subjectID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM audit_events WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM deletion_requests WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM deletion_request_inbox WHERE request_id = $1`, requestID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	now := time.Now().UTC()
	app := &maintenanceApplication{
		store: store, clock: fixedOperatorClock{now: now}, audit: audit,
		deletionPseudonymKey: bytes.Repeat([]byte{'p'}, 32),
	}
	var output bytes.Buffer
	err = runMaintenanceCommand(ctx, app, []string{
		"delete", "--request-id", requestID, "--target-type", "subject", "--target-id", subjectID,
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status":"completed"`) {
		t.Fatalf("operator output = %s", output.String())
	}
}

type fixedOperatorClock struct{ now time.Time }

func (clock fixedOperatorClock) Now() time.Time { return clock.now }
