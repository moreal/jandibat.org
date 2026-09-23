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

-- @name GetCustomProviderByID
-- @returns :opt
SELECT provider.id::STRING AS id, provider.subject_id, provider.environment_id,
  provider.slug, provider.name, COALESCE(provider.description, '') AS description,
  provider.status, provider.configuration, secrets.ingest_token_hash,
  provider.created_at, provider.updated_at
FROM custom_providers AS provider
JOIN custom_provider_secrets AS secrets ON secrets.provider_id = provider.id
WHERE provider.id = $1::UUID;

-- @name ListCustomProviders
-- @returns :many
SELECT provider.id::STRING AS id, provider.subject_id, provider.environment_id,
  provider.slug, provider.name, COALESCE(provider.description, '') AS description,
  provider.status, provider.configuration, secrets.ingest_token_hash,
  provider.created_at, provider.updated_at
FROM custom_providers AS provider
JOIN custom_provider_secrets AS secrets ON secrets.provider_id = provider.id
WHERE ($1::STRING = '' OR provider.subject_id = $1::STRING)
ORDER BY provider.subject_id, provider.slug, provider.id;

-- @name InsertCustomProvider
-- @returns :exec_result
INSERT INTO custom_providers (
  id, owner_user_id, subject_id, environment_id, slug, name, description,
  status, configuration, created_at, updated_at
)
SELECT $1::UUID, owner_user_id, $2::STRING, $3::STRING, $4::STRING, $5::STRING,
  NULLIF($6::STRING, ''), $7::STRING,
  jsonb_build_object('allowed_actions', $8::JSONB, 'allowed_metrics', $9::JSONB),
  $10::TIMESTAMPTZ, $11::TIMESTAMPTZ
FROM subjects WHERE id = $2::STRING AND owner_user_id IS NOT NULL;

-- @name UpsertCustomProviderSecret
-- @returns :exec
INSERT INTO custom_provider_secrets (provider_id, ingest_token_hash)
VALUES ($1::UUID, $2::BYTES)
ON CONFLICT (provider_id) DO UPDATE SET ingest_token_hash = excluded.ingest_token_hash;

-- @name UpdateCustomProvider
-- @returns :exec_result
UPDATE custom_providers
SET environment_id = $3::STRING, slug = $4::STRING, name = $5::STRING,
  description = NULLIF($6::STRING, ''), status = $7::STRING,
  configuration = COALESCE(configuration, '{}'::JSONB)
    || jsonb_build_object('allowed_actions', $8::JSONB, 'allowed_metrics', $9::JSONB),
  created_at = $10::TIMESTAMPTZ, updated_at = $11::TIMESTAMPTZ
WHERE id = $1::UUID AND subject_id = $2::STRING;

-- @name UpsertCustomProvider
-- @returns :exec_result
INSERT INTO custom_providers (
  id, owner_user_id, subject_id, environment_id, slug, name, description,
  status, configuration, created_at, updated_at
)
SELECT $1::UUID, owner_user_id, $2::STRING, $3::STRING, $4::STRING, $5::STRING,
  NULLIF($6::STRING, ''), $7::STRING,
  jsonb_build_object('allowed_actions', $8::JSONB, 'allowed_metrics', $9::JSONB),
  $10::TIMESTAMPTZ, $11::TIMESTAMPTZ
FROM subjects WHERE id = $2::STRING AND owner_user_id IS NOT NULL
ON CONFLICT (id) DO UPDATE SET
  environment_id = excluded.environment_id,
  slug = excluded.slug,
  name = excluded.name,
  description = excluded.description,
  status = excluded.status,
  configuration = COALESCE(custom_providers.configuration, '{}'::JSONB)
    || excluded.configuration,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at;

-- @name ReserveIngestKey
-- @returns :exec_result
INSERT INTO ingest_idempotency_keys (
  custom_provider_id, key_hash, request_hash, response_status, response_body,
  created_at, expires_at
)
VALUES ($1::UUID, $2::BYTES, $3::BYTES, $4::INT, $5::JSONB,
  $6::TIMESTAMPTZ, $7::TIMESTAMPTZ)
ON CONFLICT (custom_provider_id, key_hash) DO UPDATE SET
  request_hash = excluded.request_hash,
  response_status = excluded.response_status,
  response_body = excluded.response_body,
  created_at = excluded.created_at,
  expires_at = excluded.expires_at
WHERE ingest_idempotency_keys.expires_at <= now();

-- @name CompleteIngestKey
-- @returns :exec_result
UPDATE ingest_idempotency_keys
SET response_status = $4::INT, response_body = $5::JSONB, expires_at = $6::TIMESTAMPTZ
WHERE custom_provider_id = $1::UUID
  AND key_hash = $2::BYTES
  AND request_hash = $3::BYTES
  AND response_status = 0
  AND expires_at > now();

-- @name ReleaseIngestKey
-- @returns :exec
DELETE FROM ingest_idempotency_keys
WHERE custom_provider_id = $1::UUID
  AND key_hash = $2::BYTES
  AND request_hash = $3::BYTES
  AND response_status = 0;

-- @name GetActiveIngestKey
-- @returns :opt
SELECT custom_provider_id::STRING AS provider_id, key_hash, request_hash,
  response_status, response_body, created_at, expires_at
FROM ingest_idempotency_keys
WHERE custom_provider_id = $1::UUID AND key_hash = $2::BYTES AND expires_at > now();

-- @name LockProviderForIngest
-- @returns :opt
SELECT id::STRING AS id FROM custom_providers WHERE id = $1::UUID FOR UPDATE;

-- @name InsertCustomActivity
-- @returns :opt
INSERT INTO custom_activity_events (
  custom_provider_id, subject_id, environment_id, event_id, activity_date,
  action, metric_name, metric_value, metadata, observed_at, ingested_at
)
SELECT provider.id, provider.subject_id, provider.environment_id,
  $2::STRING, $3::STRING::DATE, $4::STRING, $5::STRING, $6::INT8, $7::JSONB,
  NULLIF($8::STRING, '')::TIMESTAMPTZ, $9::TIMESTAMPTZ
FROM custom_providers AS provider
WHERE provider.id = $1::UUID
ON CONFLICT (custom_provider_id, event_id) DO NOTHING
RETURNING custom_provider_id::STRING AS provider_id, subject_id,
  event_id AS external_id, activity_date::STRING AS activity_date,
  action, metric_name, metric_value, metadata, observed_at, ingested_at;

-- @name TouchCustomProviderIngested
-- @returns :exec
UPDATE custom_providers SET last_ingested_at = now(), updated_at = now()
WHERE id = $1::UUID;

-- @name GetCustomProviderExists
-- @returns :one
SELECT EXISTS (SELECT 1 FROM custom_providers WHERE id = $1::UUID) AS exists;

-- @name ListCustomActivities
-- @returns :many
SELECT custom_provider_id::STRING AS provider_id, subject_id,
  event_id AS external_id, activity_date::STRING AS activity_date,
  action, metric_name, metric_value, metadata, observed_at, ingested_at
FROM custom_activity_events
WHERE custom_provider_id = $1::UUID
ORDER BY activity_date, event_id;

-- @name LockCustomProviderAggregate
-- @returns :opt
SELECT subject_id, environment_id FROM custom_providers
WHERE id = $1::UUID FOR UPDATE;

-- @name DeleteCustomProviderRefreshCache
-- @returns :exec
DELETE FROM activity_refresh_cache
WHERE subject_id = $1::STRING AND environment_id = $2::STRING;

-- @name DeleteCustomProviderFacts
-- @returns :exec
DELETE FROM activity_facts
WHERE custom_provider_id = $1::UUID OR (subject_id = $2::STRING AND environment_id = $3::STRING);

-- @name DeleteCustomProviderByID
-- @returns :exec_result
DELETE FROM custom_providers WHERE id = $1::UUID;

-- @name LockConnectionForRevocation
-- @returns :opt
SELECT provider_connections.id::STRING AS id, subject_id,
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
FROM provider_connections WHERE id = $1::UUID FOR UPDATE;

-- @name EnqueueOAuthTokenRevocation
-- @returns :exec
INSERT INTO provider_token_revocation_jobs (
  connection_id, provider_id, token_ciphertext, token_key_id,
  status, attempts, available_at, created_at, updated_at
) VALUES ($1::UUID, $2::STRING, $3::BYTES, NULLIF($4::STRING, ''),
  'pending', 0, $5::TIMESTAMPTZ, $5::TIMESTAMPTZ, $5::TIMESTAMPTZ);

-- @name SanitizeRevokedConnection
-- @returns :exec
UPDATE provider_connections
SET status = 'revoked', access_token_ciphertext = NULL, access_token_key_id = NULL,
  refresh_token_ciphertext = NULL, refresh_token_key_id = NULL,
  token_expires_at = NULL, last_error = NULL, updated_at = $2::TIMESTAMPTZ,
  sync_cursor = ((COALESCE(sync_cursor, '{}'::JSONB) - 'sync_execution_expires_at') - 'sync_execution_claim_token')
    || jsonb_build_object('connection_status', 'revoked')
WHERE id = $1::UUID;

-- @name PurgeConnectionFacts
-- @returns :exec
DELETE FROM activity_facts WHERE subject_id = $1::STRING AND environment_id = $2::STRING;

-- @name PurgeConnectionSyncJobs
-- @returns :exec
DELETE FROM provider_sync_jobs WHERE provider_connection_id = $1::UUID;

-- @name ClaimOAuthTokenRevocations
-- @returns :many
UPDATE provider_token_revocation_jobs
SET status = 'processing', attempts = attempts + 1,
  lease_until = $2::TIMESTAMPTZ, claim_token = gen_random_uuid(), updated_at = $1::TIMESTAMPTZ
WHERE id IN (
  SELECT id FROM provider_token_revocation_jobs
  WHERE (status = 'pending' AND available_at <= $1::TIMESTAMPTZ)
     OR (status = 'processing' AND lease_until <= $1::TIMESTAMPTZ)
  ORDER BY available_at, id
  LIMIT $3::INT8
  FOR UPDATE SKIP LOCKED
)
RETURNING id::STRING AS id, COALESCE(connection_id::STRING, '') AS connection_id,
  provider_id, token_ciphertext, COALESCE(token_key_id, '') AS token_key_id,
  claim_token::STRING AS claim_token, attempts, available_at,
  lease_until, created_at, updated_at;

-- @name CompleteOAuthTokenRevocation
-- @returns :exec_result
DELETE FROM provider_token_revocation_jobs
WHERE id = $1::UUID AND status = 'processing' AND claim_token = $2::UUID;

-- @name DeleteExpiredSyncReservation
-- @returns :exec
DELETE FROM provider_sync_jobs
WHERE provider_connection_id = $1::UUID AND idempotency_key_hash = $2::BYTES
  AND idempotency_expires_at <= $3::TIMESTAMPTZ;

-- @name UpsertSyncJob
-- @returns :exec_result
INSERT INTO provider_sync_jobs (
  id, provider_connection_id, subject_id, environment_id, status,
  date_from, date_to, attempt, max_attempts, available_at,
  started_at, finished_at, idempotency_key_hash, request_hash,
  idempotency_expires_at, last_error, created_at, updated_at
)
SELECT $1::UUID, id, subject_id, environment_id, $3::STRING,
  NULLIF($4::STRING, '')::DATE, NULLIF($5::STRING, '')::DATE,
  $6::INT4, $7::INT4, $8::TIMESTAMPTZ,
  NULLIF($9::STRING, '')::TIMESTAMPTZ,
  NULLIF($10::STRING, '')::TIMESTAMPTZ,
  $11::BYTES, $12::BYTES,
  NULLIF($13::STRING, '')::TIMESTAMPTZ,
  $14::STRING, $15::TIMESTAMPTZ, $16::TIMESTAMPTZ
FROM provider_connections WHERE id = $2::UUID
ON CONFLICT (id) DO UPDATE SET
  status = excluded.status,
  date_from = excluded.date_from,
  date_to = excluded.date_to,
  attempt = excluded.attempt,
  max_attempts = excluded.max_attempts,
  available_at = excluded.available_at,
  started_at = excluded.started_at,
  finished_at = excluded.finished_at,
  idempotency_key_hash = excluded.idempotency_key_hash,
  request_hash = excluded.request_hash,
  idempotency_expires_at = excluded.idempotency_expires_at,
  last_error = excluded.last_error,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at;

-- @name GetSyncJobByID
-- @returns :opt
SELECT id::STRING AS id, provider_connection_id::STRING AS connection_id,
  date_from::STRING AS date_from, date_to::STRING AS date_to,
  status, attempt, available_at, started_at, finished_at,
  COALESCE(last_error, '{}') AS payload, created_at, updated_at
FROM provider_sync_jobs WHERE id = $1::UUID;

-- @name ListSyncJobs
-- @returns :many
SELECT id::STRING AS id, provider_connection_id::STRING AS connection_id,
  date_from::STRING AS date_from, date_to::STRING AS date_to,
  status, attempt, available_at, started_at, finished_at,
  COALESCE(last_error, '{}') AS payload, created_at, updated_at
FROM provider_sync_jobs
WHERE ($1::STRING = '' OR provider_connection_id::STRING = $1::STRING)
ORDER BY created_at, id;

-- @name GetSyncJobByIdempotency
-- @returns :opt
SELECT id::STRING AS id, provider_connection_id::STRING AS connection_id,
  date_from::STRING AS date_from, date_to::STRING AS date_to,
  status, attempt, available_at, started_at, finished_at,
  COALESCE(last_error, '{}') AS payload, created_at, updated_at
FROM provider_sync_jobs
WHERE provider_connection_id = $1::UUID
  AND idempotency_key_hash = $2::BYTES
  AND idempotency_expires_at > $3::TIMESTAMPTZ
ORDER BY created_at DESC, id DESC LIMIT 1;

-- @name ListClaimableSyncJobs
-- @returns :many
SELECT id::STRING AS id FROM provider_sync_jobs
WHERE (status = 'queued' AND available_at <= $1::TIMESTAMPTZ)
   OR (status = 'running' AND lease_expires_at <= $1::TIMESTAMPTZ)
ORDER BY available_at, id LIMIT $2::INT8;

-- @name ClaimSyncJob
-- @returns :opt
UPDATE provider_sync_jobs
SET status = 'running', started_at = $2::TIMESTAMPTZ, updated_at = $2::TIMESTAMPTZ,
  claim_token = gen_random_uuid(), lease_expires_at = $3::TIMESTAMPTZ
WHERE id = $1::UUID
  AND ((status = 'queued' AND available_at <= $2::TIMESTAMPTZ)
    OR (status = 'running' AND lease_expires_at <= $2::TIMESTAMPTZ))
RETURNING claim_token::STRING AS claim_token;

-- @name CompleteClaimedSyncJob
-- @returns :exec_result
UPDATE provider_sync_jobs
SET status = $3::STRING, finished_at = NULLIF($4::STRING, '')::TIMESTAMPTZ,
  available_at = $5::TIMESTAMPTZ, last_error = $6::STRING,
  updated_at = $7::TIMESTAMPTZ, claim_token = NULL, lease_expires_at = NULL
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'running';

-- @name RetryOAuthTokenRevocation
-- @returns :exec_result
UPDATE provider_token_revocation_jobs
SET status = 'pending', available_at = $3::TIMESTAMPTZ,
  lease_until = NULL, claim_token = NULL, updated_at = now()
WHERE id = $1::UUID AND status = 'processing' AND claim_token = $2::UUID;

-- @name DeadLetterOAuthTokenRevocation
-- @returns :exec_result
UPDATE provider_token_revocation_jobs
SET status = 'dead', available_at = $3::TIMESTAMPTZ,
  lease_until = NULL, claim_token = NULL,
  terminal_at = $3::TIMESTAMPTZ, terminal_reason = 'max_attempts_exhausted',
  updated_at = $3::TIMESTAMPTZ
WHERE id = $1::UUID AND status = 'processing' AND claim_token = $2::UUID;
