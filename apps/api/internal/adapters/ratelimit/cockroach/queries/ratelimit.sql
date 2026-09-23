-- @name IncrementRateLimitBucket
-- @returns :opt
INSERT INTO api_rate_limit_buckets (
  scope, key_hash, window_start, count, expires_at
) VALUES ($1::STRING, $2::BYTES, $3::TIMESTAMPTZ, 1, $4::TIMESTAMPTZ)
ON CONFLICT (scope, key_hash, window_start) DO UPDATE SET
  count = api_rate_limit_buckets.count + 1,
  expires_at = excluded.expires_at
WHERE api_rate_limit_buckets.count < $5::INT8
RETURNING count;
