// Package cockroach persists activity domain objects in CockroachDB.
package cockroach

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

var ErrNilDB = errors.New("cockroach: pgx pool is required")

// Store implements activity.ActivityStore. The caller owns a pool passed to New.
type Store struct {
	pool  *pgxpool.Pool
	owned bool
}

var _ activity.ActivityStore = (*Store)(nil)

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, ErrNilDB
	}
	return &Store{pool: pool}, nil
}

// Open creates a pool owned by the returned store.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("cockroach: open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cockroach: ping database: %w", err)
	}
	return &Store{pool: pool, owned: true}, nil
}

func (s *Store) Close() error {
	if s != nil && s.pool != nil && s.owned {
		s.pool.Close()
	}
	return nil
}

func (s *Store) executor(ctx context.Context) appdb.DBTX { return appdb.PGXExecutorFor(ctx, s.pool) }

func (s *Store) inTx(ctx context.Context, callback func(context.Context, pgx.Tx) error) error {
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, callback)
}
