package cockroach

import (
	"context"
	"fmt"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// Do not filter by key_id here. Version-1 envelopes can already be labeled
// with the active key ID; the worker parses each bounded candidate page and
// skips only confirmed active-key version-2 values.
const listSecretsForReencryptionQuery = `
WITH encrypted_secrets (kind, id, key_id, ciphertext) AS (
  SELECT 'connection_access_token', id::STRING,
    COALESCE(access_token_key_id, ''), access_token_ciphertext
  FROM provider_connections
  WHERE access_token_ciphertext IS NOT NULL
  UNION ALL
  SELECT 'connection_refresh_token', id::STRING,
    COALESCE(refresh_token_key_id, ''), refresh_token_ciphertext
  FROM provider_connections
  WHERE refresh_token_ciphertext IS NOT NULL
  UNION ALL
  SELECT 'oauth_revocation_token', id::STRING,
    COALESCE(token_key_id, ''), token_ciphertext
  FROM provider_token_revocation_jobs
  WHERE token_ciphertext IS NOT NULL
)
SELECT kind, id, key_id, ciphertext
FROM encrypted_secrets
WHERE (kind > $1 OR (kind = $1 AND id > $2))
ORDER BY kind, id
LIMIT $3`

func (store *Store) ListSecretsForReencryption(ctx context.Context, after operations.SecretLocator, limit int) ([]operations.EncryptedSecretRecord, error) {
	if limit <= 0 {
		return nil, operations.ErrInvalidReencryptionConfig
	}
	rows, err := store.db.QueryContext(ctx, listSecretsForReencryptionQuery, after.Kind, after.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("list secrets for re-encryption: %w", err)
	}
	defer rows.Close()
	result := make([]operations.EncryptedSecretRecord, 0, limit)
	for rows.Next() {
		var record operations.EncryptedSecretRecord
		if err := rows.Scan(&record.Locator.Kind, &record.Locator.ID, &record.KeyID, &record.Ciphertext); err != nil {
			return nil, fmt.Errorf("scan secret for re-encryption: %w", err)
		}
		record.Ciphertext = append([]byte(nil), record.Ciphertext...)
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list secrets for re-encryption: %w", err)
	}
	return result, nil
}

func (store *Store) ReplaceEncryptedSecret(ctx context.Context, expected operations.EncryptedSecretRecord, newKeyID string, ciphertext []byte) (bool, error) {
	if newKeyID == "" || len(ciphertext) == 0 {
		return false, operations.ErrInvalidReencryptionConfig
	}
	query, err := replaceSecretQuery(expected.Locator.Kind)
	if err != nil {
		return false, err
	}
	result, err := store.db.ExecContext(ctx, query,
		expected.Locator.ID, expected.Ciphertext, ciphertext, newKeyID, expected.KeyID,
	)
	if err != nil {
		return false, fmt.Errorf("replace encrypted secret %s/%s: %w", expected.Locator.Kind, expected.Locator.ID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("replace encrypted secret rows affected: %w", err)
	}
	return count == 1, nil
}

func replaceSecretQuery(kind operations.SecretKind) (string, error) {
	switch kind {
	case operations.SecretConnectionAccessToken:
		return `UPDATE provider_connections
SET access_token_ciphertext = $3, access_token_key_id = $4, updated_at = now()
WHERE id = $1::UUID AND access_token_ciphertext = $2
  AND COALESCE(access_token_key_id, '') = $5`, nil
	case operations.SecretConnectionRefreshToken:
		return `UPDATE provider_connections
SET refresh_token_ciphertext = $3, refresh_token_key_id = $4, updated_at = now()
WHERE id = $1::UUID AND refresh_token_ciphertext = $2
  AND COALESCE(refresh_token_key_id, '') = $5`, nil
	case operations.SecretOAuthRevocationToken:
		return `UPDATE provider_token_revocation_jobs
SET token_ciphertext = $3, token_key_id = $4, updated_at = now()
WHERE id = $1::UUID AND token_ciphertext = $2
  AND COALESCE(token_key_id, '') = $5`, nil
	default:
		return "", fmt.Errorf("%w: unknown kind %q", operations.ErrInvalidSecretRecord, kind)
	}
}
