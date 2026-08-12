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

-- Defined before activity_facts so the final FK shape can be created without
-- a later ALTER on a CockroachDB schema-locked table.
CREATE TABLE IF NOT EXISTS custom_providers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_user_id STRING NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  slug STRING NOT NULL,
  name STRING NOT NULL,
  description STRING NULL,
  status STRING NOT NULL DEFAULT 'active',
  ingest_token_hash BYTES NOT NULL UNIQUE,
  ingest_token_key_id STRING NULL,
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

CREATE INDEX IF NOT EXISTS custom_providers_key_rotation_idx
  ON custom_providers (ingest_token_key_id, id)
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
