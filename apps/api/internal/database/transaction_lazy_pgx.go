package database

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrLazyPGXInactive = errors.New("database: lazy pgx transaction is inactive")

// PGXLazyPool is the shared pool surface needed by generated queries and the
// request audit boundary. Only the transaction owner commits or rolls back.
type PGXLazyPool interface {
	DBTX
	TxBeginner
}

type lazyPGXContextKey struct{}

// LazyPGXTransaction delays BEGIN until a generated write or an explicit InTx
// callback. Preflight reads therefore do not hold a transaction during OAuth
// or verifier network calls. It is request-scoped and must not be shared across
// concurrent handlers.
type LazyPGXTransaction struct {
	pool         PGXLazyPool
	poolIdentity uintptr
	ctx          context.Context
	mu           sync.Mutex
	tx           pgx.Tx
	closed       bool
}

func WithLazyPGXTransaction(ctx context.Context, pool PGXLazyPool) (context.Context, *LazyPGXTransaction) {
	identity, _ := transactionPoolIdentity(pool)
	lazy := &LazyPGXTransaction{pool: pool, poolIdentity: identity, ctx: ctx}
	return context.WithValue(ctx, lazyPGXContextKey{}, lazy), lazy
}

// HasPendingLazyPGXTransaction reports a request boundary that has not yet
// started a transaction. Adapters may preflight known no-op mutations before
// claiming durable state, while their conditional write remains authoritative.
func HasPendingLazyPGXTransaction(ctx context.Context, pool DBTX) bool {
	lazy, ok := ctx.Value(lazyPGXContextKey{}).(*LazyPGXTransaction)
	if !ok || lazy == nil || lazy.Active() || lazy.isClosed() {
		return false
	}
	identity, err := transactionPoolIdentity(pool)
	return err == nil && lazy.poolIdentity == identity
}

func (lazy *LazyPGXTransaction) Transaction() (pgx.Tx, bool) {
	if lazy == nil {
		return nil, false
	}
	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	return lazy.tx, lazy.tx != nil && !lazy.closed
}

func (lazy *LazyPGXTransaction) Active() bool {
	_, active := lazy.Transaction()
	return active
}

func (lazy *LazyPGXTransaction) isClosed() bool {
	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	return lazy.closed
}

func (lazy *LazyPGXTransaction) begin(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if lazy == nil || lazy.pool == nil || lazy.poolIdentity == 0 {
		return nil, ErrInvalidPool
	}
	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	if lazy.closed {
		return nil, ErrLazyPGXInactive
	}
	if lazy.tx != nil {
		return lazy.tx, nil
	}
	tx, err := lazy.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	lazy.tx = tx
	return tx, nil
}

func (lazy *LazyPGXTransaction) Commit() error {
	if lazy == nil {
		return ErrLazyPGXInactive
	}
	lazy.mu.Lock()
	if lazy.closed || lazy.tx == nil {
		lazy.mu.Unlock()
		return ErrLazyPGXInactive
	}
	tx := lazy.tx
	lazy.closed = true
	lazy.mu.Unlock()
	if err := tx.Commit(lazy.ctx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	return nil
}

func (lazy *LazyPGXTransaction) Rollback() error {
	if lazy == nil {
		return nil
	}
	lazy.mu.Lock()
	if lazy.closed {
		lazy.mu.Unlock()
		return nil
	}
	tx := lazy.tx
	lazy.closed = true
	lazy.mu.Unlock()
	if tx == nil {
		return nil
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	return tx.Rollback(rollbackCtx)
}

type lazyPGXExecutor struct {
	pool DBTX
	lazy *LazyPGXTransaction
}

func (executor lazyPGXExecutor) target(ctx context.Context, query string) (DBTX, error) {
	if executor.lazy.isClosed() {
		return nil, ErrLazyPGXInactive
	}
	if tx, active := executor.lazy.Transaction(); active {
		return tx, nil
	}
	if isReadOnlyPGXQuery(query) {
		return executor.pool, nil
	}
	return executor.lazy.begin(ctx, pgx.TxOptions{})
}

func isReadOnlyPGXQuery(query string) bool {
	trimmed := strings.TrimSpace(query)
	return strings.HasPrefix(strings.ToUpper(trimmed), "SELECT ")
}

func (executor lazyPGXExecutor) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	target, err := executor.target(ctx, query)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return target.Exec(ctx, query, args...)
}

func (executor lazyPGXExecutor) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	target, err := executor.target(ctx, query)
	if err != nil {
		return nil, err
	}
	return target.Query(ctx, query, args...)
}

func (executor lazyPGXExecutor) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	target, err := executor.target(ctx, query)
	if err != nil {
		return errorPGXRow{err: err}
	}
	return target.QueryRow(ctx, query, args...)
}

func (executor lazyPGXExecutor) Begin(ctx context.Context) (pgx.Tx, error) {
	return executor.lazy.begin(ctx, pgx.TxOptions{})
}

type errorPGXRow struct{ err error }

func (row errorPGXRow) Scan(...any) error { return row.err }

type errorPGXExecutor struct{ err error }

func (executor errorPGXExecutor) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, executor.err
}

func (executor errorPGXExecutor) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, executor.err
}

func (executor errorPGXExecutor) QueryRow(context.Context, string, ...any) pgx.Row {
	return errorPGXRow(executor)
}

func (executor errorPGXExecutor) Begin(context.Context) (pgx.Tx, error) {
	return nil, executor.err
}
