#!/bin/sh
set -eu

# Exercise the URL migrator against an old baseline row without removing any DB.
database="jandibat_reservation_test_$(date +%s)_$$"

docker compose exec -T -e COCKROACH_DATABASE="$database" cockroach sh -s <<'CONTAINER_SCRIPT'
set -eu

migrations_dir=$(mktemp -d /tmp/jandibat-reservation.XXXXXX)
cleanup() {
	rm -f "$migrations_dir/0001_baseline.sql"
	rmdir "$migrations_dir"
}
trap cleanup EXIT

cp /workspace/db/migrations/0001_baseline.sql "$migrations_dir/0001_baseline.sql"
cockroach sql --insecure --host=127.0.0.1:26258 --set=errexit=true \
	--execute="CREATE DATABASE $COCKROACH_DATABASE" >/dev/null
export MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$COCKROACH_DATABASE?sslmode=disable"

MIGRATIONS_DIR="$migrations_dir" sh /workspace/scripts/db-migrate-url.sh >/dev/null
cockroach sql --url="$MIGRATION_DATABASE_URL" --set=errexit=true --execute="
INSERT INTO users (id, primary_email) VALUES ('migration-user', 'migration@example.invalid');
INSERT INTO subjects (id, owner_user_id, handle, timezone)
  VALUES ('migration-subject', 'migration-user', 'migration-subject', 'UTC');
INSERT INTO environments (id, key, name, scope, owner_subject_id)
  VALUES ('migration-environment', 'migration-environment', 'Migration', 'subject', 'migration-subject');
INSERT INTO custom_providers (id, owner_user_id, subject_id, environment_id, slug, name)
  VALUES ('00000000-0000-4000-8000-000000000001', 'migration-user',
    'migration-subject', 'migration-environment', 'migration', 'Migration');
INSERT INTO ingest_idempotency_keys
  (custom_provider_id, key_hash, request_hash, response_status, response_body, created_at, expires_at)
  VALUES ('00000000-0000-4000-8000-000000000001', b'key', b'request', 0,
    '{}'::JSONB, now(), now() + INTERVAL '1 hour');
" >/dev/null

MIGRATIONS_DIR=/workspace/db/migrations sh /workspace/scripts/db-migrate-url.sh >/dev/null
MIGRATIONS_DIR=/workspace/db/migrations sh /workspace/scripts/db-migrate-url.sh >/dev/null

result=$(cockroach sql --url="$MIGRATION_DATABASE_URL" --format=tsv --execute="
SELECT
  (SELECT count(*) FROM schema_migrations) AS versions,
  (SELECT count(*) FROM ingest_idempotency_keys WHERE reservation_token IS NOT NULL) AS backfilled,
  (SELECT count(*) FROM [SHOW CREATE ALL TABLES]
   WHERE create_statement LIKE '%CREATE TABLE public.ingest_idempotency_keys %'
     AND create_statement LIKE '%schema_locked = true%') AS relocked
" | tail -n 1 | tr -d '\r')
if [ "$result" != "$(printf '2\t1\t1')" ]; then
	echo "reservation migration validation failed: $result" >&2
	exit 1
fi
echo 'reservation token backfill, URL migration idempotency, and schema relock passed'
CONTAINER_SCRIPT
