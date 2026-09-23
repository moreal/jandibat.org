#!/bin/sh
set -eu

database=${BASELINE_LEGACY_DATABASE:-jandibat_baseline_test_legacy}
case "$database" in
  jandibat_baseline_test_legacy*) ;;
  *) echo "BASELINE_LEGACY_DATABASE must begin with jandibat_baseline_test_legacy" >&2; exit 2 ;;
esac
case "$database" in
  *[!A-Za-z0-9_]*|'') echo "unsafe legacy test database name" >&2; exit 2 ;;
esac

sql() {
  docker compose exec -T cockroach cockroach sql --insecure \
    --host=127.0.0.1:26258 --database="$database" --format=tsv --set=errexit=true "$@"
}
prepare_legacy() {
  docker compose exec -T cockroach cockroach sql --insecure --host=127.0.0.1:26258 \
    --set=errexit=true --execute="CREATE DATABASE IF NOT EXISTS $database" >/dev/null
  sql --execute="
CREATE TABLE IF NOT EXISTS schema_migrations (
  version STRING PRIMARY KEY,
  checksum STRING NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations (version, checksum)
VALUES ('0001_init.sql', 'legacy-test-fixture')
ON CONFLICT (version) DO NOTHING" >/dev/null
}
assert_untouched() {
  result=$(sql --execute="
SELECT count(*) FROM information_schema.tables
WHERE table_schema = 'public' AND table_name <> 'schema_migrations'" | tail -n 1 | tr -d '\r')
  if [ "$result" != 0 ]; then
    echo "baseline migration touched a legacy database" >&2
    exit 1
  fi
}

prepare_legacy
if COCKROACH_DATABASE="$database" sh scripts/db-migrate.sh >/dev/null 2>&1; then
  echo "legacy history was accepted by the baseline migrator" >&2
  exit 1
fi
assert_untouched

database="${database}_url"
prepare_legacy
if docker compose exec -T \
  -e MIGRATION_DATABASE_URL="postgresql://root@127.0.0.1:26258/$database?sslmode=disable" \
  -e COCKROACH_DATABASE="$database" \
  -e MIGRATIONS_DIR=/workspace/db/migrations \
  cockroach sh /workspace/scripts/db-migrate-url.sh >/dev/null 2>&1; then
  echo "legacy history was accepted by the URL baseline migrator" >&2
  exit 1
fi
assert_untouched
echo "legacy migration history rejected without schema changes"
