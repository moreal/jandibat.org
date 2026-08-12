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
