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
