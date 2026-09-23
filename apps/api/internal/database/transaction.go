// Package database contains the pgx transaction boundary shared by generated
// queries and application services. It keeps CockroachDB retries outside
// generated SQL and gives nested services one transaction for a pool.
package database

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrTransactionPoolMismatch = errors.New("database: transaction belongs to another pool")
	ErrInvalidPool             = errors.New("database: transaction pool must be a non-nil pointer")
	ErrCallbackRequired        = errors.New("database: transaction callback is required")
)

const (
	defaultMaxAttempts = 5
	defaultBackoffMin  = 10 * time.Millisecond
	defaultBackoffMax  = 250 * time.Millisecond
	rollbackTimeout    = 5 * time.Second
)

// DBTX matches the Scythe Go pgx backend so generated queries accept either a
// pool or the pgx.Tx supplied to an application transaction callback.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// TxBeginner is implemented by *pgxpool.Pool and keeps the transaction runner
// testable without substituting SQL behavior inside its transaction logic.
type TxBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// RetryOptions configures transaction isolation and bounded serialization
// retries. The callback may execute more than once and must contain database
// work only; external side effects belong after InTx returns successfully.
type RetryOptions struct {
	TxOptions   pgx.TxOptions
	MaxAttempts int
	BackoffMin  time.Duration
	BackoffMax  time.Duration
}

type pgxTransactionContextKey struct{}

type activeTransaction struct {
	poolIdentity uintptr
	tx           pgx.Tx
}

// InTx owns a pgx transaction and retries CockroachDB serialization failures
// (SQLSTATE 40001) with bounded exponential jitter. Nested calls join the
// current transaction only when they use the same pool.
func InTx(
	ctx context.Context,
	pool TxBeginner,
	options RetryOptions,
	callback func(context.Context, pgx.Tx) error,
) error {
	if ctx == nil {
		return errors.New("database: transaction context is required")
	}
	if callback == nil {
		return ErrCallbackRequired
	}
	identity, err := transactionPoolIdentity(pool)
	if err != nil {
		return err
	}
	if active, ok := ctx.Value(pgxTransactionContextKey{}).(activeTransaction); ok {
		if active.poolIdentity != identity {
			return ErrTransactionPoolMismatch
		}
		return callback(ctx, active.tx)
	}
	options = options.withDefaults()
	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, beginErr := pool.BeginTx(ctx, options.TxOptions)
		if beginErr != nil {
			if !IsRetryableSerializationFailure(beginErr) || attempt == options.MaxAttempts {
				return beginErr
			}
			if err := waitForRetry(ctx, retryDelay(options, attempt)); err != nil {
				return err
			}
			continue
		}

		txContext := context.WithValue(ctx, pgxTransactionContextKey{}, activeTransaction{
			poolIdentity: identity,
			tx:           tx,
		})
		attemptErr := invokeCallback(txContext, tx, callback)
		if attemptErr == nil {
			attemptErr = tx.Commit(ctx)
		}
		retryable := IsRetryableSerializationFailure(attemptErr)
		if attemptErr != nil {
			if rollbackErr := rollback(tx); rollbackErr != nil {
				attemptErr = errors.Join(attemptErr, rollbackErr)
			}
		}
		if attemptErr == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !retryable || attempt == options.MaxAttempts {
			return attemptErr
		}
		if err := waitForRetry(ctx, retryDelay(options, attempt)); err != nil {
			return err
		}
	}
	return errors.New("database: transaction retry loop ended unexpectedly")
}

// IsRetryableSerializationFailure classifies wrapped Cockroach/PostgreSQL
// serialization errors without coupling generated query code to retry policy.
func IsRetryableSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

func transactionPoolIdentity(pool TxBeginner) (uintptr, error) {
	if pool == nil {
		return 0, ErrInvalidPool
	}
	value := reflect.ValueOf(pool)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return 0, ErrInvalidPool
	}
	return value.Pointer(), nil
}

func (options RetryOptions) withDefaults() RetryOptions {
	if options.MaxAttempts < 1 {
		options.MaxAttempts = defaultMaxAttempts
	}
	if options.BackoffMin <= 0 {
		options.BackoffMin = defaultBackoffMin
	}
	if options.BackoffMax <= 0 {
		options.BackoffMax = defaultBackoffMax
	}
	if options.BackoffMax < options.BackoffMin {
		options.BackoffMax = options.BackoffMin
	}
	return options
}

func retryDelay(options RetryOptions, attempt int) time.Duration {
	delay := options.BackoffMin
	for step := 1; step < attempt && delay < options.BackoffMax; step++ {
		if delay > options.BackoffMax/2 {
			delay = options.BackoffMax
			break
		}
		delay *= 2
	}
	if delay > options.BackoffMax {
		delay = options.BackoffMax
	}
	if delay <= 1 {
		return delay
	}
	return time.Duration(rand.Int63n(int64(delay) + 1))
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func rollback(tx pgx.Tx) error {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	err := tx.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("database: rollback transaction: %w", err)
	}
	return nil
}

func invokeCallback(
	ctx context.Context,
	tx pgx.Tx,
	callback func(context.Context, pgx.Tx) error,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = rollback(tx)
			panic(recovered)
		}
	}()
	return callback(ctx, tx)
}
