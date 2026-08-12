-- Deliberately fails after replacing a constraint on a schema-locked table.
-- The runner must roll back DDL+ledger and restore the table lock.
-- jandibat:schema-unlock migration_schema_lock_probe
ALTER TABLE migration_schema_lock_probe
  DROP CONSTRAINT migration_schema_lock_probe_outcome_chk;
ALTER TABLE migration_schema_lock_probe
  ADD CONSTRAINT migration_schema_lock_probe_outcome_v2_chk
  CHECK (outcome IN ('succeeded', 'failed'));
SELECT 1 / 0;
