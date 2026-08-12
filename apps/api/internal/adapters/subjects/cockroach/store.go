// Package cockroach persists users, subjects, and preferences in CockroachDB.
package cockroach

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

var ErrNilDB = errors.New("subjects cockroach: database is required")

// Store implements subjects.Repository. The caller owns the database handle.
type Store struct {
	db *sql.DB
}

func (store *Store) mutationExecutor(ctx context.Context) (appdb.Executor, error) {
	return appdb.MutationExecutor(ctx, store.db)
}

func (store *Store) beginMutation(ctx context.Context) (context.Context, *appdb.Scope, error) {
	return appdb.Begin(ctx, store.db, nil)
}

var _ subjects.Repository = (*Store)(nil)

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	return &Store{db: db}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func persistenceError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505":
		return errors.Join(subjects.ErrConflict, err)
	case "23503":
		return errors.Join(subjects.ErrNotFound, err)
	case "22000", "22001", "22P02", "23502", "23514":
		return errors.Join(subjects.ErrInvalidInput, err)
	default:
		return err
	}
}

func affected(result sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, persistenceError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}
