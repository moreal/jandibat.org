// Package cockroach persists authentication state in CockroachDB.
package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

var ErrNilDB = errors.New("auth cockroach: database is required")

// Store implements auth.Repository using database/sql. The caller owns db.
type Store struct {
	db                      *sql.DB
	deletedIdentityHMACKeys []identityHMACKey
}

func (store *Store) mutationExecutor(ctx context.Context) (appdb.Executor, error) {
	return appdb.MutationExecutor(ctx, store.db)
}

func (store *Store) beginMutation(ctx context.Context) (context.Context, *appdb.Scope, error) {
	return appdb.Begin(ctx, store.db, nil)
}

type identityHMACKey struct {
	id       string
	material []byte
}

var _ coreauth.Repository = (*Store)(nil)

func New(db *sql.DB) (*Store, error) {
	return NewWithDeletedIdentityHMACKeys(db, nil)
}

// NewWithDeletedIdentityHMACKeys configures every retained read key. During
// rotation, old keys must remain here until every tombstone written under them
// has expired. New tombstones are written by maintenance with only its active
// key; the auth boundary intentionally has no active/write distinction.
func NewWithDeletedIdentityHMACKeys(db *sql.DB, keys map[string][]byte) (*Store, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	ids := make([]string, 0, len(keys))
	for id, material := range keys {
		if id == "" || len(id) > 128 || len(material) != 32 {
			return nil, fmt.Errorf("auth cockroach: invalid deleted-identity HMAC key %q", id)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	configured := make([]identityHMACKey, 0, len(ids))
	for _, id := range ids {
		configured = append(configured, identityHMACKey{id: id, material: append([]byte(nil), keys[id]...)})
	}
	return &Store{db: db, deletedIdentityHMACKeys: configured}, nil
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("auth cockroach: open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auth cockroach: ping database: %w", err)
	}
	return &Store{db: db}, nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
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
		return errors.Join(coreauth.ErrConflict, err)
	case "23503":
		return errors.Join(coreauth.ErrNotFound, err)
	case "22000", "22001", "22P02", "23502", "23514":
		return errors.Join(coreauth.ErrInvalidInput, err)
	default:
		return err
	}
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, persistenceError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}
