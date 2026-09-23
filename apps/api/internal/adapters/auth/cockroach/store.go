// Package cockroach persists authentication state in CockroachDB.
package cockroach

import (
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

var ErrNilDB = errors.New("auth cockroach: database is required")

// Store implements auth.Repository. The caller owns configured handles.
type Store struct {
	pool                    *pgxpool.Pool
	deletedIdentityHMACKeys []identityHMACKey
}

type identityHMACKey struct {
	id       string
	material []byte
}

var _ coreauth.Repository = (*Store)(nil)

func New(pool *pgxpool.Pool) (*Store, error) {
	return NewWithDeletedIdentityHMACKeys(pool, nil)
}

// NewWithDeletedIdentityHMACKeys configures every retained read key. During
// rotation, old keys must remain here until every tombstone written under them
// has expired. New tombstones are written by maintenance with only its active
// key; the auth boundary intentionally has no active/write distinction.
func NewWithDeletedIdentityHMACKeys(pool *pgxpool.Pool, keys map[string][]byte) (*Store, error) {
	if pool == nil {
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
	return &Store{pool: pool, deletedIdentityHMACKeys: configured}, nil
}

func (store *Store) Close() error {
	// The pool is shared with other adapters and owned by the caller.
	return nil
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
