-- @name InsertMutationAuditOutcome
-- @returns :exec
INSERT INTO mutation_audit_outbox (
  id, audit_event_id, request_id, occurred_at, actor_type, actor_id, action,
  target_type, target_id, outcome, metadata, status, attempts, available_at,
  created_at, updated_at
) VALUES (
  $1::UUID, $2::UUID, $3::STRING, $4::TIMESTAMPTZ, $5::STRING,
  NULLIF($6::STRING, ''), $7::STRING, $8::STRING, NULLIF($9::STRING, ''),
  $10::STRING, $11::JSONB, 'pending', 0,
  $4::TIMESTAMPTZ, $4::TIMESTAMPTZ, $4::TIMESTAMPTZ
);

-- @name GetDeletionInboxForRequest
-- @returns :opt
SELECT id::STRING AS id, request_id, target_type, target_id, requested_at
FROM deletion_request_inbox
WHERE request_id = $1::STRING OR (target_type = $2::STRING AND target_id = $3::STRING)
ORDER BY (request_id = $1::STRING) DESC, requested_at
LIMIT 1;

-- @name GetDeletionRequestByID
-- @returns :opt
SELECT deletion_requests.id::STRING AS id, request_id, target_type, target_id, status,
  COALESCE(last_completed_stage, '') AS last_completed_stage,
  COALESCE(error_code, '') AS error_code,
  COALESCE(array_to_json(subject_ids), '[]'::JSON) AS subject_ids,
  requested_at, updated_at, completed_at, backup_expiry_at,
  COALESCE(audit_event_id::STRING, '') AS audit_event_id,
  COALESCE((SELECT claim.attempts FROM deletion_request_claims AS claim
    WHERE claim.deletion_request_id = deletion_requests.id), 0) AS attempts,
  COALESCE((SELECT claim.available_at FROM deletion_request_claims AS claim
    WHERE claim.deletion_request_id = deletion_requests.id), requested_at) AS available_at,
  (SELECT claim.lease_until FROM deletion_request_claims AS claim
    WHERE claim.deletion_request_id = deletion_requests.id) AS lease_until,
  COALESCE((SELECT claim.claim_token::STRING FROM deletion_request_claims AS claim
    WHERE claim.deletion_request_id = deletion_requests.id), '') AS claim_token
FROM deletion_requests
WHERE request_id = $1::STRING;

-- @name ListEncryptedSecretsForReencryption
-- @returns :many
WITH encrypted_secrets (kind, id, key_id, ciphertext) AS (
  SELECT 'connection_access_token', id::STRING,
    COALESCE(access_token_key_id, ''), access_token_ciphertext
  FROM provider_connections
  WHERE access_token_ciphertext IS NOT NULL
  UNION ALL
  SELECT 'connection_refresh_token', id::STRING,
    COALESCE(refresh_token_key_id, ''), refresh_token_ciphertext
  FROM provider_connections
  WHERE refresh_token_ciphertext IS NOT NULL
  UNION ALL
  SELECT 'oauth_revocation_token', id::STRING,
    COALESCE(token_key_id, ''), token_ciphertext
  FROM provider_token_revocation_jobs
  WHERE token_ciphertext IS NOT NULL
)
SELECT kind, id, key_id, ciphertext
FROM encrypted_secrets
WHERE (kind > $1::STRING OR (kind = $1::STRING AND id > $2::STRING))
ORDER BY kind, id
LIMIT $3::INT8;

-- @name ReplaceConnectionAccessToken
-- @returns :exec_result
UPDATE provider_connections
SET access_token_ciphertext = $3::BYTES, access_token_key_id = $4::STRING, updated_at = now()
WHERE id = $1::UUID AND access_token_ciphertext = $2::BYTES
  AND COALESCE(access_token_key_id, '') = $5::STRING;

-- @name ReplaceConnectionRefreshToken
-- @returns :exec_result
UPDATE provider_connections
SET refresh_token_ciphertext = $3::BYTES, refresh_token_key_id = $4::STRING, updated_at = now()
WHERE id = $1::UUID AND refresh_token_ciphertext = $2::BYTES
  AND COALESCE(refresh_token_key_id, '') = $5::STRING;

-- @name ReplaceOAuthRevocationToken
-- @returns :exec_result
UPDATE provider_token_revocation_jobs
SET token_ciphertext = $3::BYTES, token_key_id = $4::STRING, updated_at = now()
WHERE id = $1::UUID AND token_ciphertext = $2::BYTES
  AND COALESCE(token_key_id, '') = $5::STRING;

-- @name CountDeletionResiduals
-- @returns :one
SELECT
  (SELECT count(*) FROM subjects WHERE owner_user_id = $1::STRING) AS subjects,
  (SELECT count(*) FROM user_passkeys WHERE user_id = $1::STRING) AS passkeys,
  (SELECT count(*) FROM user_sessions WHERE user_id = $1::STRING) AS sessions,
  (SELECT count(*) FROM magic_link_tokens WHERE user_id = $1::STRING) AS magic_links,
  (SELECT count(*) FROM auth_challenges WHERE user_id = $1::STRING OR payload->>'SubjectID' = ANY($2::STRING[])) AS auth_challenges,
  (SELECT count(*) FROM provider_connections WHERE subject_id = ANY($2::STRING[])) AS provider_connections,
  (SELECT count(*) FROM provider_connection_private_consents WHERE connection_id IN (
    SELECT id FROM provider_connections WHERE subject_id = ANY($2::STRING[])
  )) AS private_consents,
  (SELECT count(*) FROM custom_providers WHERE subject_id = ANY($2::STRING[])) AS custom_providers,
  (SELECT count(*) FROM activity_facts WHERE subject_id = ANY($2::STRING[])) AS activity_facts,
  (SELECT count(*) FROM timeline_cache WHERE subject_id = ANY($2::STRING[])) AS timeline_cache,
  (SELECT count(*) FROM activity_refresh_cache WHERE subject_id = ANY($2::STRING[])) AS activity_refresh,
  (SELECT count(*) FROM provider_sync_jobs WHERE subject_id = ANY($2::STRING[])) AS provider_sync_jobs;

-- @name InsertDeletionInboxIfAbsent
-- @returns :exec
INSERT INTO deletion_request_inbox (request_id, target_type, target_id, requested_at)
VALUES ($1::STRING, $2::STRING, $3::STRING, $4::TIMESTAMPTZ)
ON CONFLICT DO NOTHING;

-- @name DeadExpiredMutationAudits
-- @returns :exec
UPDATE mutation_audit_outbox
SET status = 'dead', lease_until = NULL, claim_token = NULL,
  updated_at = $1::TIMESTAMPTZ, terminal_at = $1::TIMESTAMPTZ,
  terminal_reason = 'attempts_exhausted'
WHERE status = 'processing' AND lease_until <= $1::TIMESTAMPTZ
  AND attempts >= $2::INT8;

-- @name ClaimMutationAuditBatch
-- @returns :exec
UPDATE mutation_audit_outbox
SET status = 'processing', attempts = attempts + 1,
  lease_until = $2::TIMESTAMPTZ, claim_token = $3::UUID,
  updated_at = $1::TIMESTAMPTZ
WHERE id IN (
  SELECT id FROM mutation_audit_outbox
  WHERE attempts < $4::INT8
    AND ((status = 'pending' AND available_at <= $1::TIMESTAMPTZ)
      OR (status = 'processing' AND lease_until <= $1::TIMESTAMPTZ))
  ORDER BY available_at, created_at, id
  LIMIT $5::INT8 FOR UPDATE SKIP LOCKED
);

-- @name ListClaimedMutationAudits
-- @returns :many
SELECT id::STRING AS id, audit_event_id::STRING AS audit_event_id,
  request_id, occurred_at, actor_type, COALESCE(actor_id, '') AS actor_id,
  action, target_type, COALESCE(target_id, '') AS target_id, outcome,
  metadata, claim_token::STRING AS claim_token, attempts
FROM mutation_audit_outbox
WHERE status = 'processing' AND claim_token = $1::UUID
ORDER BY available_at, created_at, id;

-- @name GetMutationAuditForDelivery
-- @returns :opt
SELECT audit_event_id::STRING AS audit_event_id, occurred_at, actor_type,
  COALESCE(actor_id, '') AS actor_id, action, target_type,
  COALESCE(target_id, '') AS target_id, outcome, request_id, metadata
FROM mutation_audit_outbox
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'
FOR UPDATE;

-- @name InsertAuditEvent
-- @returns :exec
INSERT INTO audit_events (
  id, occurred_at, actor_type, actor_id, action, target_type, target_id,
  outcome, request_id, metadata
) VALUES ($1::UUID, $2::TIMESTAMPTZ, $3::STRING, NULLIF($4::STRING, ''),
  $5::STRING, $6::STRING, NULLIF($7::STRING, ''), $8::STRING, $9::STRING, $10::JSONB);

-- @name MarkMutationAuditDelivered
-- @returns :exec_result
UPDATE mutation_audit_outbox
SET status = 'delivered', lease_until = NULL, claim_token = NULL,
  delivered_at = $3::TIMESTAMPTZ, updated_at = $3::TIMESTAMPTZ
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing';

-- @name RetryMutationAuditClaim
-- @returns :opt
UPDATE mutation_audit_outbox
SET status = CASE WHEN attempts >= $5::INT8 THEN 'dead' ELSE 'pending' END,
  available_at = CASE WHEN attempts >= $5::INT8 THEN available_at ELSE $4::TIMESTAMPTZ END,
  lease_until = NULL, claim_token = NULL, updated_at = $3::TIMESTAMPTZ,
  terminal_at = CASE WHEN attempts >= $5::INT8 THEN $3::TIMESTAMPTZ ELSE NULL END,
  terminal_reason = CASE WHEN attempts >= $5::INT8 THEN $6::STRING ELSE NULL END
WHERE id = $1::UUID AND claim_token = $2::UUID AND status = 'processing'
RETURNING status;

-- @name GetActiveDeletionID
-- @returns :opt
SELECT id::STRING AS id FROM deletion_requests
WHERE request_id = $1::STRING AND status <> 'completed';

-- @name LockDeletionInboxForClaim
-- @returns :opt
SELECT target_type, target_id, requested_at
FROM deletion_request_inbox
WHERE request_id = $1::STRING AND status = 'requested'
FOR UPDATE;

-- @name PromoteDeletionInboxRequest
-- @returns :exec
INSERT INTO deletion_requests (
  request_id, target_type, target_id, status, last_completed_stage,
  subject_ids, requested_at, updated_at
) VALUES ($1::STRING, $2::STRING, $3::STRING, 'requested', 'requested',
  ARRAY[]::STRING[], $4::TIMESTAMPTZ, $5::TIMESTAMPTZ)
ON CONFLICT DO NOTHING;

-- @name GetDeletionIDForExactTarget
-- @returns :opt
SELECT id::STRING AS id FROM deletion_requests
WHERE request_id = $1::STRING AND target_type = $2::STRING AND target_id = $3::STRING;

-- @name DeletePromotedDeletionInbox
-- @returns :exec
DELETE FROM deletion_request_inbox WHERE request_id = $1::STRING;

-- @name BootstrapDeletionClaim
-- @returns :exec
INSERT INTO deletion_request_claims (deletion_request_id, available_at, updated_at)
VALUES ($1::UUID, $2::TIMESTAMPTZ, $2::TIMESTAMPTZ)
ON CONFLICT (deletion_request_id) DO NOTHING;

-- @name AcquireDeletionClaim
-- @returns :exec_result
UPDATE deletion_request_claims SET
  claim_token = $2::UUID, lease_until = $3::TIMESTAMPTZ,
  attempts = attempts + 1, updated_at = $1::TIMESTAMPTZ
WHERE deletion_request_id = $4::UUID
  AND attempts < 100 AND available_at <= $1::TIMESTAMPTZ
  AND (lease_until IS NULL OR lease_until <= $1::TIMESTAMPTZ);

-- @name GetMaintenanceCheckpoint
-- @returns :opt
SELECT operation, scope, payload, updated_at
FROM maintenance_checkpoints
WHERE operation = $1::STRING AND scope = $2::STRING;

-- @name SaveMaintenanceCheckpoint
-- @returns :exec
INSERT INTO maintenance_checkpoints (operation, scope, payload, updated_at)
VALUES ($1::STRING, $2::STRING, $3::JSONB, $4::TIMESTAMPTZ)
ON CONFLICT (operation, scope) DO UPDATE SET
  payload = excluded.payload,
  updated_at = excluded.updated_at;

-- @name DeleteMaintenanceCheckpoint
-- @returns :exec
DELETE FROM maintenance_checkpoints
WHERE operation = $1::STRING AND scope = $2::STRING;

-- @name InsertDeletionRequestIfAbsent
-- @returns :exec
INSERT INTO deletion_requests (
  request_id, target_type, target_id, status, last_completed_stage,
  subject_ids, requested_at, updated_at
) VALUES ($1::STRING, $2::STRING, $3::STRING, 'requested', 'requested',
  ARRAY[]::STRING[], $4::TIMESTAMPTZ, $4::TIMESTAMPTZ)
ON CONFLICT DO NOTHING;

-- @name GetDeletionRequestForRequester
-- @returns :opt
SELECT id::STRING AS id, request_id, target_type, target_id, status,
  COALESCE(last_completed_stage, '') AS last_completed_stage,
  COALESCE(error_code, '') AS error_code,
  COALESCE(array_to_json(subject_ids), '[]'::JSON) AS subject_ids,
  requested_at, updated_at, completed_at, backup_expiry_at,
  COALESCE(audit_event_id::STRING, '') AS audit_event_id
FROM deletion_requests
WHERE request_id = $1::STRING
  OR (target_type = $2::STRING AND target_id = $3::STRING AND status <> 'completed')
ORDER BY (request_id = $1::STRING) DESC, requested_at
LIMIT 1;
