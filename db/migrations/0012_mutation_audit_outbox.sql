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
  CONSTRAINT mutation_audit_outbox_outcome_chk CHECK (outcome = 'succeeded'),
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
