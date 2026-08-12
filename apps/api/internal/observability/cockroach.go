package observability

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const cockroachSerializationFailure = "40001"

// CockroachQueryTracer observes errors at the pgx transport boundary. This
// covers database/sql Query, QueryRow, Exec, and transaction COMMIT calls
// without relying on each repository to remember an instrumentation helper.
type CockroachQueryTracer struct {
	registry *Registry
}

type poolAcquireStartKey struct{}

var (
	_ pgx.QueryTracer       = (*CockroachQueryTracer)(nil)
	_ pgxpool.AcquireTracer = (*CockroachQueryTracer)(nil)
)

// NewCockroachQueryTracer constructs the tracer installed by OpenDatabase.
func NewCockroachQueryTracer(registry *Registry) *CockroachQueryTracer {
	if registry == nil {
		registry = Default()
	}
	return &CockroachQueryTracer{registry: registry}
}

func (*CockroachQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (tracer *CockroachQueryTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if tracer == nil || tracer.registry == nil || data.Err == nil {
		return
	}
	outcome := "driver_error"
	var pgErr *pgconn.PgError
	if errors.As(data.Err, &pgErr) {
		outcome = "sql_error"
		if pgErr.Code == cockroachSerializationFailure {
			// Cockroach can surface SQLSTATE 40001 on a statement or COMMIT.
			// This application has no automatic transaction replay loop, so either
			// location is a final retry-required transaction failure.
			outcome = "retry_required"
		}
	}
	tracer.registry.ObserveCockroachTransactionError(outcome)
}

// TraceAcquireStart and TraceAcquireEnd measure the pgxpool checkout queue.
// OpenDatabase deliberately leaves database/sql unlimited and idle-free, so
// every logical connection checkout reaches this one, traced queue.
func (*CockroachQueryTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return context.WithValue(ctx, poolAcquireStartKey{}, time.Now())
}

func (tracer *CockroachQueryTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireEndData) {
	if tracer == nil || tracer.registry == nil {
		return
	}
	started, ok := ctx.Value(poolAcquireStartKey{}).(time.Time)
	if !ok {
		return
	}
	tracer.registry.ObserveDBPoolWait(time.Since(started))
}
