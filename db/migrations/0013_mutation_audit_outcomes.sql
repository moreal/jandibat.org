-- Some security-sensitive requests intentionally commit state even when the
-- HTTP response is denied or failed (for example, consuming a one-time
-- ceremony after a verification failure). Preserve that state transition and
-- its final outcome in the same transaction instead of reviving the ceremony.
-- jandibat:schema-unlock mutation_audit_outbox
ALTER TABLE mutation_audit_outbox
  DROP CONSTRAINT mutation_audit_outbox_outcome_chk;

ALTER TABLE mutation_audit_outbox
  ADD CONSTRAINT mutation_audit_outbox_outcome_v2_chk
  CHECK (outcome IN ('succeeded', 'failed', 'denied'));
