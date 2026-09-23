#!/bin/sh
set -eu

# Use a new, isolated database. Never drop an existing database or PVC.
database="jandibat_followup_$(date +%s)_$$"

docker compose exec -T -e COCKROACH_DATABASE="$database" cockroach sh -s <<'CONTAINER_SCRIPT'
set -eu

migrations_dir=$(mktemp -d /tmp/jandibat-followup.XXXXXX)
cleanup() {
	rm -f "$migrations_dir/0001_baseline.sql" "$migrations_dir/0002_followup.sql" "$migrations_dir/notes.txt"
	rmdir "$migrations_dir"
}
trap cleanup EXIT

cp /workspace/db/migrations/0001_baseline.sql "$migrations_dir/0001_baseline.sql"
printf '%s\n' 'CREATE TABLE migration_followup_probe (id STRING PRIMARY KEY);' >"$migrations_dir/0002_followup.sql"

cockroach sql --insecure --host=127.0.0.1:26258 --set=errexit=true \
	--execute="CREATE DATABASE $COCKROACH_DATABASE" >/dev/null

export MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$COCKROACH_DATABASE?sslmode=disable"
export MIGRATIONS_DIR="$migrations_dir"
sh /workspace/scripts/db-migrate-url.sh >/dev/null
sh /workspace/scripts/db-migrate-url.sh >/dev/null

count=$(cockroach sql --url="$MIGRATION_DATABASE_URL" --format=tsv \
	--execute='SELECT count(*) FROM schema_migrations' | tail -n 1 | tr -d '\r')
if [ "$count" != 2 ]; then
	echo "follow-up migration history has $count entries, expected 2" >&2
	exit 1
fi

cockroach sql --url="$MIGRATION_DATABASE_URL" --set=errexit=true \
	--execute="INSERT INTO schema_migrations (version, checksum) VALUES ('0001_init.sql', 'legacy-test-fixture')" >/dev/null
if sh /workspace/scripts/db-migrate-url.sh >/dev/null 2>&1; then
	echo 'unmanaged migration history was accepted after the baseline' >&2
	exit 1
fi
cockroach sql --url="$MIGRATION_DATABASE_URL" --set=errexit=true \
	--execute="DELETE FROM schema_migrations WHERE version = '0001_init.sql'" >/dev/null
printf '%s\n' 'not a migration' >"$migrations_dir/notes.txt"
cockroach sql --url="$MIGRATION_DATABASE_URL" --set=errexit=true \
	--execute="INSERT INTO schema_migrations (version, checksum) VALUES ('notes.txt', 'not-a-migration')" >/dev/null
if sh /workspace/scripts/db-migrate-url.sh >/dev/null 2>&1; then
	echo 'non-SQL migration history was accepted after the baseline' >&2
	exit 1
fi
echo 'URL migrator reran baseline and follow-up, then rejected unmanaged history'
CONTAINER_SCRIPT

database="${database}_host"
migrations_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-followup.XXXXXX")
cleanup() {
	rm -f "$migrations_dir/0001_baseline.sql" "$migrations_dir/0002_followup.sql" "$migrations_dir/notes.txt"
	rmdir "$migrations_dir"
}
trap cleanup EXIT

cp db/migrations/0001_baseline.sql "$migrations_dir/0001_baseline.sql"
printf '%s\n' 'CREATE TABLE migration_followup_probe (id STRING PRIMARY KEY);' >"$migrations_dir/0002_followup.sql"

COCKROACH_DATABASE="$database" MIGRATIONS_DIR="$migrations_dir" sh scripts/db-migrate.sh >/dev/null
COCKROACH_DATABASE="$database" MIGRATIONS_DIR="$migrations_dir" sh scripts/db-migrate.sh >/dev/null

count=$(docker compose exec -T cockroach cockroach sql --insecure \
	--host=127.0.0.1:26258 --database="$database" --format=tsv \
	--execute='SELECT count(*) FROM schema_migrations' | tail -n 1 | tr -d '\r')
if [ "$count" != 2 ]; then
	echo "host follow-up migration history has $count entries, expected 2" >&2
	exit 1
fi
printf '%s\n' 'not a migration' >"$migrations_dir/notes.txt"
docker compose exec -T cockroach cockroach sql --insecure \
	--host=127.0.0.1:26258 --database="$database" --set=errexit=true \
	--execute="INSERT INTO schema_migrations (version, checksum) VALUES ('notes.txt', 'not-a-migration')" >/dev/null
if COCKROACH_DATABASE="$database" MIGRATIONS_DIR="$migrations_dir" sh scripts/db-migrate.sh >/dev/null 2>&1; then
	echo 'host migrator accepted non-SQL migration history' >&2
	exit 1
fi
echo 'host migrator reran baseline and follow-up'
