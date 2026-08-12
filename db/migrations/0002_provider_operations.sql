-- Provider extensibility and asynchronous-operation support.

CREATE TABLE IF NOT EXISTS auth_challenges (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id STRING NULL REFERENCES users(id) ON DELETE CASCADE,
  kind STRING NOT NULL,
  challenge_hash BYTES NOT NULL UNIQUE,
  payload JSONB NOT NULL DEFAULT '{}'::JSONB,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT auth_challenges_kind_chk CHECK (
    kind IN ('passkey_registration', 'passkey_authentication', 'oauth_state')
  )
);

CREATE INDEX IF NOT EXISTS auth_challenges_user_expiry_idx
  ON auth_challenges (user_id, expires_at DESC);

CREATE INDEX IF NOT EXISTS auth_challenges_expiry_idx
  ON auth_challenges (expires_at);

CREATE TABLE IF NOT EXISTS provider_sync_jobs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_connection_id UUID NOT NULL REFERENCES provider_connections(id) ON DELETE CASCADE,
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  date_from DATE NULL,
  date_to DATE NULL,
  status STRING NOT NULL DEFAULT 'queued',
  attempt INT4 NOT NULL DEFAULT 0,
  max_attempts INT4 NOT NULL DEFAULT 5,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_expires_at TIMESTAMPTZ NULL,
  started_at TIMESTAMPTZ NULL,
  finished_at TIMESTAMPTZ NULL,
  last_error STRING NULL,
  idempotency_key_hash BYTES NULL,
  request_hash BYTES NULL,
  idempotency_expires_at TIMESTAMPTZ NULL,
  claim_token UUID NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_sync_jobs_status_chk CHECK (
    status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')
  ),
  CONSTRAINT provider_sync_jobs_attempt_chk CHECK (
    attempt >= 0 AND max_attempts > 0 AND attempt <= max_attempts
  ),
  CONSTRAINT provider_sync_jobs_date_range_chk CHECK (
    date_from IS NULL OR date_to IS NULL OR date_from <= date_to
  ),
  CONSTRAINT provider_sync_jobs_claim_lease_chk CHECK (
    (status = 'running' AND lease_expires_at IS NOT NULL AND claim_token IS NOT NULL)
    OR
    (status <> 'running' AND lease_expires_at IS NULL AND claim_token IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS provider_sync_jobs_dequeue_idx
  ON provider_sync_jobs (status, available_at, created_at);

CREATE INDEX IF NOT EXISTS provider_sync_jobs_connection_idx
  ON provider_sync_jobs (provider_connection_id, created_at DESC);

CREATE INDEX IF NOT EXISTS provider_sync_jobs_retention_idx
  ON provider_sync_jobs (finished_at)
  WHERE finished_at IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS provider_sync_jobs_idempotency_uq
  ON provider_sync_jobs (provider_connection_id, idempotency_key_hash)
  WHERE idempotency_key_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS provider_sync_jobs_idempotency_expiry_idx
  ON provider_sync_jobs (idempotency_expires_at)
  WHERE idempotency_expires_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS provider_sync_jobs_running_lease_idx
  ON provider_sync_jobs (lease_expires_at)
  WHERE status = 'running';

CREATE TABLE IF NOT EXISTS activity_refresh_cache (
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  activity_date DATE NOT NULL,
  fetched_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (subject_id, environment_id, activity_date)
);

CREATE INDEX IF NOT EXISTS activity_refresh_cache_fetched_at_idx
  ON activity_refresh_cache (fetched_at);
