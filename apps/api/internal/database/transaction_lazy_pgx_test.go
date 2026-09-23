package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type lazyPGXTestPool struct {
	tx     *lazyPGXTestTx
	begins int
	reads  int
}

func (pool *lazyPGXTestPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	pool.begins++
	return pool.tx, nil
}

func (pool *lazyPGXTestPool) Begin(ctx context.Context) (pgx.Tx, error) {
	return pool.BeginTx(ctx, pgx.TxOptions{})
}

func (*lazyPGXTestPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("write bypassed lazy transaction")
}

func (*lazyPGXTestPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected pool Query")
}

func (pool *lazyPGXTestPool) QueryRow(context.Context, string, ...any) pgx.Row {
	pool.reads++
	return lazyPGXTestRow{}
}

type lazyPGXTestTx struct {
	pgx.Tx
	writes    int
	commits   int
	rollbacks int
}

func (tx *lazyPGXTestTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	tx.writes++
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tx *lazyPGXTestTx) QueryRow(context.Context, string, ...any) pgx.Row {
	tx.writes++
	return lazyPGXTestRow{}
}

func (tx *lazyPGXTestTx) Commit(context.Context) error {
	tx.commits++
	return nil
}

func (tx *lazyPGXTestTx) Rollback(context.Context) error {
	tx.rollbacks++
	return nil
}

type lazyPGXTestRow struct{}

func (lazyPGXTestRow) Scan(dest ...any) error {
	*dest[0].(*int) = 1
	return nil
}

func TestLazyPGXStartsOnMutationButNotPreflightRead(t *testing.T) {
	pool := &lazyPGXTestPool{tx: &lazyPGXTestTx{}}
	ctx, lazy := WithLazyPGXTransaction(context.Background(), pool)
	var read int
	if err := PGXExecutorFor(ctx, pool).QueryRow(ctx, "SELECT 1").Scan(&read); err != nil || read != 1 {
		t.Fatalf("preflight read = (%d, %v)", read, err)
	}
	if pool.begins != 0 || pool.reads != 1 || lazy.Active() {
		t.Fatalf("read started transaction: begins=%d reads=%d active=%t", pool.begins, pool.reads, lazy.Active())
	}
	if _, err := PGXExecutorFor(ctx, pool).Exec(ctx, "UPDATE users SET status='active'"); err != nil {
		t.Fatal(err)
	}
	if pool.begins != 1 || pool.tx.writes != 1 || !lazy.Active() {
		t.Fatalf("mutation boundary: begins=%d writes=%d active=%t", pool.begins, pool.tx.writes, lazy.Active())
	}
	if err := lazy.Commit(); err != nil || pool.tx.commits != 1 || lazy.Active() {
		t.Fatalf("commit = %v commits=%d active=%t", err, pool.tx.commits, lazy.Active())
	}
}

func TestInTxJoinsLazyPGXWithoutCommittingIt(t *testing.T) {
	pool := &lazyPGXTestPool{tx: &lazyPGXTestTx{}}
	ctx, lazy := WithLazyPGXTransaction(context.Background(), pool)
	if err := InTx(ctx, pool, RetryOptions{}, func(_ context.Context, got pgx.Tx) error {
		if got != pool.tx {
			t.Fatalf("callback transaction = %p, want %p", got, pool.tx)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if pool.begins != 1 || pool.tx.commits != 0 || !lazy.Active() {
		t.Fatalf("joined transaction: begins=%d commits=%d active=%t", pool.begins, pool.tx.commits, lazy.Active())
	}
	if err := lazy.Rollback(); err != nil || pool.tx.rollbacks != 1 || lazy.Active() {
		t.Fatalf("rollback = %v rollbacks=%d active=%t", err, pool.tx.rollbacks, lazy.Active())
	}
}

func TestLazyPGXStartsForReturningMutation(t *testing.T) {
	pool := &lazyPGXTestPool{tx: &lazyPGXTestTx{}}
	ctx, lazy := WithLazyPGXTransaction(context.Background(), pool)
	var value int
	if err := PGXExecutorFor(ctx, pool).QueryRow(ctx, "UPDATE users SET status='active' RETURNING 1").Scan(&value); err != nil || value != 1 {
		t.Fatalf("returning mutation = (%d, %v)", value, err)
	}
	if pool.begins != 1 || pool.reads != 0 || pool.tx.writes != 1 || !lazy.Active() {
		t.Fatalf("returning mutation escaped lazy tx: begins=%d reads=%d writes=%d active=%t", pool.begins, pool.reads, pool.tx.writes, lazy.Active())
	}
	_ = lazy.Rollback()
}

func TestLazyPGXRejectsReadsAfterRollback(t *testing.T) {
	pool := &lazyPGXTestPool{tx: &lazyPGXTestTx{}}
	ctx, lazy := WithLazyPGXTransaction(context.Background(), pool)
	if err := lazy.Rollback(); err != nil {
		t.Fatal(err)
	}
	var value int
	if err := PGXExecutorFor(ctx, pool).QueryRow(ctx, "SELECT 1").Scan(&value); !errors.Is(err, ErrLazyPGXInactive) {
		t.Fatalf("read after rollback = (%d, %v)", value, err)
	}
}

func TestLazyPGXRejectsDifferentPoolInsteadOfEscapingAudit(t *testing.T) {
	first := &lazyPGXTestPool{tx: &lazyPGXTestTx{}}
	second := &lazyPGXTestPool{tx: &lazyPGXTestTx{}}
	ctx, lazy := WithLazyPGXTransaction(context.Background(), first)
	var value int
	if err := PGXExecutorFor(ctx, second).QueryRow(ctx, "SELECT 1").Scan(&value); !errors.Is(err, ErrTransactionPoolMismatch) {
		t.Fatalf("other-pool read = (%d, %v)", value, err)
	}
	if second.reads != 0 || second.begins != 0 || lazy.Active() {
		t.Fatalf("other pool escaped audit: reads=%d begins=%d active=%t", second.reads, second.begins, lazy.Active())
	}
}
