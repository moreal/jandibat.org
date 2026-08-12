// Package cockroach persists provider integrations in CockroachDB.
package cockroach

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var ErrNilDB = errors.New("integration cockroach: database is required")

// Store implements all persistence ports used by the integrations services.
// The caller owns db and is responsible for closing it.
type Store struct {
	db *sql.DB
}

func (s *Store) mutationExecutor(ctx context.Context) (appdb.Executor, error) {
	return appdb.MutationExecutor(ctx, s.db)
}

func (s *Store) beginMutation(ctx context.Context) (context.Context, *appdb.Scope, error) {
	return appdb.Begin(ctx, s.db, nil)
}

var (
	_ integrations.ConnectionStore          = (*Store)(nil)
	_ integrations.CustomProviderStore      = (*Store)(nil)
	_ integrations.SyncJobStore             = (*Store)(nil)
	_ integrations.SyncIdempotencyStore     = (*Store)(nil)
	_ integrations.SyncExecutionStore       = (*Store)(nil)
	_ integrations.SyncConnectionStateStore = (*Store)(nil)
)

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	return &Store{db: db}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func persistenceError(err error, duplicate error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if duplicate != nil {
				return duplicate
			}
			return integrations.ErrConflict
		case "22P02":
			return errors.Join(integrations.ErrInvalidIdentifier, err)
		}
	}
	return err
}

func notFound(entity, id string) error {
	return errors.Join(integrations.ErrNotFound, errors.New(entity+" "+id))
}
