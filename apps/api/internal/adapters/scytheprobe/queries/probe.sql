-- @name UpsertProbeUser
-- @returns :one
UPSERT INTO users (id, primary_email)
VALUES ($1::STRING, $2::STRING)
RETURNING id, primary_email, email_verified_at;

-- @name CreateProbePasskey
-- @returns :one
INSERT INTO user_passkeys (
  user_id, credential_id, public_key, transports,
  verifier_credential, last_used_at
)
VALUES (
  $1::STRING, $2::BYTES, $3::BYTES, $4::STRING[],
  $5::JSONB, $6::TIMESTAMPTZ
)
RETURNING id, user_id, credential_id, transports,
  verifier_credential, last_used_at;

-- @name ListProbePasskeys
-- @returns :many
SELECT id, user_id, credential_id, transports,
  verifier_credential, last_used_at
FROM user_passkeys
WHERE user_id = ANY($1::STRING[])
ORDER BY id
LIMIT 10;

-- @name CountProbePasskeys
-- @returns :one
SELECT count(*) AS total
FROM user_passkeys
WHERE user_id = ANY($1::STRING[]);

-- @name MarkProbePasskeyUsed
-- @returns :one
UPDATE user_passkeys
SET last_used_at = $2::TIMESTAMPTZ
WHERE id = $1::UUID
RETURNING id, last_used_at;
