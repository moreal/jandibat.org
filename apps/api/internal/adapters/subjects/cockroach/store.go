// Package cockroach persists users, subjects, and preferences in CockroachDB.
package cockroach

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

var ErrNilDB = errors.New("subjects cockroach: pgx pool is required")

// Store implements subjects.Repository. The caller owns the pool.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, ErrNilDB
	}
	return &Store{pool: pool}, nil
}

var _ subjects.Repository = (*Store)(nil)

func (store *Store) executor(ctx context.Context) appdb.DBTX {
	return appdb.PGXExecutorFor(ctx, store.pool)
}

func (store *Store) inTx(ctx context.Context, callback func(context.Context, pgx.Tx) error) error {
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, callback)
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

func subjectFrom(id string, owner, display *string, handle, timezone string, public bool, created, updated time.Time) subjects.Subject {
	return subjects.Subject{ID: id, OwnerUserID: value(owner), Handle: handle, DisplayName: copyString(display), Timezone: timezone, IsPublic: public, CreatedAt: created, UpdatedAt: updated}
}

func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func copyString(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
