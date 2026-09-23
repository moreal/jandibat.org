-- @name InsertOAuthState
-- @returns :exec
INSERT INTO auth_challenges (kind, challenge_hash, payload, expires_at)
VALUES ('oauth_state', $1::BYTES, $2::JSONB, $3::TIMESTAMPTZ);

-- @name ConsumeOAuthState
-- @returns :opt
UPDATE auth_challenges
SET consumed_at = now()
WHERE challenge_hash = $1::BYTES
  AND kind = 'oauth_state'
  AND consumed_at IS NULL
  AND expires_at > now()
  AND payload->>'ProviderID' = $2::STRING
  AND payload->>'RedirectURI' = $3::STRING
  AND (
    (NOT $4::BOOL AND COALESCE(payload->>'SessionBindingHash', '') = '')
    OR (
      COALESCE(payload->>'SessionBindingHash', '') <> ''
      AND $5::STRING <> ''
      AND payload->>'SessionBindingHash' = $5::STRING
    )
  )
RETURNING payload, expires_at;
