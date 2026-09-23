-- @name GetUserByID
-- @returns :one
SELECT id, primary_email, status, email_verified_at, created_at, updated_at
FROM users WHERE id = $1::STRING;

-- @name GetActiveIdentityTombstone
-- @returns :one
SELECT EXISTS (
  SELECT 1 FROM deleted_identity_tombstones_v2
  WHERE identity_key_id = $1::STRING AND identity_digest = $2::BYTES
    AND expires_at > $3::TIMESTAMPTZ
) AS exists;

-- @name InsertOrFindUser
-- @returns :opt
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
SELECT $1::STRING, $2::STRING, $3::TIMESTAMPTZ, 'active', $3::TIMESTAMPTZ, $3::TIMESTAMPTZ
WHERE NOT EXISTS (
  SELECT 1 FROM deleted_identity_tombstones
  WHERE email_hash = $4::BYTES AND expires_at > $3::TIMESTAMPTZ
)
ON CONFLICT (primary_email) DO UPDATE SET primary_email = excluded.primary_email
RETURNING id, primary_email, status, email_verified_at, created_at, updated_at;

-- @name InsertCredential
-- @returns :exec
INSERT INTO user_passkeys (
  id, user_id, credential_id, public_key, aaguid, sign_count,
  transports, verifier_credential, label, created_at, last_used_at
) VALUES ($1::UUID, $2::STRING, $3::BYTES, $4::BYTES, $5::BYTES, $6::INT8,
          $7::STRING[], $8::JSONB, NULLIF($9::STRING, ''), $10::TIMESTAMPTZ, $11::TIMESTAMPTZ);

-- @name GetCredentialByCredentialId
-- @returns :opt
SELECT id::STRING AS id, user_id, credential_id, public_key, aaguid, sign_count,
       transports, verifier_credential, COALESCE(label, '') AS label, created_at, last_used_at
FROM user_passkeys WHERE credential_id = $1::BYTES;

-- @name ListCredentialsByUser
-- @returns :many
SELECT id::STRING AS id, user_id, credential_id, public_key, aaguid, sign_count,
       transports, verifier_credential, COALESCE(label, '') AS label, created_at, last_used_at
FROM user_passkeys WHERE user_id = $1::STRING ORDER BY id;

-- @name UpdateCredentialUse
-- @returns :exec_result
UPDATE user_passkeys SET sign_count = $3::INT8, verifier_credential = $4::JSONB,
  last_used_at = $5::TIMESTAMPTZ
WHERE credential_id = $1::BYTES AND sign_count = $2::INT8;

-- @name GetCredentialExists
-- @returns :one
SELECT EXISTS (SELECT 1 FROM user_passkeys WHERE credential_id = $1::BYTES) AS exists;

-- @name InsertActiveSession
-- @returns :exec_result
INSERT INTO user_sessions (id, user_id, session_token_hash, created_at, expires_at,
  revoked_at, last_seen_at, ip, user_agent)
SELECT $1::UUID, $2::STRING, $3::BYTES, $4::TIMESTAMPTZ, $5::TIMESTAMPTZ,
  NULLIF($6::STRING, '')::TIMESTAMPTZ, NULLIF($7::STRING, '')::TIMESTAMPTZ,
  NULLIF($8::STRING, '')::INET,
  NULLIF($9::STRING, '')
FROM users WHERE id = $2::STRING AND status = 'active';

-- @name UseActiveSession
-- @returns :opt
UPDATE user_sessions SET last_seen_at = $2::TIMESTAMPTZ
WHERE session_token_hash = $1::BYTES AND revoked_at IS NULL AND expires_at > $2::TIMESTAMPTZ
  AND EXISTS (SELECT 1 FROM users WHERE users.id = user_sessions.user_id AND users.status = 'active')
RETURNING id::STRING AS id, user_id, session_token_hash, created_at, expires_at,
  revoked_at, last_seen_at, COALESCE(ip::STRING, '') AS ip, COALESCE(user_agent, '') AS user_agent;

-- @name GetSessionState
-- @returns :opt
SELECT revoked_at, expires_at FROM user_sessions WHERE session_token_hash = $1::BYTES;

-- @name RevokeSessionByHash
-- @returns :exec_result
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $2::TIMESTAMPTZ)
WHERE session_token_hash = $1::BYTES;

-- @name GetSessionByHash
-- @returns :opt
SELECT id::STRING AS id, user_id, session_token_hash, created_at, expires_at,
  revoked_at, last_seen_at, COALESCE(ip::STRING, '') AS ip, COALESCE(user_agent, '') AS user_agent
FROM user_sessions WHERE session_token_hash = $1::BYTES;

-- @name ListSessionsByUser
-- @returns :many
SELECT id::STRING AS id, user_id, session_token_hash, created_at, expires_at,
  revoked_at, last_seen_at, COALESCE(ip::STRING, '') AS ip, COALESCE(user_agent, '') AS user_agent
FROM user_sessions WHERE user_id = $1::STRING ORDER BY created_at DESC, id;

-- @name GetSessionOwnedByUserExists
-- @returns :one
SELECT EXISTS (SELECT 1 FROM user_sessions WHERE user_id = $1::STRING AND session_token_hash = $2::BYTES) AS exists;

-- @name RevokeOtherSessions
-- @returns :exec
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $3::TIMESTAMPTZ)
WHERE user_id = $1::STRING AND session_token_hash != $2::BYTES;

-- @name RevokeSessionById
-- @returns :exec_result
UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, $3::TIMESTAMPTZ)
WHERE user_id = $1::STRING AND id = $2::UUID;

-- @name InsertCeremony
-- @returns :exec
INSERT INTO auth_challenges (id, user_id, kind, challenge_hash, payload, expires_at, consumed_at, created_at)
VALUES ($1::UUID, NULLIF($2::STRING, ''), $3::STRING, $4::BYTES, $5::JSONB,
        $6::TIMESTAMPTZ, NULLIF($7::STRING, '')::TIMESTAMPTZ, $8::TIMESTAMPTZ);

-- @name ConsumeCeremony
-- @returns :opt
UPDATE auth_challenges SET consumed_at = $3::TIMESTAMPTZ
WHERE id = $1::UUID AND kind = $2::STRING AND consumed_at IS NULL AND expires_at > $3::TIMESTAMPTZ
RETURNING id::STRING AS id, kind, COALESCE(user_id, '') AS user_id,
  payload, created_at, expires_at, consumed_at;

-- @name GetCeremonyState
-- @returns :opt
SELECT kind, consumed_at, expires_at FROM auth_challenges WHERE id = $1::UUID;

-- @name SupersedeLegacyMagicLinks
-- @returns :exec
UPDATE magic_link_tokens SET consumed_at = $3::TIMESTAMPTZ
WHERE email = $1::STRING AND purpose = $2::STRING AND consumed_at IS NULL;

-- @name InsertMagicLink
-- @returns :exec
INSERT INTO magic_link_tokens (id, email, token_hash, purpose, expires_at, consumed_at, created_at)
VALUES ($1::UUID, $2::STRING, $3::BYTES, $4::STRING, $5::TIMESTAMPTZ,
  NULLIF($6::STRING, '')::TIMESTAMPTZ, $7::TIMESTAMPTZ);

-- @name ConsumeLegacyMagicLink
-- @returns :opt
UPDATE magic_link_tokens SET consumed_at = $3::TIMESTAMPTZ
WHERE token_hash = $1::BYTES AND purpose = $2::STRING
  AND consumed_at IS NULL AND expires_at > $3::TIMESTAMPTZ
RETURNING id::STRING AS id, email, token_hash, purpose, created_at, expires_at, consumed_at;

-- @name ConsumeDeliveryMagicLink
-- @returns :opt
UPDATE magic_link_mail_outbox
SET consumed_at = $3::TIMESTAMPTZ,
    status = CASE WHEN status = 'processing' THEN 'sent' ELSE status END,
    lease_until = CASE WHEN status = 'processing' THEN NULL ELSE lease_until END,
    claim_token = CASE WHEN status = 'processing' THEN NULL ELSE claim_token END,
    updated_at = $3::TIMESTAMPTZ
WHERE token_hash = $1::BYTES AND purpose = $2::STRING AND consumed_at IS NULL
  AND token_expires_at > $3::TIMESTAMPTZ AND status IN ('processing', 'sent')
RETURNING id::STRING AS id, recipient_email AS email, token_hash, purpose, created_at,
  token_expires_at AS expires_at, consumed_at;

-- @name GetMagicLinkState
-- @returns :opt
SELECT consumed_at, expires_at FROM magic_link_tokens
WHERE token_hash = $1::BYTES AND purpose = $2::STRING
UNION ALL
SELECT consumed_at, token_expires_at AS expires_at FROM magic_link_mail_outbox
WHERE token_hash = $1::BYTES AND purpose = $2::STRING AND status IN ('processing', 'sent')
LIMIT 1;

-- @name InvalidateLegacyMagicLink
-- @returns :exec_result
UPDATE magic_link_tokens SET consumed_at = COALESCE(consumed_at, $2::TIMESTAMPTZ)
WHERE token_hash = $1::BYTES;

-- @name InvalidateDeliveryMagicLink
-- @returns :exec_result
UPDATE magic_link_mail_outbox SET consumed_at = $2::TIMESTAMPTZ,
  status = CASE WHEN status = 'processing' THEN 'sent' ELSE status END,
  lease_until = CASE WHEN status = 'processing' THEN NULL ELSE lease_until END,
  claim_token = CASE WHEN status = 'processing' THEN NULL ELSE claim_token END,
  updated_at = $2::TIMESTAMPTZ
WHERE token_hash = $1::BYTES AND consumed_at IS NULL AND status IN ('processing', 'sent');

-- @name ProbeMagicLinkDeliverySchema
-- @returns :many
SELECT id, token_hash, token_expires_at, consumed_at, recipient_email,
  redirect_uri, purpose, status, attempts, available_at, lease_until,
  claim_token, created_at, updated_at, terminal_at, terminal_reason
FROM magic_link_mail_outbox WHERE false;

-- @name ConsumeSentDeliveryLinks
-- @returns :exec
UPDATE magic_link_mail_outbox SET consumed_at = $3::TIMESTAMPTZ, updated_at = $3::TIMESTAMPTZ
WHERE recipient_email = $1::STRING AND purpose = $2::STRING AND status = 'sent'
  AND consumed_at IS NULL;

-- @name SupersedePendingDeliveries
-- @returns :exec
UPDATE magic_link_mail_outbox
SET status = 'superseded', token_hash = NULL, token_expires_at = NULL,
    consumed_at = NULL, lease_until = NULL, claim_token = NULL,
    updated_at = $3::TIMESTAMPTZ, terminal_at = $3::TIMESTAMPTZ,
    terminal_reason = 'superseded'
WHERE recipient_email = $1::STRING AND purpose = $2::STRING
  AND status IN ('pending', 'processing');

-- @name InsertDeliveryIntent
-- @returns :exec
INSERT INTO magic_link_mail_outbox (
  id, recipient_email, redirect_uri, purpose, status, attempts,
  available_at, created_at, updated_at
) VALUES ($1::UUID, $2::STRING, $3::STRING, $4::STRING, 'pending', 0,
          $5::TIMESTAMPTZ, $6::TIMESTAMPTZ, $6::TIMESTAMPTZ);

-- @name RecoverConsumedDelivery
-- @returns :exec
UPDATE magic_link_mail_outbox SET status = 'sent', lease_until = NULL,
  claim_token = NULL, updated_at = $1::TIMESTAMPTZ
WHERE status = 'processing' AND lease_until <= $1::TIMESTAMPTZ
  AND consumed_at IS NOT NULL;

-- @name DeadExpiredDelivery
-- @returns :exec
UPDATE magic_link_mail_outbox SET status = 'dead', token_hash = NULL,
  token_expires_at = NULL, consumed_at = NULL, lease_until = NULL,
  claim_token = NULL, updated_at = $1::TIMESTAMPTZ, terminal_at = $1::TIMESTAMPTZ,
  terminal_reason = 'attempts_exhausted'
WHERE status = 'processing' AND lease_until <= $1::TIMESTAMPTZ
  AND attempts >= $2::INT4;

-- @name ClaimDeliveries
-- @returns :exec
UPDATE magic_link_mail_outbox SET status = 'processing', attempts = attempts + 1,
  lease_until = $2::TIMESTAMPTZ, claim_token = $3::UUID,
  token_hash = NULL, token_expires_at = NULL, consumed_at = NULL,
  updated_at = $1::TIMESTAMPTZ
WHERE id IN (
  SELECT id FROM magic_link_mail_outbox
  WHERE attempts < $4::INT4 AND (
    (status = 'pending' AND available_at <= $1::TIMESTAMPTZ)
    OR (status = 'processing' AND lease_until <= $1::TIMESTAMPTZ))
  ORDER BY available_at, created_at, id LIMIT $5::INT8 FOR UPDATE SKIP LOCKED
);

-- @name ListClaimedDeliveries
-- @returns :many
SELECT id::STRING AS id, recipient_email, redirect_uri, purpose, status,
  attempts, available_at, lease_until, COALESCE(claim_token::STRING, '') AS claim_token,
  created_at, updated_at, terminal_at, COALESCE(terminal_reason, '') AS terminal_reason
FROM magic_link_mail_outbox WHERE status = 'processing' AND claim_token = $1::UUID
ORDER BY available_at, created_at, id;

-- @name LockClaimedDelivery
-- @returns :opt
SELECT recipient_email, purpose FROM magic_link_mail_outbox
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing' FOR UPDATE;

-- @name ActivateDeliveryToken
-- @returns :exec_result
UPDATE magic_link_mail_outbox SET token_hash = $3::BYTES,
  token_expires_at = $4::TIMESTAMPTZ, consumed_at = NULL, updated_at = $5::TIMESTAMPTZ
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing';

-- @name CompleteDelivery
-- @returns :exec_result
UPDATE magic_link_mail_outbox SET status = 'sent', lease_until = NULL,
  claim_token = NULL, updated_at = $3::TIMESTAMPTZ
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'
  AND token_hash IS NOT NULL AND token_expires_at IS NOT NULL;

-- @name RetryDelivery
-- @returns :opt
UPDATE magic_link_mail_outbox
SET status = CASE WHEN attempts >= $5::INT4 THEN 'dead' ELSE 'pending' END,
  token_hash = NULL, token_expires_at = NULL, consumed_at = NULL,
  available_at = CASE WHEN attempts >= $5::INT4 THEN available_at ELSE $4::TIMESTAMPTZ END,
  lease_until = NULL, claim_token = NULL, updated_at = $3::TIMESTAMPTZ,
  terminal_at = CASE WHEN attempts >= $5::INT4 THEN $3::TIMESTAMPTZ ELSE NULL END,
  terminal_reason = CASE WHEN attempts >= $5::INT4 THEN 'attempts_exhausted' ELSE NULL END
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'
RETURNING status;
