-- @name AcquireSyncExecution
-- @returns :opt
UPDATE provider_connections
SET sync_cursor = COALESCE(sync_cursor, '{}'::JSONB)
    || jsonb_build_object(
      'sync_execution_expires_at', now() + INTERVAL '20 minutes',
      'sync_execution_claim_token', gen_random_uuid()::STRING
    )
WHERE id = $1::UUID
  AND COALESCE(sync_cursor->>'connection_status', status) IN ('active', 'error')
  AND COALESCE(
    NULLIF(sync_cursor->>'sync_execution_expires_at', '')::TIMESTAMPTZ,
    'epoch'::TIMESTAMPTZ
  ) <= now()
RETURNING sync_cursor->>'sync_execution_claim_token' AS claim_token;

-- @name ReleaseSyncExecution
-- @returns :exec
UPDATE provider_connections
SET sync_cursor = (COALESCE(sync_cursor, '{}'::JSONB) - 'sync_execution_expires_at') - 'sync_execution_claim_token'
WHERE id = $1::UUID AND sync_cursor->>'sync_execution_claim_token' = $2::STRING;

-- @name GetConnectionByID
-- @returns :opt
SELECT
  provider_connections.id::STRING AS id, subject_id,
  COALESCE(sync_cursor->>'provider_id', '') AS provider_id,
  environment_id, auth_method, COALESCE(external_account_id, '') AS external_account_id,
  COALESCE(sync_cursor->>'external_account_login', '') AS external_account_login,
  COALESCE(sync_cursor->>'connection_status', status) AS connection_status,
  COALESCE(array_to_json(scopes), '[]'::JSON) AS scopes_json,
  access_token_ciphertext, refresh_token_ciphertext, token_expires_at,
  last_synced_at,
  NULLIF(sync_cursor->>'last_sync_attempt_at', '')::TIMESTAMPTZ AS last_sync_attempt_at,
  NULLIF(sync_cursor->>'next_sync_attempt_at', '')::TIMESTAMPTZ AS next_sync_attempt_at,
  COALESCE((sync_cursor->>'last_sync_attempt')::INT, 0) AS last_sync_attempt,
  COALESCE((sync_cursor->>'consecutive_failures')::INT, 0) AS consecutive_failures,
  COALESCE(last_error, '') AS last_error, created_at, updated_at,
  EXISTS (
    SELECT 1 FROM provider_connection_private_consents AS consent
    WHERE consent.connection_id = provider_connections.id AND consent.enabled
  ) AS private_data_enabled
FROM provider_connections WHERE id = $1::UUID;

-- @name ListConnections
-- @returns :many
SELECT
  provider_connections.id::STRING AS id, subject_id,
  COALESCE(sync_cursor->>'provider_id', '') AS provider_id,
  environment_id, auth_method, COALESCE(external_account_id, '') AS external_account_id,
  COALESCE(sync_cursor->>'external_account_login', '') AS external_account_login,
  COALESCE(sync_cursor->>'connection_status', status) AS connection_status,
  COALESCE(array_to_json(scopes), '[]'::JSON) AS scopes_json,
  access_token_ciphertext, refresh_token_ciphertext, token_expires_at,
  last_synced_at,
  NULLIF(sync_cursor->>'last_sync_attempt_at', '')::TIMESTAMPTZ AS last_sync_attempt_at,
  NULLIF(sync_cursor->>'next_sync_attempt_at', '')::TIMESTAMPTZ AS next_sync_attempt_at,
  COALESCE((sync_cursor->>'last_sync_attempt')::INT, 0) AS last_sync_attempt,
  COALESCE((sync_cursor->>'consecutive_failures')::INT, 0) AS consecutive_failures,
  COALESCE(last_error, '') AS last_error, created_at, updated_at,
  EXISTS (
    SELECT 1 FROM provider_connection_private_consents AS consent
    WHERE consent.connection_id = provider_connections.id AND consent.enabled
  ) AS private_data_enabled
FROM provider_connections
WHERE ($1::STRING = '' OR subject_id = $1::STRING)
ORDER BY subject_id, id;

-- @name UpsertConnection
-- @returns :exec_result
INSERT INTO provider_connections (
  id, subject_id, environment_id, auth_method, external_account_id, status,
  scopes, access_token_ciphertext, access_token_key_id,
  refresh_token_ciphertext, refresh_token_key_id, token_expires_at,
  last_synced_at, last_error, created_at, updated_at, sync_cursor
) VALUES (
  $1::UUID, $2::STRING, $3::STRING, $4::STRING, NULLIF($5::STRING, ''),
  $6::STRING, $7::STRING[], $8::BYTES, NULLIF($9::STRING, ''),
  $10::BYTES, NULLIF($11::STRING, ''), $12::TIMESTAMPTZ,
  $13::TIMESTAMPTZ, NULLIF($14::STRING, ''), $15::TIMESTAMPTZ,
  $16::TIMESTAMPTZ,
  jsonb_build_object(
    'provider_id', $17::STRING,
    'connection_status', $18::STRING,
    'last_sync_attempt_at', NULLIF($19::STRING, '')::TIMESTAMPTZ,
    'next_sync_attempt_at', NULLIF($20::STRING, '')::TIMESTAMPTZ,
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
WHERE COALESCE(provider_connections.sync_cursor->>'connection_status', provider_connections.status) <> 'revoked';

-- @name EnableConnectionPrivateConsent
-- @returns :exec
INSERT INTO provider_connection_private_consents (connection_id, enabled, created_at, updated_at)
VALUES ($1::UUID, true, $2::TIMESTAMPTZ, $2::TIMESTAMPTZ)
ON CONFLICT (connection_id) DO UPDATE SET enabled = true, updated_at = excluded.updated_at;

-- @name DisableConnectionPrivateConsent
-- @returns :exec
DELETE FROM provider_connection_private_consents WHERE connection_id = $1::UUID;

-- @name UpdateConnectionAfterSync
-- @returns :exec_result
UPDATE provider_connections SET
  status = $2::STRING,
  last_synced_at = NULLIF($3::STRING, '')::TIMESTAMPTZ,
  last_error = NULLIF($4::STRING, ''),
  updated_at = $5::TIMESTAMPTZ,
  sync_cursor = COALESCE(sync_cursor, '{}'::JSONB) || jsonb_build_object(
    'connection_status', $6::STRING,
    'last_sync_attempt_at', NULLIF($7::STRING, '')::TIMESTAMPTZ,
    'next_sync_attempt_at', NULLIF($8::STRING, '')::TIMESTAMPTZ,
    'last_sync_attempt', $9::INT,
    'consecutive_failures', $10::INT
  )
WHERE id = $1::UUID
  AND COALESCE(sync_cursor->>'connection_status', status) IN ('active', 'error')
  AND sync_cursor->>'sync_execution_claim_token' = $11::STRING;
