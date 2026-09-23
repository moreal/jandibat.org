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

-- @name DeleteSubjectSyncJobs
-- @returns :exec
DELETE FROM provider_sync_jobs WHERE subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectActivityFacts
-- @returns :exec
DELETE FROM activity_facts WHERE subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectTimelineCache
-- @returns :exec
DELETE FROM timeline_cache WHERE subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectActivityRefresh
-- @returns :exec
DELETE FROM activity_refresh_cache WHERE subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectCustomProviders
-- @returns :exec
DELETE FROM custom_providers WHERE subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectProviderConnections
-- @returns :exec
DELETE FROM provider_connections WHERE subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectEnvironments
-- @returns :exec
DELETE FROM environments WHERE owner_subject_id = ANY($1::STRING[]);

-- @name DeleteSubjectRows
-- @returns :exec
DELETE FROM subjects WHERE id = ANY($1::STRING[]);

-- @name DeleteAccountPasskeys
-- @returns :exec
DELETE FROM user_passkeys WHERE user_id = $1::STRING;

-- @name DeleteAccountRow
-- @returns :exec_result
DELETE FROM users WHERE id = $1::STRING;

-- @name MarkDeletionPrimaryDeleted
-- @returns :exec_result
UPDATE deletion_requests SET
  status = 'verifying', last_completed_stage = 'primary_deleted',
  error_code = NULL, subject_ids = $2::STRING[], updated_at = $3::TIMESTAMPTZ,
  completed_at = NULL, backup_expiry_at = NULL, audit_event_id = NULL
WHERE request_id = $1::STRING;

-- @name MarkDeletionFailed
-- @returns :exec_result
UPDATE deletion_requests
SET status = 'failed', error_code = $2::STRING, updated_at = $3::TIMESTAMPTZ
WHERE request_id = $1::STRING AND status <> 'completed';

-- @name ReleaseFailedDeletionClaim
-- @returns :exec_result
UPDATE deletion_request_claims SET
  available_at = $2::TIMESTAMPTZ + INTERVAL '1 minute',
  lease_until = NULL, claim_token = NULL, updated_at = $2::TIMESTAMPTZ
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1::STRING)
  AND claim_token::STRING = $3::STRING;

-- @name MarkDeletionDeferredForLegalHold
-- @returns :exec_result
UPDATE deletion_requests
SET status = 'failed', error_code = 'legal_hold_active', updated_at = $2::TIMESTAMPTZ
WHERE request_id = $1::STRING AND status <> 'completed';

-- @name GetDeletionHoldReleaseAt
-- @returns :one
SELECT COALESCE(max(hold.expires_at), $3::TIMESTAMPTZ + INTERVAL '1 minute') AS available_at
FROM legal_holds AS hold
WHERE hold.expires_at > $3::TIMESTAMPTZ
      AND (
        (hold.target_type = $1::STRING AND hold.target_id = $2::STRING)
        OR ($1::STRING = 'account' AND hold.target_type = 'subject' AND EXISTS (
          SELECT 1 FROM subjects WHERE subjects.id = hold.target_id AND subjects.owner_user_id = $2::STRING
        ))
        OR ($1::STRING = 'subject' AND hold.target_type = 'account' AND EXISTS (
          SELECT 1 FROM subjects WHERE subjects.id = $2::STRING AND subjects.owner_user_id = hold.target_id
        ))
      );

-- @name ReleaseHeldDeletionClaim
-- @returns :exec_result
UPDATE deletion_request_claims SET
  attempts = GREATEST(attempts - 1, 0), available_at = $2::TIMESTAMPTZ,
  lease_until = NULL, claim_token = NULL, updated_at = $3::TIMESTAMPTZ
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1::STRING)
  AND claim_token::STRING = $4::STRING;

-- @name GetAccountEmailForDeletion
-- @returns :opt
SELECT primary_email FROM users WHERE id = $1::STRING FOR UPDATE;

-- @name MarkAccountDeletionPending
-- @returns :exec_result
UPDATE users SET status = 'deletion_pending', updated_at = $2::TIMESTAMPTZ
WHERE id = $1::STRING;

-- @name ListAccountSubjectsForDeletion
-- @returns :many
SELECT id FROM subjects WHERE owner_user_id = $1::STRING ORDER BY id;

-- @name ExtendDeletedIdentityHMACTombstone
-- @returns :exec_result
UPDATE deleted_identity_tombstones_v2
SET expires_at = GREATEST(expires_at, $2::TIMESTAMPTZ)
WHERE deletion_request_id = (SELECT id FROM deletion_requests WHERE request_id = $1::STRING);

-- @name InsertDeletedIdentityHMACTombstone
-- @returns :exec_result
INSERT INTO deleted_identity_tombstones_v2 (
  identity_key_id, identity_digest, deletion_request_id, created_at, expires_at
)
SELECT $2::STRING, $3::BYTES, id, $4::TIMESTAMPTZ, $5::TIMESTAMPTZ
FROM deletion_requests
WHERE request_id = $1::STRING
ON CONFLICT (identity_key_id, identity_digest) DO NOTHING;

-- @name DeleteAccountSessionsForDeletion
-- @returns :exec
DELETE FROM user_sessions WHERE user_id = $1::STRING;

-- @name DeleteAccountMagicLinksForDeletion
-- @returns :exec
DELETE FROM magic_link_tokens WHERE user_id = $1::STRING OR ($2::STRING <> '' AND email = $2::STRING);

-- @name DeleteAccountAuthChallengesForDeletion
-- @returns :exec
DELETE FROM auth_challenges
WHERE user_id = $1::STRING OR payload->>'SubjectID' = ANY($2::STRING[]);

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

-- @name ListDeletionInboxForPromotion
-- @returns :many
SELECT id::STRING AS id, request_id, target_type, target_id, requested_at
FROM deletion_request_inbox
WHERE status = 'requested'
ORDER BY requested_at, id
LIMIT $1::INT8
FOR UPDATE SKIP LOCKED;

-- @name AcknowledgeDuplicateDeletionInbox
-- @returns :exec
UPDATE deletion_request_inbox
SET status = 'promoted', promoted_at = $2::TIMESTAMPTZ
WHERE id = $1::UUID AND status = 'requested';

-- @name DeletePromotedDeletionInboxByID
-- @returns :exec_result
DELETE FROM deletion_request_inbox
WHERE id = $1::UUID AND status = 'requested';

-- @name BootstrapPendingDeletionClaims
-- @returns :exec
INSERT INTO deletion_request_claims (deletion_request_id, available_at, updated_at)
SELECT request.id, LEAST(request.requested_at, $1::TIMESTAMPTZ), $1::TIMESTAMPTZ
FROM deletion_requests AS request
WHERE request.status <> 'completed'
ON CONFLICT (deletion_request_id) DO NOTHING;

-- @name ListClaimableDeletionRequests
-- @returns :many
SELECT request.request_id AS request_id
FROM deletion_request_claims AS claim
JOIN deletion_requests AS request ON request.id = claim.deletion_request_id
WHERE request.status <> 'completed'
  AND claim.attempts < 100
  AND claim.available_at <= $1::TIMESTAMPTZ
  AND (claim.lease_until IS NULL OR claim.lease_until <= $1::TIMESTAMPTZ)
ORDER BY claim.available_at, claim.updated_at, claim.deletion_request_id
LIMIT $2::INT8
FOR UPDATE OF claim SKIP LOCKED;

-- @name LockUnclaimedDeletionRequest
-- @returns :opt
SELECT NOT EXISTS (
  SELECT 1 FROM deletion_request_claims AS claim
  WHERE claim.deletion_request_id = deletion_requests.id AND claim.claim_token IS NOT NULL
) AS unclaimed
FROM deletion_requests
WHERE request_id = $1::STRING
FOR UPDATE;

-- @name LockDeletionLease
-- @returns :opt
SELECT COALESCE(claim.claim_token::STRING, '') AS claim_token,
  claim.lease_until > current_timestamp AS lease_active
FROM deletion_request_claims AS claim
JOIN deletion_requests AS request ON request.id = claim.deletion_request_id
WHERE request.request_id = $1::STRING
FOR UPDATE OF claim;

-- @name HasActiveDeletionHold
-- @returns :one
SELECT EXISTS (
  SELECT 1 FROM legal_holds AS hold
  WHERE hold.expires_at > $3::TIMESTAMPTZ
    AND (
      (hold.target_type = $1::STRING AND hold.target_id = $2::STRING)
      OR ($1::STRING = 'account' AND hold.target_type = 'subject' AND EXISTS (
        SELECT 1 FROM subjects WHERE subjects.id = hold.target_id AND subjects.owner_user_id = $2::STRING
      ))
      OR ($1::STRING = 'subject' AND hold.target_type = 'account' AND EXISTS (
        SELECT 1 FROM subjects WHERE subjects.id = $2::STRING AND subjects.owner_user_id = hold.target_id
      ))
    )
) AS active;

-- @name GetSubjectForDeletion
-- @returns :opt
SELECT id FROM subjects WHERE id = $1::STRING;

-- @name EnqueueDeletionTokenRevocations
-- @returns :exec
INSERT INTO provider_token_revocation_jobs (
  connection_id, provider_id, token_ciphertext, token_key_id,
  status, attempts, available_at, created_at, updated_at
)
SELECT connection.id, COALESCE(connection.sync_cursor->>'provider_id', ''),
  connection.access_token_ciphertext, connection.access_token_key_id,
  'pending', 0, $2::TIMESTAMPTZ, $2::TIMESTAMPTZ, $2::TIMESTAMPTZ
FROM provider_connections AS connection
WHERE connection.subject_id = ANY($1::STRING[])
  AND connection.auth_method = 'oauth2'
  AND connection.access_token_ciphertext IS NOT NULL
  AND COALESCE(NULLIF(connection.sync_cursor->>'provider_id', ''), '') <> ''
ON CONFLICT (connection_id) DO NOTHING;

-- @name MarkDeletionCredentialsRevoked
-- @returns :exec_result
UPDATE deletion_requests SET
  status = 'deleting_primary', last_completed_stage = 'credentials_revoked',
  error_code = NULL, subject_ids = $2::STRING[], updated_at = $3::TIMESTAMPTZ,
  completed_at = NULL, backup_expiry_at = NULL, audit_event_id = NULL
WHERE request_id = $1::STRING;

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
