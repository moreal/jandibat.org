-- Deliberately fails after transactional DDL. Used only by
-- scripts/test-migration-atomicity.sh; never include this directory in a
-- runtime migration path.
CREATE TABLE migration_atomicity_probe (id INT8 PRIMARY KEY);
SELECT 1 / 0;
