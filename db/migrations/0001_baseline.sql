-- Canonical CockroachDB baseline. Existing databases and migration histories have no upgrade path.
-- Apply only to a new database; runtime roles are granted separately.

-- jandibat.org initial schema (CockroachDB)

CREATE TABLE IF NOT EXISTS users (
  id STRING PRIMARY KEY,
  primary_email STRING NOT NULL UNIQUE,
  email_verified_at TIMESTAMPTZ NULL,
  status STRING NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT users_status_chk CHECK (status IN ('active', 'disabled', 'pending', 'deletion_pending'))
);

CREATE TABLE IF NOT EXISTS subjects (
  id STRING PRIMARY KEY,
  owner_user_id STRING NULL REFERENCES users(id) ON DELETE SET NULL,
  handle STRING NOT NULL UNIQUE,
  display_name STRING NULL,
  timezone STRING NOT NULL,
  is_public BOOL NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS subjects_owner_user_idx
  ON subjects (owner_user_id);

CREATE TABLE IF NOT EXISTS user_passkeys (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id STRING NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id BYTES NOT NULL UNIQUE,
  public_key BYTES NOT NULL,
  aaguid BYTES NULL,
  sign_count INT8 NOT NULL DEFAULT 0,
  transports STRING[] NULL,
  label STRING NULL,
  verifier_credential JSONB NOT NULL DEFAULT '{}'::JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS user_passkeys_user_idx
  ON user_passkeys (user_id);

CREATE TABLE IF NOT EXISTS magic_link_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id STRING NULL REFERENCES users(id) ON DELETE CASCADE,
  email STRING NOT NULL,
  token_hash BYTES NOT NULL UNIQUE,
  purpose STRING NOT NULL DEFAULT 'signin',
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT magic_link_tokens_purpose_chk CHECK (purpose IN ('signin', 'verify_email'))
);

CREATE INDEX IF NOT EXISTS magic_link_tokens_email_idx
  ON magic_link_tokens (email, created_at DESC);

CREATE INDEX IF NOT EXISTS magic_link_tokens_retention_idx
  ON magic_link_tokens (expires_at);

CREATE TABLE IF NOT EXISTS user_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id STRING NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  session_token_hash BYTES NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ NULL,
  last_seen_at TIMESTAMPTZ NULL,
  ip INET NULL,
  user_agent STRING NULL
);

CREATE INDEX IF NOT EXISTS user_sessions_user_idx
  ON user_sessions (user_id, expires_at DESC);

CREATE INDEX IF NOT EXISTS user_sessions_retention_idx
  ON user_sessions (expires_at);

CREATE TABLE IF NOT EXISTS environments (
  id STRING PRIMARY KEY,
  key STRING NOT NULL,
  name STRING NOT NULL,
  scope STRING NOT NULL,
  owner_subject_id STRING NULL REFERENCES subjects(id) ON DELETE CASCADE,
  metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT environments_scope_chk CHECK (scope IN ('global', 'subject')),
  CONSTRAINT environments_owner_subject_chk CHECK (
    (scope = 'global' AND owner_subject_id IS NULL)
    OR
    (scope = 'subject' AND owner_subject_id IS NOT NULL)
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS environments_owner_key_uq
  ON environments (COALESCE(owner_subject_id, ''), key);

-- Define referenced providers before activity_facts for the final FK shape.
CREATE TABLE IF NOT EXISTS custom_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_user_id STRING NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  slug STRING NOT NULL,
  name STRING NOT NULL,
  description STRING NULL,
  status STRING NOT NULL DEFAULT 'active',
  configuration JSONB NOT NULL DEFAULT '{}'::JSONB,
  last_ingested_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT custom_providers_status_chk CHECK (status IN ('active', 'disabled'))
);

CREATE UNIQUE INDEX IF NOT EXISTS custom_providers_owner_slug_uq
  ON custom_providers (owner_user_id, slug);

CREATE UNIQUE INDEX IF NOT EXISTS custom_providers_subject_environment_uq
  ON custom_providers (subject_id, environment_id);

CREATE TABLE IF NOT EXISTS custom_provider_secrets (
  provider_id UUID PRIMARY KEY REFERENCES custom_providers(id) ON DELETE CASCADE,
  ingest_token_hash BYTES NOT NULL UNIQUE,
  ingest_token_key_id STRING NULL
);

CREATE INDEX IF NOT EXISTS custom_provider_secrets_key_rotation_idx
  ON custom_provider_secrets (ingest_token_key_id, provider_id)
  WHERE ingest_token_key_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS provider_connections (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  auth_method STRING NOT NULL,
  external_account_id STRING NULL,
  status STRING NOT NULL DEFAULT 'active',
  scopes STRING[] NULL,
  access_token_ciphertext BYTES NULL,
  refresh_token_ciphertext BYTES NULL,
  access_token_key_id STRING NULL,
  refresh_token_key_id STRING NULL,
  token_expires_at TIMESTAMPTZ NULL,
  sync_cursor JSONB NOT NULL DEFAULT '{}'::JSONB,
  last_synced_at TIMESTAMPTZ NULL,
  last_error STRING NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_connections_auth_method_chk CHECK (auth_method IN ('oauth2', 'token', 'none')),
  CONSTRAINT provider_connections_status_chk CHECK (status IN ('pending', 'active', 'disabled', 'revoked', 'error'))
);

CREATE UNIQUE INDEX IF NOT EXISTS provider_connections_subject_env_account_uq
  ON provider_connections (subject_id, environment_id, COALESCE(external_account_id, ''));

CREATE INDEX IF NOT EXISTS provider_connections_access_key_rotation_idx
  ON provider_connections (access_token_key_id, id)
  WHERE access_token_key_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS provider_connections_refresh_key_rotation_idx
  ON provider_connections (refresh_token_key_id, id)
  WHERE refresh_token_key_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS activity_facts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
  provider_connection_id UUID NULL REFERENCES provider_connections(id) ON DELETE SET NULL,
  custom_provider_id UUID NULL REFERENCES custom_providers(id) ON DELETE CASCADE,
  activity_date DATE NOT NULL,
  action STRING NOT NULL,
  metric_name STRING NOT NULL,
  metric_value INT8 NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ingested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT activity_facts_metric_non_negative_chk CHECK (metric_value >= 0)
);

CREATE INDEX IF NOT EXISTS activity_facts_subject_date_idx
  ON activity_facts (subject_id, activity_date DESC);

CREATE INDEX IF NOT EXISTS activity_facts_environment_date_idx
  ON activity_facts (environment_id, activity_date DESC);

CREATE UNIQUE INDEX IF NOT EXISTS activity_facts_dedupe_uq
  ON activity_facts (
    subject_id,
    environment_id,
    activity_date,
    action,
    metric_name,
    md5(metadata::STRING)
  );

CREATE INDEX IF NOT EXISTS activity_facts_custom_provider_idx
  ON activity_facts (custom_provider_id, activity_date DESC)
  WHERE custom_provider_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS activity_facts_retention_idx
  ON activity_facts (ingested_at);

CREATE TABLE IF NOT EXISTS timeline_cache (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  target_date DATE NOT NULL,
  payload JSONB NOT NULL,
  payload_schema_version INT4 NOT NULL DEFAULT 1,
  fetched_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NULL,
  failure_policy STRING NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT timeline_cache_failure_policy_chk CHECK (failure_policy IN ('keep_stale', 'purge'))
);

CREATE UNIQUE INDEX IF NOT EXISTS timeline_cache_subject_date_uq
  ON timeline_cache (subject_id, target_date);

CREATE INDEX IF NOT EXISTS timeline_cache_expires_at_idx
  ON timeline_cache (expires_at);

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

-- Phase 2/3 persistence required by the public contract and operations runbooks.

CREATE TABLE IF NOT EXISTS user_settings (
  user_id STRING PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  locale STRING NOT NULL DEFAULT 'en-US',
  timezone STRING NOT NULL DEFAULT 'UTC',
  theme STRING NOT NULL DEFAULT 'system',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT user_settings_locale_chk CHECK (length(locale) BETWEEN 2 AND 35),
  CONSTRAINT user_settings_timezone_chk CHECK (length(timezone) BETWEEN 1 AND 64),
  CONSTRAINT user_settings_theme_chk CHECK (
    theme IN ('system', 'light', 'dark', 'github-light', 'github-dark')
  )
);

CREATE TABLE IF NOT EXISTS subject_settings (
  subject_id STRING PRIMARY KEY REFERENCES subjects(id) ON DELETE CASCADE,
  default_theme STRING NOT NULL DEFAULT 'system',
  week_start STRING NOT NULL DEFAULT 'sunday',
  sync_enabled BOOL NOT NULL DEFAULT true,
  sync_interval_minutes INT4 NOT NULL DEFAULT 60,
  failure_policy STRING NOT NULL DEFAULT 'keep_stale',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT subject_settings_theme_chk CHECK (
    default_theme IN ('system', 'light', 'dark', 'github-light', 'github-dark')
  ),
  CONSTRAINT subject_settings_week_start_chk CHECK (week_start IN ('sunday', 'monday')),
  CONSTRAINT subject_settings_sync_interval_chk CHECK (sync_interval_minutes BETWEEN 15 AND 10080),
  CONSTRAINT subject_settings_failure_policy_chk CHECK (failure_policy IN ('keep_stale', 'purge'))
);

CREATE TABLE IF NOT EXISTS custom_activity_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  custom_provider_id UUID NOT NULL REFERENCES custom_providers(id) ON DELETE CASCADE,
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
  event_id STRING NOT NULL,
  activity_date DATE NOT NULL,
  action STRING NOT NULL,
  metric_name STRING NOT NULL,
  metric_value INT8 NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
  observed_at TIMESTAMPTZ NULL,
  ingested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT custom_activity_event_id_chk CHECK (length(event_id) BETWEEN 1 AND 255),
  CONSTRAINT custom_activity_action_chk CHECK (length(action) BETWEEN 1 AND 64),
  CONSTRAINT custom_activity_metric_name_chk CHECK (length(metric_name) BETWEEN 1 AND 64),
  CONSTRAINT custom_activity_metric_value_chk CHECK (metric_value >= 0),
  UNIQUE (custom_provider_id, event_id)
);

CREATE INDEX IF NOT EXISTS custom_activity_events_subject_date_idx
  ON custom_activity_events (subject_id, activity_date DESC);

CREATE INDEX IF NOT EXISTS custom_activity_events_environment_date_idx
  ON custom_activity_events (environment_id, activity_date DESC);

CREATE INDEX IF NOT EXISTS custom_activity_events_retention_idx
  ON custom_activity_events (ingested_at);

CREATE TABLE IF NOT EXISTS ingest_idempotency_keys (
  custom_provider_id UUID NOT NULL REFERENCES custom_providers(id) ON DELETE CASCADE,
  key_hash BYTES NOT NULL,
  request_hash BYTES NOT NULL,
  response_status INT4 NOT NULL,
  response_body JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (custom_provider_id, key_hash),
  CONSTRAINT ingest_idempotency_expiry_chk CHECK (expires_at >= created_at)
);

CREATE INDEX IF NOT EXISTS ingest_idempotency_keys_expiry_idx
  ON ingest_idempotency_keys (expires_at);

CREATE TABLE IF NOT EXISTS audit_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor_type STRING NOT NULL,
  actor_id STRING NULL,
  action STRING NOT NULL,
  target_type STRING NOT NULL,
  target_id STRING NULL,
  outcome STRING NOT NULL,
  request_id STRING NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
  CONSTRAINT audit_events_actor_type_chk CHECK (actor_type IN ('anonymous', 'user', 'system', 'custom_provider')),
  CONSTRAINT audit_events_outcome_chk CHECK (outcome IN ('succeeded', 'failed', 'denied')),
  CONSTRAINT audit_events_action_chk CHECK (length(action) BETWEEN 1 AND 128),
  CONSTRAINT audit_events_target_type_chk CHECK (length(target_type) BETWEEN 1 AND 64),
  CONSTRAINT audit_events_request_id_chk CHECK (length(request_id) BETWEEN 1 AND 255)
);

CREATE INDEX IF NOT EXISTS audit_events_occurred_at_idx
  ON audit_events (occurred_at DESC);

CREATE INDEX IF NOT EXISTS audit_events_actor_idx
  ON audit_events (actor_type, actor_id, occurred_at DESC);

CREATE INDEX IF NOT EXISTS audit_events_target_idx
  ON audit_events (target_type, target_id, occurred_at DESC);

-- Cross-instance fixed-window rate limiting. Only a SHA-256 digest of the
-- caller/provider key is persisted; raw email, IP, token, and provider keys
-- must never be stored in this table.

CREATE TABLE IF NOT EXISTS api_rate_limit_buckets (
  scope STRING NOT NULL,
  key_hash BYTES NOT NULL,
  window_start TIMESTAMPTZ NOT NULL,
  count INT8 NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (scope, key_hash, window_start),
  CONSTRAINT api_rate_limit_buckets_count_nonnegative CHECK (count >= 0)
);

CREATE INDEX IF NOT EXISTS api_rate_limit_buckets_expires_at_idx
  ON api_rate_limit_buckets (expires_at);

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

-- Durable maintenance progress, legal holds, and resumable deletion requests.
-- target_id and subject_ids are stable internal identifiers. target_email is
-- intentionally omitted: account lookup is completed before the durable
-- deletion request is created, avoiding another retained email copy.

CREATE TABLE IF NOT EXISTS maintenance_checkpoints (
  operation STRING NOT NULL,
  scope STRING NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::JSONB,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operation, scope),
  CONSTRAINT maintenance_checkpoints_operation_chk CHECK (
    operation IN ('retention', 'credential_reencryption', 'account_deletion')
  ),
  CONSTRAINT maintenance_checkpoints_scope_chk CHECK (
    length(scope) BETWEEN 1 AND 255
  )
);

CREATE TABLE IF NOT EXISTS legal_holds (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  target_type STRING NOT NULL,
  target_id STRING NOT NULL,
  approval_ref STRING NOT NULL,
  reason STRING NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT legal_holds_target_type_chk CHECK (
    target_type IN ('account', 'subject', 'audit_event')
  ),
  CONSTRAINT legal_holds_target_id_chk CHECK (length(target_id) BETWEEN 1 AND 255),
  CONSTRAINT legal_holds_approval_ref_chk CHECK (length(approval_ref) BETWEEN 1 AND 255),
  CONSTRAINT legal_holds_reason_chk CHECK (length(reason) BETWEEN 1 AND 1000),
  CONSTRAINT legal_holds_expiry_chk CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS legal_holds_active_expiry_idx
  ON legal_holds (target_type, target_id, expires_at DESC);

CREATE TABLE IF NOT EXISTS deletion_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id STRING NOT NULL UNIQUE,
  target_type STRING NOT NULL,
  target_id STRING NOT NULL,
  status STRING NOT NULL DEFAULT 'requested',
  last_completed_stage STRING NULL,
  error_code STRING NULL,
  subject_ids STRING[] NOT NULL DEFAULT ARRAY[]::STRING[],
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ NULL,
  backup_expiry_at TIMESTAMPTZ NULL,
  audit_event_id UUID NULL REFERENCES audit_events(id) ON DELETE SET NULL,
  CONSTRAINT deletion_requests_target_type_chk CHECK (
    target_type IN ('account', 'subject')
  ),
  CONSTRAINT deletion_requests_target_id_chk CHECK (length(target_id) BETWEEN 1 AND 255),
  CONSTRAINT deletion_requests_status_chk CHECK (
    status IN ('requested', 'revoking', 'deleting_primary', 'verifying', 'completed', 'failed')
  ),
  CONSTRAINT deletion_requests_stage_chk CHECK (
    last_completed_stage IS NULL OR last_completed_stage IN (
      'requested', 'credentials_revoked', 'primary_deleted', 'verified', 'completed'
    )
  ),
  CONSTRAINT deletion_requests_error_code_chk CHECK (
    error_code IS NULL OR length(error_code) BETWEEN 1 AND 128
  ),
  CONSTRAINT deletion_requests_completion_chk CHECK (
    (status = 'completed' AND completed_at IS NOT NULL AND error_code IS NULL)
    OR (status <> 'completed' AND completed_at IS NULL)
  ),
  CONSTRAINT deletion_requests_backup_expiry_chk CHECK (
    backup_expiry_at IS NULL OR backup_expiry_at >= requested_at
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS deletion_requests_incomplete_target_uq
  ON deletion_requests (target_type, target_id)
  WHERE status <> 'completed';

CREATE INDEX IF NOT EXISTS deletion_requests_pending_idx
  ON deletion_requests (status, updated_at, id)
  WHERE status <> 'completed';

CREATE INDEX IF NOT EXISTS deletion_requests_backup_expiry_idx
  ON deletion_requests (backup_expiry_at, id)
  WHERE backup_expiry_at IS NOT NULL;

-- Prevent a deleted account from being recreated while backup copies and the
-- deletion replay obligation still exist. Only a canonical email SHA-256
-- digest is retained; raw email is forbidden in this table.

CREATE TABLE IF NOT EXISTS deleted_identity_tombstones (
  email_hash BYTES PRIMARY KEY,
  deletion_request_id UUID NOT NULL UNIQUE REFERENCES deletion_requests(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT deleted_identity_tombstones_hash_chk CHECK (length(email_hash) = 32),
  CONSTRAINT deleted_identity_tombstones_expiry_chk CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS deleted_identity_tombstones_expiry_idx
  ON deleted_identity_tombstones (expires_at, email_hash);

-- provider_connections is schema-locked for changefeed safety. Keep explicit
-- private-data consent durable in a companion table so adding consent does not
-- require a non-atomic unlock/DDL/relock sequence. Absence means false.
CREATE TABLE IF NOT EXISTS provider_connection_private_consents (
  connection_id UUID PRIMARY KEY REFERENCES provider_connections(id) ON DELETE CASCADE,
  enabled BOOL NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_connection_private_consents_enabled_chk CHECK (enabled = true)
);

-- deletion_requests is also schema-locked. Keep executor coordination in an
-- atomic companion table. The API only inserts deletion_requests; maintenance
-- creates/claims this row and owns retries.
CREATE TABLE IF NOT EXISTS deletion_request_claims (
  deletion_request_id UUID PRIMARY KEY REFERENCES deletion_requests(id) ON DELETE CASCADE,
  attempts INT8 NOT NULL DEFAULT 0,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ NULL,
  claim_token UUID NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT deletion_request_claims_attempts_chk CHECK (attempts BETWEEN 0 AND 100),
  CONSTRAINT deletion_request_claims_lease_chk CHECK (
    (lease_until IS NULL AND claim_token IS NULL)
    OR (lease_until IS NOT NULL AND claim_token IS NOT NULL)
  )
);

CREATE INDEX IF NOT EXISTS deletion_request_claims_available_idx
  ON deletion_request_claims (available_at, updated_at, deletion_request_id);

CREATE INDEX IF NOT EXISTS deletion_request_claims_lease_idx
  ON deletion_request_claims (lease_until, deletion_request_id)
  WHERE lease_until IS NOT NULL;

-- Keep the Internet-facing deletion enqueue boundary separate from the
-- maintenance-owned workflow table. CockroachDB requires read privilege on a
-- referenced table when inserting a row with a nullable foreign key, even
-- when the foreign-key value is NULL. This FK-free inbox lets the API enqueue
-- without gaining read access to append-only audit_events.

CREATE TABLE IF NOT EXISTS deletion_request_inbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id STRING NOT NULL UNIQUE,
  target_type STRING NOT NULL,
  target_id STRING NOT NULL,
  status STRING NOT NULL DEFAULT 'requested',
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  promoted_at TIMESTAMPTZ NULL,
  CONSTRAINT deletion_request_inbox_request_id_chk CHECK (length(request_id) BETWEEN 1 AND 255),
  CONSTRAINT deletion_request_inbox_target_type_chk CHECK (target_type IN ('account', 'subject')),
  CONSTRAINT deletion_request_inbox_target_id_chk CHECK (length(target_id) BETWEEN 1 AND 255),
  CONSTRAINT deletion_request_inbox_status_chk CHECK (status IN ('requested', 'promoted')),
  CONSTRAINT deletion_request_inbox_promotion_chk CHECK (
    (status = 'requested' AND promoted_at IS NULL)
    OR (status = 'promoted' AND promoted_at IS NOT NULL)
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS deletion_request_inbox_active_target_uq
  ON deletion_request_inbox (target_type, target_id)
  WHERE status = 'requested';

CREATE INDEX IF NOT EXISTS deletion_request_inbox_pending_idx
  ON deletion_request_inbox (status, requested_at, id)
  WHERE status = 'requested';

-- Durable, timing-independent Magic Link delivery. The API stores only an
-- intent. The worker creates the one-time token in memory and activates only
-- its one-way hash and expiry in this FK-free outbox row.
CREATE TABLE IF NOT EXISTS magic_link_mail_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash BYTES NULL,
  token_expires_at TIMESTAMPTZ NULL,
  consumed_at TIMESTAMPTZ NULL,
  recipient_email STRING NOT NULL,
  redirect_uri STRING NOT NULL DEFAULT '',
  purpose STRING NOT NULL DEFAULT 'signin',
  status STRING NOT NULL DEFAULT 'pending',
  attempts INT8 NOT NULL DEFAULT 0,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ NULL,
  claim_token UUID NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  terminal_at TIMESTAMPTZ NULL,
  terminal_reason STRING NULL,
  CONSTRAINT magic_link_mail_outbox_recipient_chk CHECK (length(recipient_email) BETWEEN 3 AND 320),
  CONSTRAINT magic_link_mail_outbox_redirect_chk CHECK (length(redirect_uri) <= 2048),
  CONSTRAINT magic_link_mail_outbox_purpose_chk CHECK (purpose = 'signin'),
  CONSTRAINT magic_link_mail_outbox_token_hash_chk CHECK (token_hash IS NULL OR length(token_hash) = 32),
  CONSTRAINT magic_link_mail_outbox_status_chk CHECK (status IN ('pending', 'processing', 'sent', 'dead', 'superseded')),
  CONSTRAINT magic_link_mail_outbox_attempts_chk CHECK (attempts BETWEEN 0 AND 5),
  CONSTRAINT magic_link_mail_outbox_lease_chk CHECK (
    (status = 'processing' AND lease_until IS NOT NULL AND claim_token IS NOT NULL)
    OR (status <> 'processing' AND lease_until IS NULL AND claim_token IS NULL)
  ),
  CONSTRAINT magic_link_mail_outbox_token_state_chk CHECK (
    (status = 'pending' AND token_hash IS NULL AND token_expires_at IS NULL AND consumed_at IS NULL)
    OR (status = 'processing' AND (
      (token_hash IS NULL AND token_expires_at IS NULL AND consumed_at IS NULL)
      OR (token_hash IS NOT NULL AND token_expires_at IS NOT NULL AND token_expires_at > updated_at)
    ))
    OR (status = 'sent' AND token_hash IS NOT NULL AND token_expires_at IS NOT NULL)
    OR (status IN ('dead', 'superseded') AND token_hash IS NULL AND token_expires_at IS NULL AND consumed_at IS NULL)
  ),
  CONSTRAINT magic_link_mail_outbox_terminal_chk CHECK (
    (status IN ('dead', 'superseded') AND terminal_at IS NOT NULL AND terminal_reason IS NOT NULL)
    OR (status NOT IN ('dead', 'superseded') AND terminal_at IS NULL AND terminal_reason IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS magic_link_mail_outbox_available_idx
  ON magic_link_mail_outbox (status, available_at, created_at, id)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS magic_link_mail_outbox_processing_lease_idx
  ON magic_link_mail_outbox (lease_until, id)
  WHERE status = 'processing';

CREATE INDEX IF NOT EXISTS magic_link_mail_outbox_terminal_idx
  ON magic_link_mail_outbox (terminal_at DESC, id)
  WHERE status IN ('dead', 'superseded');

-- Versioned HMAC-SHA-256 identities prevent offline dictionary enumeration of
-- canonical email addresses. The legacy SHA-256 table remains readable only
-- for its maximum outstanding retention window because it cannot be backfilled
-- without retaining raw email.
CREATE TABLE IF NOT EXISTS deleted_identity_tombstones_v2 (
  identity_key_id STRING NOT NULL,
  identity_digest BYTES NOT NULL,
  deletion_request_id UUID NOT NULL UNIQUE REFERENCES deletion_requests(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (identity_key_id, identity_digest),
  CONSTRAINT deleted_identity_tombstones_v2_key_id_chk CHECK (length(identity_key_id) BETWEEN 1 AND 128),
  CONSTRAINT deleted_identity_tombstones_v2_digest_chk CHECK (length(identity_digest) = 32),
  CONSTRAINT deleted_identity_tombstones_v2_expiry_chk CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS deleted_identity_tombstones_v2_expiry_idx
  ON deleted_identity_tombstones_v2 (expires_at, identity_key_id, identity_digest);

-- Same-transaction success audit for the mutation adapters that explicitly
-- adopt this outbox. A worker copies the stable, redacted payload into the
-- append-only audit sink with idempotent event identity.
CREATE TABLE IF NOT EXISTS mutation_audit_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  audit_event_id UUID NOT NULL UNIQUE,
  request_id STRING NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL,
  actor_type STRING NOT NULL,
  actor_id STRING NULL,
  action STRING NOT NULL,
  target_type STRING NOT NULL,
  target_id STRING NULL,
  outcome STRING NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
  status STRING NOT NULL DEFAULT 'pending',
  attempts INT8 NOT NULL DEFAULT 0,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ NULL,
  claim_token UUID NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  delivered_at TIMESTAMPTZ NULL,
  terminal_at TIMESTAMPTZ NULL,
  terminal_reason STRING NULL,
  CONSTRAINT mutation_audit_outbox_request_id_chk CHECK (length(request_id) BETWEEN 1 AND 255),
  CONSTRAINT mutation_audit_outbox_actor_type_chk CHECK (actor_type IN ('anonymous', 'user', 'system', 'custom_provider')),
  CONSTRAINT mutation_audit_outbox_action_chk CHECK (length(action) BETWEEN 1 AND 128),
  CONSTRAINT mutation_audit_outbox_target_type_chk CHECK (length(target_type) BETWEEN 1 AND 64),
  CONSTRAINT mutation_audit_outbox_outcome_v2_chk CHECK (outcome IN ('succeeded', 'failed', 'denied')),
  CONSTRAINT mutation_audit_outbox_attempts_chk CHECK (attempts BETWEEN 0 AND 5),
  CONSTRAINT mutation_audit_outbox_lease_chk CHECK (
    (status = 'processing' AND lease_until IS NOT NULL AND claim_token IS NOT NULL)
    OR (status <> 'processing' AND lease_until IS NULL AND claim_token IS NULL)
  ),
  CONSTRAINT mutation_audit_outbox_terminal_chk CHECK (
    (status = 'pending' AND delivered_at IS NULL AND terminal_at IS NULL AND terminal_reason IS NULL)
    OR (status = 'processing' AND delivered_at IS NULL AND terminal_at IS NULL AND terminal_reason IS NULL)
    OR (status = 'delivered' AND delivered_at IS NOT NULL AND terminal_at IS NULL AND terminal_reason IS NULL)
    OR (status = 'dead' AND delivered_at IS NULL AND terminal_at IS NOT NULL AND terminal_reason IS NOT NULL)
  ),
  CONSTRAINT mutation_audit_outbox_status_chk CHECK (status IN ('pending', 'processing', 'delivered', 'dead'))
);

CREATE INDEX IF NOT EXISTS mutation_audit_outbox_pending_idx
  ON mutation_audit_outbox (status, available_at, created_at, id)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS mutation_audit_outbox_processing_lease_idx
  ON mutation_audit_outbox (lease_until, id)
  WHERE status = 'processing';

CREATE INDEX IF NOT EXISTS mutation_audit_outbox_terminal_idx
  ON mutation_audit_outbox (COALESCE(delivered_at, terminal_at), id)
  WHERE status IN ('delivered', 'dead');
