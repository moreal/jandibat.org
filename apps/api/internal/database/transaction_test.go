package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type scriptedPool struct {
	txs         []*scriptedTx
	beginErrors []error
	beginCalls  int
}

func (pool *scriptedPool) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	pool.beginCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(pool.beginErrors) != 0 {
		err := pool.beginErrors[0]
		pool.beginErrors = pool.beginErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(pool.txs) == 0 {
		return nil, errors.New("test pool exhausted")
	}
	tx := pool.txs[0]
	pool.txs = pool.txs[1:]
	return tx, nil
}

type scriptedTx struct {
	pgx.Tx
	commitErr   error
	rollbackErr error
	commits     int
	rollbacks   int
}

func (tx *scriptedTx) Commit(context.Context) error {
	tx.commits++
	return tx.commitErr
}

func (tx *scriptedTx) Rollback(context.Context) error {
	tx.rollbacks++
	return tx.rollbackErr
}

func noDelayOptions(maxAttempts int) RetryOptions {
	return RetryOptions{MaxAttempts: maxAttempts, BackoffMin: time.Nanosecond, BackoffMax: time.Nanosecond}
}

func serializationFailure() error {
	return &pgconn.PgError{Code: "40001", Message: "serialization failure"}
}

func TestInTxCommitsSuccessfulCallback(t *testing.T) {
	tx := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{tx}}
	err := InTx(context.Background(), pool, noDelayOptions(1), func(_ context.Context, got pgx.Tx) error {
		if got != tx {
			t.Fatalf("callback transaction = %p, want %p", got, tx)
		}
		return nil
	})
	if err != nil || tx.commits != 1 || tx.rollbacks != 0 || pool.beginCalls != 1 {
		t.Fatalf("InTx() = %v, commit=%d rollback=%d begin=%d", err, tx.commits, tx.rollbacks, pool.beginCalls)
	}
}

func TestInTxRollsBackCallbackError(t *testing.T) {
	want := errors.New("callback failed")
	tx := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{tx}}
	err := InTx(context.Background(), pool, noDelayOptions(3), func(context.Context, pgx.Tx) error { return want })
	if !errors.Is(err, want) || tx.commits != 0 || tx.rollbacks != 1 || pool.beginCalls != 1 {
		t.Fatalf("InTx() = %v, commit=%d rollback=%d begin=%d", err, tx.commits, tx.rollbacks, pool.beginCalls)
	}
}

func TestInTxRollsBackBeforePropagatingCallbackPanic(t *testing.T) {
	tx := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{tx}}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = InTx(context.Background(), pool, noDelayOptions(1), func(context.Context, pgx.Tx) error {
			panic("callback panic")
		})
	}()
	if recovered != "callback panic" || tx.rollbacks != 1 || tx.commits != 0 {
		t.Fatalf("recovered=%v rollback=%d commit=%d", recovered, tx.rollbacks, tx.commits)
	}
}

func TestInTxRetriesSerializationFailureAndRunsCallbackAgain(t *testing.T) {
	first, second := &scriptedTx{}, &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{first, second}}
	attempts := 0
	err := InTx(context.Background(), pool, noDelayOptions(2), func(context.Context, pgx.Tx) error {
		attempts++
		if attempts == 1 {
			return serializationFailure()
		}
		return nil
	})
	if err != nil || attempts != 2 || pool.beginCalls != 2 || first.rollbacks != 1 || second.commits != 1 {
		t.Fatalf("InTx() = %v, attempts=%d begin=%d first rollback=%d second commit=%d", err, attempts, pool.beginCalls, first.rollbacks, second.commits)
	}
}

func TestInTxRetriesSerializationFailureOnCommit(t *testing.T) {
	first := &scriptedTx{commitErr: serializationFailure()}
	second := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{first, second}}
	attempts := 0
	err := InTx(context.Background(), pool, noDelayOptions(2), func(context.Context, pgx.Tx) error {
		attempts++
		return nil
	})
	if err != nil || attempts != 2 || pool.beginCalls != 2 || second.commits != 1 {
		t.Fatalf("InTx() = %v, attempts=%d begin=%d second commit=%d", err, attempts, pool.beginCalls, second.commits)
	}
}

func TestInTxStopsAfterRetryExhaustion(t *testing.T) {
	transactions := []*scriptedTx{{}, {}, {}}
	pool := &scriptedPool{txs: transactions}
	attempts := 0
	err := InTx(context.Background(), pool, noDelayOptions(3), func(context.Context, pgx.Tx) error {
		attempts++
		return serializationFailure()
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" || attempts != 3 || pool.beginCalls != 3 {
		t.Fatalf("InTx() = %v, attempts=%d begin=%d", err, attempts, pool.beginCalls)
	}
	for i, tx := range transactions {
		if tx.rollbacks != 1 {
			t.Errorf("attempt %d rollback count = %d", i+1, tx.rollbacks)
		}
	}
}

func TestInTxDoesNotRetryWhenOnlyRollbackReturnsSerializationFailure(t *testing.T) {
	callbackErr := errors.New("callback failed")
	first := &scriptedTx{rollbackErr: serializationFailure()}
	second := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{first, second}}
	err := InTx(context.Background(), pool, noDelayOptions(2), func(context.Context, pgx.Tx) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || pool.beginCalls != 1 || first.rollbacks != 1 {
		t.Fatalf("InTx() = %v, begin=%d rollback=%d", err, pool.beginCalls, first.rollbacks)
	}
}

func TestInTxStopsWhenContextIsCanceledDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tx := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{tx}}
	err := InTx(ctx, pool, noDelayOptions(5), func(context.Context, pgx.Tx) error {
		cancel()
		return serializationFailure()
	})
	if !errors.Is(err, context.Canceled) || pool.beginCalls != 1 || tx.rollbacks != 1 {
		t.Fatalf("InTx() = %v, begin=%d rollback=%d", err, pool.beginCalls, tx.rollbacks)
	}
}

func TestInTxReusesNestedTransactionForSamePool(t *testing.T) {
	tx := &scriptedTx{}
	pool := &scriptedPool{txs: []*scriptedTx{tx}}
	err := InTx(context.Background(), pool, noDelayOptions(1), func(ctx context.Context, outer pgx.Tx) error {
		return InTx(ctx, pool, noDelayOptions(5), func(_ context.Context, inner pgx.Tx) error {
			if inner != outer {
				t.Fatalf("nested transaction = %p, outer = %p", inner, outer)
			}
			return nil
		})
	})
	if err != nil || pool.beginCalls != 1 || tx.commits != 1 {
		t.Fatalf("nested InTx() = %v, begin=%d commit=%d", err, pool.beginCalls, tx.commits)
	}
}

func TestInTxRejectsNestedTransactionFromAnotherPool(t *testing.T) {
	firstTx := &scriptedTx{}
	first := &scriptedPool{txs: []*scriptedTx{firstTx}}
	second := &scriptedPool{txs: []*scriptedTx{{}}}
	err := InTx(context.Background(), first, noDelayOptions(1), func(ctx context.Context, _ pgx.Tx) error {
		return InTx(ctx, second, noDelayOptions(1), func(context.Context, pgx.Tx) error { return nil })
	})
	if !errors.Is(err, ErrTransactionPoolMismatch) || firstTx.rollbacks != 1 || second.beginCalls != 0 {
		t.Fatalf("nested InTx() = %v, outer rollback=%d other begin=%d", err, firstTx.rollbacks, second.beginCalls)
	}
}

func TestIsRetryableSerializationFailureFollowsWrappedPostgresError(t *testing.T) {
	if !IsRetryableSerializationFailure(errors.Join(errors.New("wrapped"), serializationFailure())) {
		t.Fatal("wrapped SQLSTATE 40001 was not classified as retryable")
	}
	if IsRetryableSerializationFailure(errors.New("unrelated database error")) {
		t.Fatal("unrelated database error was classified as retryable")
	}
}

func TestInTxUsesConfiguredPGXTransactionOptions(t *testing.T) {
	pool := &capturingPool{scriptedPool: &scriptedPool{txs: []*scriptedTx{{}}}}
	options := noDelayOptions(1)
	options.TxOptions = pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly}
	if err := InTx(context.Background(), pool, options, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly}
	if !reflect.DeepEqual(pool.options, want) {
		t.Fatalf("BeginTx options = %+v, want %+v", pool.options, want)
	}
}

type capturingPool struct {
	*scriptedPool
	options pgx.TxOptions
}

func (pool *capturingPool) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	pool.options = options
	return pool.scriptedPool.BeginTx(ctx, options)
}
