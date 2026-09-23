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
