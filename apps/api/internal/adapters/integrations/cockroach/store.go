// Package cockroach persists provider integrations in CockroachDB.
package cockroach

import (
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var ErrNilDB = errors.New("integration cockroach: database is required")

// Store implements all persistence ports used by the integrations services.
// The caller owns and closes both configured handles.
type Store struct {
	db   *sql.DB
	pool *pgxpool.Pool
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

// NewWithPGXPool keeps legacy adapters on db while migrated query groups use
// pool. Both handles must target the same database and runtime role.
func NewWithPGXPool(db *sql.DB, pool *pgxpool.Pool) (*Store, error) {
	if db == nil || pool == nil {
		return nil, ErrNilDB
	}
	return &Store{db: db, pool: pool}, nil
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
