package adapters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

func TestLazyTransactionStartsOnFirstMutationAndJoinsCompoundWrites(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	ctx, lazy := appdb.WithLazyTransaction(context.Background(), db)
	if _, ok := appdb.Transaction(ctx, db); ok || len(script.Calls()) != 0 {
		t.Fatal("lazy request transaction began before a mutation")
	}
	first, err := appdb.MutationExecutor(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ExecContext(ctx, "UPDATE first_state SET value = 1"); err != nil {
		t.Fatal(err)
	}
	second, err := appdb.MutationExecutor(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ExecContext(ctx, "INSERT INTO second_state VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := lazy.Commit(); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 4 || calls[0].Operation != fakedb.Begin || calls[3].Operation != fakedb.Commit {
		t.Fatalf("compound transaction calls = %#v", calls)
	}
}

func TestLazyTransactionRejectsDifferentDatabase(t *testing.T) {
	firstScript := fakedb.New()
	first := firstScript.Open()
	t.Cleanup(func() { _ = first.Close() })
	secondScript := fakedb.New()
	second := secondScript.Open()
	t.Cleanup(func() { _ = second.Close() })
	ctx, _ := appdb.WithLazyTransaction(context.Background(), first)
	if _, err := appdb.MutationExecutor(ctx, second); !errors.Is(err, appdb.ErrTransactionDatabaseMismatch) {
		t.Fatalf("database mismatch error = %v", err)
	}
	if len(firstScript.Calls()) != 0 || len(secondScript.Calls()) != 0 {
		t.Fatal("database mismatch opened a transaction")
	}
}
