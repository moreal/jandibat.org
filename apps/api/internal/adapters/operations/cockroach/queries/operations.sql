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

-- @name InsertDeliveredMutationAudit
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
