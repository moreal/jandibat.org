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
