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
