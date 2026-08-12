package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCockroachQueryTracerClassifiesBoundedErrorOutcomes(t *testing.T) {
	registry := NewRegistry(Resource{Environment: "test"})
	tracer := NewCockroachQueryTracer(registry)
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "40001"}})
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505"}})
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: errors.New("connection reset")})
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, fragment := range []string{
		`cockroach_transaction_errors_total{outcome="retry_required",build_sha="unknown",environment="test",region="unknown"} 1`,
		`cockroach_transaction_errors_total{outcome="sql_error",build_sha="unknown",environment="test",region="unknown"} 1`,
		`cockroach_transaction_errors_total{outcome="driver_error",build_sha="unknown",environment="test",region="unknown"} 1`,
	} {
		if !strings.Contains(recorder.Body.String(), fragment) {
			t.Errorf("Cockroach metric missing %q:\n%s", fragment, recorder.Body.String())
		}
	}
}

func TestCockroachQueryTracerMeasuresPoolAcquisition(t *testing.T) {
	registry := NewRegistry(Resource{Environment: "test"})
	tracer := NewCockroachQueryTracer(registry)
	ctx := tracer.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	tracer.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, fragment := range []string{
		`db_pool_wait_duration_seconds_count{build_sha="unknown",environment="test",region="unknown"} 1`,
		`observability_capability{capability="db_pool_wait_p99",state="implemented",build_sha="unknown",environment="test",region="unknown"} 1`,
	} {
		if !strings.Contains(recorder.Body.String(), fragment) {
			t.Errorf("pool acquire metric missing %q:\n%s", fragment, recorder.Body.String())
		}
	}
}
