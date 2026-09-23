package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

const credentialColumns = `id, user_id, credential_id, public_key, aaguid, sign_count, transports, verifier_credential, COALESCE(label, ''), created_at, last_used_at`

func (store *Store) SaveCredential(ctx context.Context, credential coreauth.PasskeyCredential) error {
	if credential.ID == "" || credential.UserID == "" || len(credential.CredentialID) == 0 ||
		len(credential.PublicKey) == 0 || !validJSONObject(credential.VerifierCredential) {
		return coreauth.ErrInvalidInput
	}
	if store.pool != nil {
		id, err := uuid.Parse(credential.ID)
		if err != nil {
			return coreauth.ErrInvalidInput
		}
		var aaguid *[]byte
		if len(credential.AAGUID) != 0 {
			value := append([]byte(nil), credential.AAGUID...)
			aaguid = &value
		}
		var transports *[]string
		if credential.Transports != nil {
			value := append([]string(nil), credential.Transports...)
			transports = &value
		}
		label := credential.Label
		return persistenceError(generated.InsertCredential(ctx, appdb.PGXExecutorFor(ctx, store.pool), id, credential.UserID, credential.CredentialID, credential.PublicKey, aaguid, int64(credential.SignCount), transports, credential.VerifierCredential, &label, credential.CreatedAt, credential.LastUsedAt))
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO user_passkeys
  (id, user_id, credential_id, public_key, aaguid, sign_count, transports, verifier_credential, label, created_at, last_used_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10, $11)`,
		credential.ID, credential.UserID, credential.CredentialID, credential.PublicKey, nullableBytes(credential.AAGUID),
		int64(credential.SignCount), credential.Transports, []byte(credential.VerifierCredential), credential.Label,
		credential.CreatedAt, credential.LastUsedAt)
	return persistenceError(err)
}

func (store *Store) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (coreauth.PasskeyCredential, error) {
	if len(credentialID) == 0 {
		return coreauth.PasskeyCredential{}, coreauth.ErrInvalidInput
	}
	if store.pool != nil {
		row, err := generated.GetCredentialByCredentialId(ctx, appdb.PGXExecutorFor(ctx, store.pool), credentialID)
		if err != nil {
			return coreauth.PasskeyCredential{}, persistenceError(err)
		}
		if row == nil {
			return coreauth.PasskeyCredential{}, coreauth.ErrNotFound
		}
		return credentialFromGenerated(*row)
	}
	return scanCredential(appdb.ExecutorFor(ctx, store.db).QueryRowContext(ctx, `
SELECT `+credentialColumns+` FROM user_passkeys WHERE credential_id = $1`, credentialID))
}

func (store *Store) ListCredentialsByUser(ctx context.Context, userID string) ([]coreauth.PasskeyCredential, error) {
	if store.pool != nil {
		rows, err := generated.ListCredentialsByUser(ctx, appdb.PGXExecutorFor(ctx, store.pool), userID)
		if err != nil {
			return nil, persistenceError(err)
		}
		items := make([]coreauth.PasskeyCredential, 0, len(rows))
		for _, row := range rows {
			item, mapErr := credentialFromGenerated(generated.GetCredentialByCredentialIdRow(row))
			if mapErr != nil {
				return nil, mapErr
			}
			items = append(items, item)
		}
		return items, nil
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT `+credentialColumns+` FROM user_passkeys WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, persistenceError(err)
	}
	defer rows.Close()
	items := make([]coreauth.PasskeyCredential, 0)
	for rows.Next() {
		item, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, persistenceError(err)
	}
	return items, nil
}

func (store *Store) UseCredential(ctx context.Context, credentialID []byte, previous, next uint32, verifierCredential json.RawMessage, now time.Time) error {
	if len(credentialID) == 0 || !validJSONObject(verifierCredential) {
		return coreauth.ErrInvalidInput
	}
	if counterDidNotAdvance(previous, next) {
		return coreauth.ErrConflict
	}
	if store.pool != nil {
		executor := appdb.PGXExecutorFor(ctx, store.pool)
		count, err := generated.UpdateCredentialUse(ctx, executor, credentialID, int64(previous), int64(next), verifierCredential, now)
		if err != nil {
			return persistenceError(err)
		}
		if count != 0 {
			return nil
		}
		exists, err := generated.GetCredentialExists(ctx, executor, credentialID)
		if err != nil {
			return persistenceError(err)
		}
		if !exists.Exists {
			return coreauth.ErrNotFound
		}
		return coreauth.ErrConflict
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
UPDATE user_passkeys
SET sign_count = $3, verifier_credential = $4, last_used_at = $5
WHERE credential_id = $1 AND sign_count = $2`, credentialID, int64(previous), int64(next), []byte(verifierCredential), now)
	count, err := rowsAffected(result, err)
	if err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	var exists bool
	if err := executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_passkeys WHERE credential_id = $1)`, credentialID).Scan(&exists); err != nil {
		return persistenceError(err)
	}
	if !exists {
		return coreauth.ErrNotFound
	}
	return coreauth.ErrConflict
}

func credentialFromGenerated(row generated.GetCredentialByCredentialIdRow) (coreauth.PasskeyCredential, error) {
	if row.SignCount < 0 || row.SignCount > math.MaxUint32 || !validJSONObject(row.VerifierCredential) {
		return coreauth.PasskeyCredential{}, coreauth.ErrInvalidInput
	}
	credential := coreauth.PasskeyCredential{ID: row.Id, UserID: row.UserId, CredentialID: append([]byte(nil), row.CredentialId...), PublicKey: append([]byte(nil), row.PublicKey...), SignCount: uint32(row.SignCount), VerifierCredential: append(json.RawMessage(nil), row.VerifierCredential...), Label: row.Label, CreatedAt: row.CreatedAt}
	if row.Aaguid != nil {
		credential.AAGUID = append([]byte(nil), (*row.Aaguid)...)
	}
	if row.Transports != nil {
		credential.Transports = append([]string(nil), (*row.Transports)...)
	}
	if row.LastUsedAt != nil {
		value := *row.LastUsedAt
		credential.LastUsedAt = &value
	}
	return credential, nil
}

func scanCredential(row scanner) (coreauth.PasskeyCredential, error) {
	var credential coreauth.PasskeyCredential
	var count int64
	var lastUsed sql.NullTime
	var verifier []byte
	var transports nullableStringSlice
	if err := row.Scan(&credential.ID, &credential.UserID, &credential.CredentialID, &credential.PublicKey,
		&credential.AAGUID, &count, &transports, &verifier, &credential.Label,
		&credential.CreatedAt, &lastUsed); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.PasskeyCredential{}, coreauth.ErrNotFound
		}
		return coreauth.PasskeyCredential{}, persistenceError(err)
	}
	if count < 0 || count > math.MaxUint32 || !validJSONObject(verifier) {
		return coreauth.PasskeyCredential{}, coreauth.ErrInvalidInput
	}
	credential.SignCount = uint32(count)
	credential.Transports = append([]string(nil), transports...)
	credential.VerifierCredential = append(json.RawMessage(nil), verifier...)
	if lastUsed.Valid {
		value := lastUsed.Time
		credential.LastUsedAt = &value
	}
	return credential, nil
}

// nullableStringSlice bridges database/sql and pgx's text-array codec. pgx's
// database/sql adapter exposes a non-NULL STRING[] as PostgreSQL array text and
// NULL as nil; neither can be scanned directly into *[]string.
type nullableStringSlice []string

func (destination *nullableStringSlice) Scan(source any) error {
	if destination == nil {
		return fmt.Errorf("auth cockroach: scan transports into nil destination")
	}
	switch value := source.(type) {
	case nil:
		*destination = nil
		return nil
	case []string:
		*destination = append((*destination)[:0], value...)
		return nil
	case string:
		return destination.scanText([]byte(value))
	case []byte:
		return destination.scanText(value)
	default:
		return fmt.Errorf("auth cockroach: unsupported transports value %T", source)
	}
}

func (destination *nullableStringSlice) scanText(source []byte) error {
	var decoded pgtype.FlatArray[string]
	if err := pgtype.NewMap().Scan(pgtype.TextArrayOID, pgtype.TextFormatCode, source, &decoded); err != nil {
		return fmt.Errorf("auth cockroach: decode transports: %w", err)
	}
	*destination = append((*destination)[:0], decoded...)
	return nil
}

func counterDidNotAdvance(previous, next uint32) bool {
	return (previous != 0 || next != 0) && next <= previous
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
