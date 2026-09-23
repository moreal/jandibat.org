// Package cockroach persists provider integrations in CockroachDB.
package cockroach

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var ErrNilDB = errors.New("integration cockroach: database is required")

// Store implements all persistence ports used by the integrations services.
// The caller owns and closes the configured pool.
type Store struct {
	pool *pgxpool.Pool
}

var (
	_ integrations.ConnectionStore          = (*Store)(nil)
	_ integrations.ConnectionPageStore      = (*Store)(nil)
	_ integrations.CustomProviderStore      = (*Store)(nil)
	_ integrations.CustomProviderPageStore  = (*Store)(nil)
	_ integrations.SyncJobStore             = (*Store)(nil)
	_ integrations.SyncIdempotencyStore     = (*Store)(nil)
	_ integrations.SyncExecutionStore       = (*Store)(nil)
	_ integrations.SyncConnectionStateStore = (*Store)(nil)
)

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, ErrNilDB
	}
	return &Store{pool: pool}, nil
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
