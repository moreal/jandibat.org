// Package cockroach persists activity domain objects in CockroachDB.
package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

var ErrNilDB = errors.New("cockroach: database is required")

// Store implements activity.ActivityStore using database/sql and CockroachDB.
type Store struct {
	db *sql.DB
}

func (s *Store) beginMutation(ctx context.Context) (context.Context, *appdb.Scope, error) {
	return appdb.Begin(ctx, s.db, nil)
}

var _ activity.ActivityStore = (*Store)(nil)

// New constructs a Store around an existing database handle. The caller owns
// the handle and remains responsible for closing it.
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	return &Store{db: db}, nil
}

// Open creates and verifies a pgx-backed database/sql connection pool. The
// returned Store owns the pool; call Close when it is no longer needed.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("cockroach: open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cockroach: ping database: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
