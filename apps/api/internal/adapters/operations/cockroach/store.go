// Package cockroach implements operational persistence ports for CockroachDB.
package cockroach

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const (
	requiredOperationsSchemaColumns = 104
	// adapter-sql-allowlist: virtual-catalog; this fixed information_schema.columns probe reads
	// virtual-catalog metadata, which official unpatched Scythe 0.17.0 cannot
	// model as a source table. It accepts no caller-supplied SQL or identifiers;
	// this limitation is not evidence of an upstream defect.
	validateOperationsSchemaQuery = `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND (
    (table_name = 'audit_events' AND column_name IN (
      'id', 'occurred_at', 'actor_type', 'actor_id', 'action',
      'target_type', 'target_id', 'outcome', 'request_id', 'metadata'
    ))
    OR (table_name = 'api_rate_limit_buckets' AND column_name IN (
      'scope', 'key_hash', 'window_start', 'count', 'expires_at'
    ))
    OR (table_name = 'provider_connections' AND column_name IN (
      'access_token_key_id', 'refresh_token_key_id'
    ))
	OR (table_name = 'provider_token_revocation_jobs' AND column_name IN (
	  'id', 'token_ciphertext', 'token_key_id'
	))
	OR (table_name = 'maintenance_checkpoints' AND column_name IN (
	  'operation', 'scope', 'payload', 'updated_at'
	))
	OR (table_name = 'legal_holds' AND column_name IN (
	  'target_type', 'target_id', 'expires_at'
	))
	OR (table_name = 'deletion_requests' AND column_name IN (
	  'id', 'request_id', 'target_type', 'target_id', 'status',
	  'last_completed_stage', 'error_code', 'subject_ids', 'requested_at',
	  'updated_at', 'completed_at', 'backup_expiry_at', 'audit_event_id'
	))
	OR (table_name = 'deletion_request_claims' AND column_name IN (
	  'deletion_request_id', 'attempts', 'available_at', 'lease_until',
	  'claim_token', 'updated_at'
	))
	OR (table_name = 'users' AND column_name = 'status')
	OR (table_name = 'deleted_identity_tombstones' AND column_name IN (
	  'email_hash', 'deletion_request_id', 'created_at', 'expires_at'
	))
	OR (table_name = 'deleted_identity_tombstones_v2' AND column_name IN (
	  'identity_key_id', 'identity_digest', 'deletion_request_id',
	  'created_at', 'expires_at'
	))
	OR (table_name = 'provider_connection_private_consents' AND column_name IN (
	  'connection_id', 'enabled', 'created_at', 'updated_at'
	))
	OR (table_name = 'deletion_request_inbox' AND column_name IN (
	  'id', 'request_id', 'target_type', 'target_id', 'status',
	  'requested_at', 'promoted_at'
	))
	OR (table_name = 'magic_link_mail_outbox' AND column_name IN (
	  'id', 'token_hash', 'token_expires_at', 'consumed_at',
	  'recipient_email', 'redirect_uri', 'purpose',
	  'status', 'attempts', 'available_at', 'lease_until',
	  'claim_token', 'created_at', 'updated_at', 'terminal_at', 'terminal_reason'
	))
	OR (table_name = 'mutation_audit_outbox' AND column_name IN (
	  'id', 'audit_event_id', 'request_id', 'occurred_at', 'actor_type',
	  'actor_id', 'action', 'target_type', 'target_id', 'outcome', 'metadata',
	  'status', 'attempts', 'available_at', 'lease_until', 'claim_token',
	  'created_at', 'updated_at', 'delivered_at', 'terminal_at', 'terminal_reason'
	))
  )`
)

var ErrNilDB = errors.New("operations cockroach: database is required")

var ErrInvalidDeletedIdentityHMAC = errors.New("operations cockroach: invalid deleted identity HMAC configuration")

type Store struct {
	db                    *sql.DB
	pool                  *pgxpool.Pool
	redactor              operations.Redactor
	identityHMACMu        sync.RWMutex
	identityHMACActiveKey string
	identityHMACKey       []byte
}

func (store *Store) ConfigureDeletedIdentityHMAC(activeKeyID string, key []byte) error {
	activeKeyID = strings.TrimSpace(activeKeyID)
	if activeKeyID == "" || len(activeKeyID) > 128 || len(key) != sha256.Size {
		return ErrInvalidDeletedIdentityHMAC
	}
	store.identityHMACMu.Lock()
	defer store.identityHMACMu.Unlock()
	clear(store.identityHMACKey)
	store.identityHMACActiveKey = activeKeyID
	store.identityHMACKey = append([]byte(nil), key...)
	return nil
}

func (store *Store) ClearDeletedIdentityHMAC() {
	store.identityHMACMu.Lock()
	defer store.identityHMACMu.Unlock()
	clear(store.identityHMACKey)
	store.identityHMACActiveKey = ""
	store.identityHMACKey = nil
}

func (store *Store) deletedIdentityHMAC(canonicalEmail string) (string, []byte, error) {
	store.identityHMACMu.RLock()
	defer store.identityHMACMu.RUnlock()
	if store.identityHMACActiveKey == "" || len(store.identityHMACKey) != sha256.Size {
		return "", nil, ErrInvalidDeletedIdentityHMAC
	}
	digest := hmac.New(sha256.New, store.identityHMACKey)
	_, _ = digest.Write([]byte(strings.ToLower(strings.TrimSpace(canonicalEmail))))
	return store.identityHMACActiveKey, digest.Sum(nil), nil
}

var (
	_ operations.AuditEventSink             = (*Store)(nil)
	_ operations.SecretReencryptionStore    = (*Store)(nil)
	_ operations.RetentionStore             = (*Store)(nil)
	_ operations.RetentionPreviewStore      = (*Store)(nil)
	_ operations.MaintenanceCheckpointStore = (*Store)(nil)
	_ operations.DeletionStore              = (*Store)(nil)
	_ operations.DeletionClaimStore         = (*Store)(nil)
	_ operations.DependencyProbe            = (*Store)(nil)
)

func New(db *sql.DB, extraSensitiveAuditKeys ...string) (*Store, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	return &Store{db: db, redactor: operations.NewRedactor(extraSensitiveAuditKeys...)}, nil
}

// NewWithPGXPool keeps legacy operational methods on db while migrated
// request-audit methods share pool with the other pgx-backed stores.
func NewWithPGXPool(db *sql.DB, pool *pgxpool.Pool, extraSensitiveAuditKeys ...string) (*Store, error) {
	if db == nil || pool == nil {
		return nil, ErrNilDB
	}
	return &Store{db: db, pool: pool, redactor: operations.NewRedactor(extraSensitiveAuditKeys...)}, nil
}

// Check implements operations.DependencyProbe.
func (store *Store) Check(ctx context.Context) error {
	if store.pool == nil {
		return ErrNilDB
	}
	var columns int
	err := appdb.PGXExecutorFor(ctx, store.pool).QueryRow(ctx, validateOperationsSchemaQuery).Scan(&columns)
	if err != nil {
		return fmt.Errorf("check operations schema: %w", err)
	}
	return validateOperationsSchemaColumnCount(columns)
}

func validateOperationsSchemaColumnCount(columns int) error {
	if columns != requiredOperationsSchemaColumns {
		return fmt.Errorf("check operations schema: found %d of %d required columns", columns, requiredOperationsSchemaColumns)
	}
	return nil
}
