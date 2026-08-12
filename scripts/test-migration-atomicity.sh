#!/bin/sh
set -eu

database=jandibat_migration_atomicity_test
compose=${COMPOSE_BIN:-docker compose}
case "$database" in *[!A-Za-z0-9_]*|'') exit 2 ;; esac

$compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 --set=errexit=true \
	--execute="DROP DATABASE IF EXISTS $database CASCADE; CREATE DATABASE $database" >/dev/null
cleanup() {
	$compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
		--execute="DROP DATABASE IF EXISTS $database CASCADE" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

if $compose exec -T \
	-e MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$database?sslmode=disable" \
	-e COCKROACH_DATABASE="$database" \
	-e MIGRATIONS_DIR=/workspace/scripts/fixtures/migrations \
	cockroach sh /workspace/scripts/db-migrate-url.sh >/dev/null 2>&1; then
	echo "deliberately failing migration unexpectedly succeeded" >&2
	exit 1
fi

result=$($compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
	--database="$database" --format=tsv --set=errexit=true --execute="
SELECT
  (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'migration_atomicity_probe'),
  (SELECT count(*) FROM schema_migrations WHERE version = '9999_atomicity_failure.sql')" | tail -n 1 | tr -d '\r')
if ! printf '%s\n' "$result" | awk -F '\t' 'NF == 2 && $1 == 0 && $2 == 0 { ok = 1 } END { exit !ok }'; then
	echo "failed migration left DDL or ledger state: $result" >&2
	exit 1
fi

$compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
	--database="$database" --set=errexit=true --execute="
CREATE TABLE migration_schema_lock_probe (
  id INT8 PRIMARY KEY,
  outcome STRING NOT NULL,
  CONSTRAINT migration_schema_lock_probe_outcome_chk CHECK (outcome = 'succeeded')
) WITH (schema_locked = true)" >/dev/null
if $compose exec -T \
	-e MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$database?sslmode=disable" \
	-e COCKROACH_DATABASE="$database" \
	-e MIGRATIONS_DIR=/workspace/scripts/fixtures/schema-unlock-migrations \
	cockroach sh /workspace/scripts/db-migrate-url.sh >/dev/null 2>&1; then
	echo "deliberately failing schema-unlock migration unexpectedly succeeded" >&2
	exit 1
fi
result=$($compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
	--database="$database" --format=tsv --set=errexit=true --execute="
SELECT
  (SELECT count(*) FROM [SHOW CONSTRAINTS FROM migration_schema_lock_probe]
    WHERE constraint_name = 'migration_schema_lock_probe_outcome_chk'),
  (SELECT count(*) FROM [SHOW CONSTRAINTS FROM migration_schema_lock_probe]
    WHERE constraint_name = 'migration_schema_lock_probe_outcome_v2_chk'),
  (SELECT count(*) FROM schema_migrations WHERE version = '9999_schema_unlock_failure.sql'),
  (SELECT count(*) FROM [SHOW CREATE TABLE migration_schema_lock_probe]
    WHERE create_statement LIKE '%schema_locked = true%')" | tail -n 1 | tr -d '\r')
if ! printf '%s\n' "$result" | awk -F '\t' 'NF == 4 && $1 == 1 && $2 == 0 && $3 == 0 && $4 == 1 { ok = 1 } END { exit !ok }'; then
	echo "failed schema-unlock migration left constraint, ledger, or lock drift: $result" >&2
	exit 1
fi

# Model the only failure a shell trap cannot repair: an uncatchable process
# termination after the standalone unlock. The independent verifier must fail
# closed, and its repair mode must restore the allowlisted lock from a fresh
# process without changing schema or ledger state.
$compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
	--database="$database" --set=errexit=true \
	--execute="ALTER TABLE migration_schema_lock_probe SET (schema_locked = false)" >/dev/null
if $compose exec -T \
	-e MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$database?sslmode=disable" \
	-e COCKROACH_DATABASE="$database" \
	-e MIGRATIONS_DIR=/workspace/scripts/fixtures/schema-unlock-migrations \
	cockroach sh /workspace/scripts/db-verify-schema-locks.sh >/dev/null 2>&1; then
	echo "schema-lock verifier accepted an unlocked table" >&2
	exit 1
fi
$compose exec -T \
	-e MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$database?sslmode=disable" \
	-e COCKROACH_DATABASE="$database" \
	-e MIGRATIONS_DIR=/workspace/scripts/fixtures/schema-unlock-migrations \
	cockroach sh /workspace/scripts/db-verify-schema-locks.sh --repair >/dev/null
result=$($compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
	--database="$database" --format=tsv --set=errexit=true \
	--execute="SELECT count(*) FROM [SHOW CREATE TABLE migration_schema_lock_probe] WHERE create_statement LIKE '%schema_locked = true%'" | tail -n 1 | tr -d '\r')
if [ "$result" != "1" ]; then
	echo "independent schema-lock repair did not relock the table" >&2
	exit 1
fi
echo "migration DDL/ledger rollback and independent schema-lock recovery passed"
