package apihttp_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
)

func newPGXSubjectStore(t *testing.T, ctx context.Context, dsn string) *subjectstore.Store {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := subjectstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
