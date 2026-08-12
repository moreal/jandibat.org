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
