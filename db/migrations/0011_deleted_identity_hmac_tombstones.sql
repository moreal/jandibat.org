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
