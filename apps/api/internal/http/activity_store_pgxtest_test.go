package apihttp_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
)

func newPGXActivityStore(t *testing.T, ctx context.Context, dsn string) *activitystore.Store {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := activitystore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
