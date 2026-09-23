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
