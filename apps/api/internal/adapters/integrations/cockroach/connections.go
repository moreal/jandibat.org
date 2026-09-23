package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const connectionColumns = `
id::STRING, subject_id, COALESCE(sync_cursor->>'provider_id', ''), environment_id,
auth_method, COALESCE(external_account_id, ''),
COALESCE(sync_cursor->>'external_account_login', ''),
COALESCE(sync_cursor->>'connection_status', status),
COALESCE(array_to_json(scopes), '[]'::JSON), access_token_ciphertext,
refresh_token_ciphertext, token_expires_at, last_synced_at,
NULLIF(sync_cursor->>'last_sync_attempt_at', '')::TIMESTAMPTZ,
NULLIF(sync_cursor->>'next_sync_attempt_at', '')::TIMESTAMPTZ,
COALESCE((sync_cursor->>'last_sync_attempt')::INT, 0),
COALESCE((sync_cursor->>'consecutive_failures')::INT, 0),
COALESCE(last_error, ''), created_at, updated_at,
EXISTS (
  SELECT 1 FROM provider_connection_private_consents AS consent
  WHERE consent.connection_id = provider_connections.id AND consent.enabled
)`

const upsertConnectionQuery = `
INSERT INTO provider_connections (
  id, subject_id, environment_id, auth_method, external_account_id, status,
  scopes, access_token_ciphertext, access_token_key_id,
  refresh_token_ciphertext, refresh_token_key_id, token_expires_at,
  last_synced_at, last_error, created_at, updated_at, sync_cursor
) VALUES (
  $1::UUID, $2, $3, $4, NULLIF($5, ''), $6, $7::STRING[], $8, NULLIF($9, ''),
  $10, NULLIF($11, ''), $12, $13, NULLIF($14, ''), $15, $16,
  jsonb_build_object(
    'provider_id', $17::STRING,
    'connection_status', $18::STRING,
    'last_sync_attempt_at', $19::TIMESTAMPTZ,
    'next_sync_attempt_at', $20::TIMESTAMPTZ,
    'last_sync_attempt', $21::INT,
    'consecutive_failures', $22::INT,
    'external_account_login', $23::STRING
  )
)
ON CONFLICT (id) DO UPDATE SET
  subject_id = excluded.subject_id,
  environment_id = excluded.environment_id,
  auth_method = excluded.auth_method,
  external_account_id = excluded.external_account_id,
  status = excluded.status,
  scopes = excluded.scopes,
  access_token_ciphertext = excluded.access_token_ciphertext,
  access_token_key_id = excluded.access_token_key_id,
  refresh_token_ciphertext = excluded.refresh_token_ciphertext,
  refresh_token_key_id = excluded.refresh_token_key_id,
  token_expires_at = excluded.token_expires_at,
  last_synced_at = excluded.last_synced_at,
  last_error = excluded.last_error,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at,
  sync_cursor = COALESCE(provider_connections.sync_cursor, '{}'::JSONB)
    || excluded.sync_cursor
WHERE COALESCE(provider_connections.sync_cursor->>'connection_status', provider_connections.status) <> 'revoked'`

const updateConnectionAfterSyncQuery = `
UPDATE provider_connections SET
  status = $2,
  last_synced_at = $3,
  last_error = NULLIF($4, ''),
  updated_at = $5,
  sync_cursor = COALESCE(sync_cursor, '{}'::JSONB) || jsonb_build_object(
    'connection_status', $6::STRING,
    'last_sync_attempt_at', $7::TIMESTAMPTZ,
    'next_sync_attempt_at', $8::TIMESTAMPTZ,
    'last_sync_attempt', $9::INT,
    'consecutive_failures', $10::INT
  )
WHERE id = $1::UUID
  AND COALESCE(sync_cursor->>'connection_status', status) IN ('active', 'error')
  AND sync_cursor->>'sync_execution_claim_token' = $11`

func (s *Store) SaveConnection(ctx context.Context, record integrations.ConnectionRecord) error {
	connection := record.Connection
	if connection.ID == "" {
		return integrations.ErrEmptyConnectionID
	}
	if !connection.PrivateDataEnabled {
		// Defense in depth: an opt-out record cannot persist credentials even if
		// a caller accidentally supplies them. Absence of consent is the durable
		// authority, not the authentication method.
		record.Credentials = integrations.EncryptedCredentials{}
		connection.TokenExpiresAt = nil
	}
	accessKeyID, err := encryptedCredentialKeyID(record.Credentials.AccessToken)
	if err != nil {
		return fmt.Errorf("access token key ID: %w", err)
	}
	refreshKeyID, err := encryptedCredentialKeyID(record.Credentials.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh token key ID: %w", err)
	}
	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return fmt.Errorf("save connection: begin transaction: %w", err)
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	result, err := tx.ExecContext(ctx, upsertConnectionQuery,
		connection.ID, connection.SubjectID, connection.EnvironmentID,
		connection.AuthMethod, connection.ExternalAccountID, databaseConnectionStatus(connection.Status),
		connection.Scopes, nullableBytes(record.Credentials.AccessToken), accessKeyID,
		nullableBytes(record.Credentials.RefreshToken), refreshKeyID, connection.TokenExpiresAt,
		connection.LastSyncedAt, connection.LastError, connection.CreatedAt,
		connection.UpdatedAt, connection.ProviderID, connection.Status,
		connection.LastSyncAttemptAt, connection.NextSyncAttemptAt,
		connection.LastSyncAttempt, connection.ConsecutiveFailures,
		connection.ExternalAccountLogin,
	)
	if err != nil {
		return persistenceError(err, integrations.ErrConflict)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save connection: rows affected: %w", err)
	}
	if count != 1 {
		return integrations.ErrInvalidConnectionStatus
	}
	if connection.PrivateDataEnabled {
		_, err = tx.ExecContext(ctx, `
INSERT INTO provider_connection_private_consents (connection_id, enabled, created_at, updated_at)
VALUES ($1::UUID, true, $2, $2)
ON CONFLICT (connection_id) DO UPDATE SET enabled = true, updated_at = excluded.updated_at`,
			connection.ID, connection.UpdatedAt)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM provider_connection_private_consents WHERE connection_id = $1::UUID`, connection.ID)
	}
	if err != nil {
		return persistenceError(err, integrations.ErrConflict)
	}
	if err := scope.Commit(); err != nil {
		return fmt.Errorf("save connection: commit transaction: %w", err)
	}
	return nil
}

// UpdateConnectionAfterSync is intentionally UPDATE-only so the worker role
// needs no INSERT privilege on provider_connections. The execution claim and
// status predicate also prevent a stale worker from recreating or reviving a
// disconnected connection.
func (s *Store) UpdateConnectionAfterSync(ctx context.Context, record integrations.ConnectionRecord, claimToken string) error {
	connection := record.Connection
	if connection.ID == "" || claimToken == "" {
		return integrations.ErrInvalidConnectionStatus
	}
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, updateConnectionAfterSyncQuery,
		connection.ID, databaseConnectionStatus(connection.Status),
		connection.LastSyncedAt, connection.LastError, connection.UpdatedAt, connection.Status,
		connection.LastSyncAttemptAt, connection.NextSyncAttemptAt,
		connection.LastSyncAttempt, connection.ConsecutiveFailures,
		claimToken,
	)
	if err != nil {
		return persistenceError(err, integrations.ErrConflict)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update connection after sync: rows affected: %w", err)
	}
	if count != 1 {
		return integrations.ErrInvalidConnectionStatus
	}
	return nil
}

func encryptedCredentialKeyID(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	keyID, enveloped, err := operations.CiphertextKeyID(ciphertext)
	if err != nil {
		return "", err
	}
	if !enveloped {
		return "", nil
	}
	return keyID, nil
}

func databaseConnectionStatus(status integrations.ConnectionStatus) integrations.ConnectionStatus {
	// Migration 0001 predates the OAuth pending state. Keep its constraint
	// satisfied while preserving the richer state in sync_cursor.
	if status == integrations.ConnectionPending {
		return integrations.ConnectionActive
	}
	return status
}

func (s *Store) GetConnection(ctx context.Context, id string) (integrations.ConnectionRecord, error) {
	if s.pool != nil {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return integrations.ConnectionRecord{}, integrations.ErrInvalidIdentifier
		}
		row, err := generated.GetConnectionById(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed)
		if err != nil {
			return integrations.ConnectionRecord{}, fmt.Errorf("get connection: %w", err)
		}
		if row == nil {
			return integrations.ConnectionRecord{}, notFound("connection", id)
		}
		return connectionFromGenerated(*row)
	}
	query := `SELECT ` + connectionColumns + ` FROM provider_connections WHERE id = $1::UUID`
	record, err := scanConnection(appdb.ExecutorFor(ctx, s.db).QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return integrations.ConnectionRecord{}, notFound("connection", id)
	}
	if err != nil {
		return integrations.ConnectionRecord{}, fmt.Errorf("get connection: %w", err)
	}
	return record, nil
}

func connectionFromGenerated(row generated.GetConnectionByIdRow) (integrations.ConnectionRecord, error) {
	record := integrations.ConnectionRecord{Connection: integrations.ProviderConnection{
		ID: row.Id, SubjectID: row.SubjectId, ProviderID: row.ProviderId,
		EnvironmentID: row.EnvironmentId, AuthMethod: integrations.AuthMethod(row.AuthMethod),
		ExternalAccountID: row.ExternalAccountId, ExternalAccountLogin: row.ExternalAccountLogin,
		Status:             integrations.ConnectionStatus(row.ConnectionStatus),
		PrivateDataEnabled: row.PrivateDataEnabled, LastSyncAttempt: int(row.LastSyncAttempt),
		ConsecutiveFailures: int(row.ConsecutiveFailures), LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}}
	connection := &record.Connection
	if err := json.Unmarshal(row.ScopesJson, &connection.Scopes); err != nil {
		return integrations.ConnectionRecord{}, fmt.Errorf("decode scopes: %w", err)
	}
	if connection.Scopes == nil {
		connection.Scopes = []string{}
	}
	connection.TokenExpiresAt = row.TokenExpiresAt
	connection.LastSyncedAt = row.LastSyncedAt
	connection.LastSyncAttemptAt = row.LastSyncAttemptAt
	connection.NextSyncAttemptAt = row.NextSyncAttemptAt
	if row.AccessTokenCiphertext != nil {
		record.Credentials.AccessToken = append([]byte(nil), (*row.AccessTokenCiphertext)...)
	}
	if row.RefreshTokenCiphertext != nil {
		record.Credentials.RefreshToken = append([]byte(nil), (*row.RefreshTokenCiphertext)...)
	}
	return record, nil
}

func (s *Store) ListConnections(ctx context.Context, subjectID string) ([]integrations.ConnectionRecord, error) {
	if s.pool != nil {
		rows, err := generated.ListConnections(ctx, appdb.PGXExecutorFor(ctx, s.pool), subjectID)
		if err != nil {
			return nil, fmt.Errorf("list connections: %w", err)
		}
		records := make([]integrations.ConnectionRecord, 0, len(rows))
		for _, row := range rows {
			record, err := connectionFromGenerated(generated.GetConnectionByIdRow(row))
			if err != nil {
				return nil, fmt.Errorf("list connections: %w", err)
			}
			records = append(records, record)
		}
		return records, nil
	}
	query, args := buildListConnectionsQuery(subjectID)
	rows, err := appdb.ExecutorFor(ctx, s.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	records := make([]integrations.ConnectionRecord, 0)
	for rows.Next() {
		record, scanErr := scanConnection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list connections: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	return records, nil
}

// PurgeConnectionData removes facts and sync jobs in one transaction while
// retaining the sanitized revoked tombstone for bounded operational retention.
// Public connections share a global environment and must not purge its facts.
func (s *Store) PurgeConnectionData(ctx context.Context, id string) error {
	if id == "" {
		return integrations.ErrEmptyConnectionID
	}
	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return fmt.Errorf("purge connection data: begin transaction: %w", err)
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()

	var subjectID, environmentID string
	var authMethod integrations.AuthMethod
	var status integrations.ConnectionStatus
	if err := tx.QueryRowContext(ctx, `
SELECT subject_id, environment_id, auth_method,
       COALESCE(sync_cursor->>'connection_status', status)
FROM provider_connections
WHERE id = $1::UUID
FOR UPDATE`, id).Scan(&subjectID, &environmentID, &authMethod, &status); err != nil {
		if err == sql.ErrNoRows {
			return notFound("connection", id)
		}
		return fmt.Errorf("purge connection data: load aggregate: %w", err)
	}
	if status != integrations.ConnectionRevoked {
		return integrations.ErrInvalidConnectionStatus
	}
	if authMethod != integrations.AuthNone {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM activity_facts
WHERE subject_id = $1 AND environment_id = $2`, subjectID, environmentID); err != nil {
			return fmt.Errorf("purge connection data: purge facts: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_sync_jobs WHERE provider_connection_id = $1::UUID`, id); err != nil {
		return fmt.Errorf("purge connection data: purge sync jobs: %w", err)
	}
	if err := scope.Commit(); err != nil {
		return fmt.Errorf("purge connection data: commit transaction: %w", err)
	}
	return nil
}

func buildListConnectionsQuery(subjectID string) (string, []any) {
	query := `SELECT ` + connectionColumns + ` FROM provider_connections`
	args := []any{}
	if subjectID != "" {
		query += ` WHERE subject_id = $1`
		args = append(args, subjectID)
	}
	query += ` ORDER BY subject_id, id`
	return query, args
}

func scanConnection(row scanner) (integrations.ConnectionRecord, error) {
	var (
		record                                                        integrations.ConnectionRecord
		scopes                                                        []byte
		expiresAt, lastSyncedAt, lastSyncAttemptAt, nextSyncAttemptAt sql.NullTime
		accessToken, refreshToken                                     []byte
	)
	c := &record.Connection
	if err := row.Scan(
		&c.ID, &c.SubjectID, &c.ProviderID, &c.EnvironmentID, &c.AuthMethod,
		&c.ExternalAccountID, &c.ExternalAccountLogin, &c.Status, &scopes, &accessToken, &refreshToken,
		&expiresAt, &lastSyncedAt, &lastSyncAttemptAt, &nextSyncAttemptAt,
		&c.LastSyncAttempt, &c.ConsecutiveFailures, &c.LastError, &c.CreatedAt, &c.UpdatedAt,
		&c.PrivateDataEnabled,
	); err != nil {
		return integrations.ConnectionRecord{}, err
	}
	if len(scopes) != 0 {
		if err := json.Unmarshal(scopes, &c.Scopes); err != nil {
			return integrations.ConnectionRecord{}, fmt.Errorf("decode scopes: %w", err)
		}
	}
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	if expiresAt.Valid {
		c.TokenExpiresAt = &expiresAt.Time
	}
	if lastSyncedAt.Valid {
		c.LastSyncedAt = &lastSyncedAt.Time
	}
	if lastSyncAttemptAt.Valid {
		c.LastSyncAttemptAt = &lastSyncAttemptAt.Time
	}
	if nextSyncAttemptAt.Valid {
		c.NextSyncAttemptAt = &nextSyncAttemptAt.Time
	}
	record.Credentials.AccessToken = append([]byte(nil), accessToken...)
	record.Credentials.RefreshToken = append([]byte(nil), refreshToken...)
	return record, nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
