// Package database provides a transitional bridge for adapters that still use
// database/sql. New Scythe-generated queries use the pgx transaction boundary in
// transaction.go and this compatibility file is removed after adapter migration.
package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

var ErrTransactionDatabaseMismatch = errors.New("database: transaction belongs to another database")

type transactionContextKey struct{}

type transactionContext struct {
	db *sql.DB
	tx *sql.Tx
}

type lazyTransactionContextKey struct{}

// LazyTransaction delays BEGIN until a participating write adapter calls
// Begin. Reads and external provider/WebAuthn/SMTP work therefore do not hold
// an idle Cockroach transaction open.
type LazyTransaction struct {
	db *sql.DB
	mu sync.Mutex
	tx *sql.Tx
}

func WithLazyTransaction(ctx context.Context, db *sql.DB) (context.Context, *LazyTransaction) {
	lazy := &LazyTransaction{db: db}
	return context.WithValue(ctx, lazyTransactionContextKey{}, lazy), lazy
}

func (lazy *LazyTransaction) Transaction() (*sql.Tx, bool) {
	if lazy == nil {
		return nil, false
	}
	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	return lazy.tx, lazy.tx != nil
}

func (lazy *LazyTransaction) begin(ctx context.Context, db *sql.DB, options *sql.TxOptions) (*sql.Tx, error) {
	if lazy == nil || lazy.db != db {
		return nil, ErrTransactionDatabaseMismatch
	}
	lazy.mu.Lock()
	defer lazy.mu.Unlock()
	if lazy.tx != nil {
		return lazy.tx, nil
	}
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	lazy.tx = tx
	return tx, nil
}

func (lazy *LazyTransaction) Commit() error {
	tx, ok := lazy.Transaction()
	if !ok {
		return ErrTransactionDatabaseMismatch
	}
	return tx.Commit()
}

func (lazy *LazyTransaction) Rollback() error {
	tx, ok := lazy.Transaction()
	if !ok {
		return nil
	}
	return tx.Rollback()
}

// WithTransaction binds tx to ctx for adapters backed by db.
func WithTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, transactionContextKey{}, transactionContext{db: db, tx: tx})
}

// Transaction returns the request transaction only when it belongs to db.
func Transaction(ctx context.Context, db *sql.DB) (*sql.Tx, bool) {
	value, ok := ctx.Value(transactionContextKey{}).(transactionContext)
	if ok && value.db == db && value.tx != nil {
		return value.tx, true
	}
	if lazy, lazyOK := ctx.Value(lazyTransactionContextKey{}).(*LazyTransaction); lazyOK && lazy.db == db {
		return lazy.Transaction()
	}
	return nil, false
}

// HasLazyTransaction reports whether ctx carries the audited request boundary
// for db, even before its first mutation has started the SQL transaction.
func HasLazyTransaction(ctx context.Context, db *sql.DB) bool {
	lazy, ok := ctx.Value(lazyTransactionContextKey{}).(*LazyTransaction)
	return ok && lazy != nil && lazy.db == db
}

// Executor is implemented by both *sql.DB and *sql.Tx.
type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ExecutorFor(ctx context.Context, db *sql.DB) Executor {
	if tx, ok := Transaction(ctx, db); ok {
		return tx
	}
	return db
}

// MutationExecutor joins (and lazily starts) a request transaction when one
// is installed. Outside an audited request it preserves the adapter's normal
// single-statement autocommit behavior.
func MutationExecutor(ctx context.Context, db *sql.DB) (Executor, error) {
	if tx, ok := Transaction(ctx, db); ok {
		return tx, nil
	}
	if lazy, ok := ctx.Value(lazyTransactionContextKey{}).(*LazyTransaction); ok {
		return lazy.begin(ctx, db, nil)
	}
	return db, nil
}

// Scope joins an existing request transaction or owns a new local one.
type Scope struct {
	Tx    *sql.Tx
	owned bool
}

func Begin(ctx context.Context, db *sql.DB, options *sql.TxOptions) (context.Context, *Scope, error) {
	if tx, ok := Transaction(ctx, db); ok {
		return ctx, &Scope{Tx: tx}, nil
	}
	if lazy, ok := ctx.Value(lazyTransactionContextKey{}).(*LazyTransaction); ok {
		tx, err := lazy.begin(ctx, db, options)
		if err != nil {
			return ctx, nil, err
		}
		return WithTransaction(ctx, db, tx), &Scope{Tx: tx}, nil
	}
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return ctx, nil, err
	}
	return WithTransaction(ctx, db, tx), &Scope{Tx: tx, owned: true}, nil
}

func (scope *Scope) Commit() error {
	if scope == nil || !scope.owned {
		return nil
	}
	return scope.Tx.Commit()
}

func (scope *Scope) Rollback() error {
	if scope == nil || !scope.owned {
		return nil
	}
	return scope.Tx.Rollback()
}
