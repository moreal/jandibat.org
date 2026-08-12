-- Disconnect is locally atomic while provider-side OAuth revocation is retried
-- by the credential-bearing worker. The API stores only encrypted token bytes;
-- a per-claim token prevents an expired worker from completing a newer lease.

CREATE TABLE IF NOT EXISTS provider_token_revocation_jobs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  connection_id UUID NULL UNIQUE
    REFERENCES provider_connections(id) ON DELETE SET NULL,
  provider_id STRING NOT NULL,
  token_ciphertext BYTES NOT NULL,
  token_key_id STRING NULL,
  status STRING NOT NULL DEFAULT 'pending',
  attempts INT8 NOT NULL DEFAULT 0,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ NULL,
  claim_token UUID NULL,
  terminal_at TIMESTAMPTZ NULL,
  terminal_reason STRING NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_token_revocation_jobs_status_chk CHECK (
    status IN ('pending', 'processing', 'dead')
  ),
  CONSTRAINT provider_token_revocation_jobs_attempts_chk CHECK (attempts >= 0),
  CONSTRAINT provider_token_revocation_jobs_lease_chk CHECK (
    (status = 'pending' AND lease_until IS NULL AND claim_token IS NULL AND terminal_at IS NULL AND terminal_reason IS NULL)
    OR
    (status = 'processing' AND lease_until IS NOT NULL AND claim_token IS NOT NULL AND terminal_at IS NULL AND terminal_reason IS NULL)
    OR
    (status = 'dead' AND lease_until IS NULL AND claim_token IS NULL AND terminal_at IS NOT NULL AND terminal_reason IS NOT NULL)
  ),
  CONSTRAINT provider_token_revocation_jobs_terminal_reason_chk CHECK (
    terminal_reason IS NULL OR terminal_reason IN ('max_attempts_exhausted')
  )
);

CREATE INDEX IF NOT EXISTS provider_token_revocation_jobs_available_idx
  ON provider_token_revocation_jobs (status, available_at, id);

CREATE INDEX IF NOT EXISTS provider_token_revocation_jobs_processing_lease_idx
  ON provider_token_revocation_jobs (lease_until)
  WHERE status = 'processing';

CREATE INDEX IF NOT EXISTS provider_token_revocation_jobs_dead_terminal_idx
  ON provider_token_revocation_jobs (terminal_at DESC, id)
  WHERE status = 'dead';

-- Manual synchronization idempotency must be visible to CockroachDB rather
-- than hidden only in the compatibility JSON payload. Raw idempotency keys are
-- never stored; both values below are SHA-256 digests.
WITH legacy_payloads AS (
  SELECT
    id,
    regexp_extract(
      last_error,
      '"idempotency_key_hash"[[:space:]]*:[[:space:]]*"([0-9A-Fa-f]{64})"'
    ) AS key_hash_hex,
    regexp_extract(
      last_error,
      '"request_hash"[[:space:]]*:[[:space:]]*"([0-9A-Fa-f]{64})"'
    ) AS request_hash_hex,
    regexp_extract(
      last_error,
      '"idempotency_expires_at"[[:space:]]*:[[:space:]]*"([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.+-]+Z?)"'
    ) AS expires_at_text
  FROM provider_sync_jobs
  WHERE last_error IS NOT NULL
), valid_payloads AS (
  SELECT
    id,
    ('\x' || key_hash_hex)::BYTES AS key_hash,
    ('\x' || request_hash_hex)::BYTES AS request_digest,
    parse_timestamp(expires_at_text)::TIMESTAMPTZ AS expires_at
  FROM legacy_payloads
  WHERE key_hash_hex <> ''
    AND request_hash_hex <> ''
    AND expires_at_text <> ''
)
UPDATE provider_sync_jobs AS jobs
SET idempotency_key_hash = valid_payloads.key_hash,
    request_hash = valid_payloads.request_digest,
    idempotency_expires_at = valid_payloads.expires_at
FROM valid_payloads
WHERE jobs.id = valid_payloads.id
  AND jobs.idempotency_key_hash IS NULL;

-- Preserve every historical job while retaining the newest reservation if an
-- older multi-replica deployment already produced duplicate digests.
WITH duplicate_reservations AS (
  SELECT id, row_number() OVER (
    PARTITION BY provider_connection_id, idempotency_key_hash
    ORDER BY created_at DESC, id DESC
  ) AS reservation_rank
  FROM provider_sync_jobs
  WHERE idempotency_key_hash IS NOT NULL
)
UPDATE provider_sync_jobs AS jobs
SET idempotency_key_hash = NULL,
    request_hash = NULL,
    idempotency_expires_at = NULL
FROM duplicate_reservations
WHERE jobs.id = duplicate_reservations.id
  AND duplicate_reservations.reservation_rank > 1;

-- Normalize pre-lease rows before adding the worker claim invariant. Existing
-- running work is made immediately reclaimable because no new worker can know
-- a token generated during this migration.
UPDATE provider_sync_jobs
SET lease_expires_at = now(), claim_token = gen_random_uuid()
WHERE status = 'running';

UPDATE provider_sync_jobs
SET lease_expires_at = NULL, claim_token = NULL
WHERE status <> 'running';

-- Link projected custom facts to their provider so provider/subject deletion
-- is enforced by the database even when an ingest was already in flight.
UPDATE activity_facts AS facts
SET custom_provider_id = providers.id
FROM custom_providers AS providers
WHERE facts.custom_provider_id IS NULL
  AND facts.metadata->>'custom_provider_id' = providers.id::STRING
  AND providers.subject_id = facts.subject_id
  AND providers.environment_id = facts.environment_id;

-- Move legacy slug-based custom environments into the collision-free,
-- provider-bound namespace used by current writes. The new prefix is disjoint
-- from every legacy `custom:<slug>` identifier, so each statement is safe to
-- rerun if execution stops before the migration checksum is recorded.
INSERT INTO environments (
  id, key, name, scope, owner_subject_id, metadata, created_at, updated_at
)
SELECT
  'custom-provider:' || providers.id::STRING,
  'custom-provider:' || providers.id::STRING,
  providers.name,
  'subject',
  providers.subject_id,
  jsonb_build_object(
    'category', 'custom',
    'custom_provider_id', providers.id::STRING
  ),
  providers.created_at,
  providers.updated_at
FROM custom_providers AS providers
WHERE providers.environment_id <> 'custom-provider:' || providers.id::STRING
ON CONFLICT (id) DO UPDATE SET
  key = excluded.key,
  name = excluded.name,
  metadata = excluded.metadata,
  updated_at = excluded.updated_at
WHERE environments.scope = 'subject'
  AND environments.owner_subject_id = excluded.owner_subject_id;

UPDATE custom_activity_events AS events
SET environment_id = 'custom-provider:' || providers.id::STRING
FROM custom_providers AS providers
WHERE events.custom_provider_id = providers.id
  AND events.environment_id <> 'custom-provider:' || providers.id::STRING;

UPDATE activity_facts AS facts
SET environment_id = 'custom-provider:' || providers.id::STRING
FROM custom_providers AS providers
WHERE facts.custom_provider_id = providers.id
  AND facts.environment_id <> 'custom-provider:' || providers.id::STRING;

DELETE FROM activity_refresh_cache AS cache
USING custom_providers AS providers
WHERE cache.subject_id = providers.subject_id
  AND cache.environment_id = providers.environment_id
  AND providers.environment_id <> 'custom-provider:' || providers.id::STRING;

UPDATE custom_providers
SET environment_id = 'custom-provider:' || id::STRING
WHERE environment_id <> 'custom-provider:' || id::STRING;

DELETE FROM environments AS legacy
WHERE legacy.id LIKE 'custom:%'
  AND NOT EXISTS (
    SELECT 1 FROM custom_providers WHERE custom_providers.environment_id = legacy.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM provider_connections WHERE provider_connections.environment_id = legacy.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM provider_sync_jobs WHERE provider_sync_jobs.environment_id = legacy.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM activity_facts WHERE activity_facts.environment_id = legacy.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM custom_activity_events WHERE custom_activity_events.environment_id = legacy.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM activity_refresh_cache WHERE activity_refresh_cache.environment_id = legacy.id
  );

UPDATE environments AS environment
SET key = providers.slug,
    name = providers.name,
    metadata = jsonb_build_object(
      'category', 'custom',
      'custom_provider_id', providers.id::STRING
    ),
    updated_at = providers.updated_at
FROM custom_providers AS providers
WHERE environment.id = 'custom-provider:' || providers.id::STRING
  AND environment.scope = 'subject'
  AND environment.owner_subject_id = providers.subject_id;
