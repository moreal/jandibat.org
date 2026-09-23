#!/bin/sh
set -eu

database=${BASELINE_TEST_DATABASE:-jandibat_baseline_test}
case "$database" in
  jandibat_baseline_test*) ;;
  *) echo "BASELINE_TEST_DATABASE must begin with jandibat_baseline_test" >&2; exit 2 ;;
esac
case "$database" in
  *[!A-Za-z0-9_]*|'') echo "unsafe baseline test database name" >&2; exit 2 ;;
esac

sql() {
  docker compose exec -T cockroach cockroach sql --insecure \
    --host=127.0.0.1:26258 --database="$database" --format=tsv --set=errexit=true "$@"
}

COCKROACH_DATABASE="$database" sh scripts/db-migrate.sh >/dev/null

versions=$(sql --execute="SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations" | tail -n 1 | tr -d '\r')
if [ "$versions" != 0001_baseline.sql,0002_ingest_reservation_token.sql ]; then
  echo "expected baseline and reservation-token migrations; got: $versions" >&2
  exit 1
fi

empty_catalog=$(mktemp "${TMPDIR:-/tmp}/jandibat-baseline-empty.XXXXXX")
populated_catalog=$(mktemp "${TMPDIR:-/tmp}/jandibat-baseline-populated.XXXXXX")
trap 'rm -f "$empty_catalog" "$populated_catalog"' EXIT HUP INT TERM
sql --file=/workspace/scripts/baseline-catalog-fingerprint.sql | tail -n +2 | tr -d '\r' >"$empty_catalog"

sql --execute="
INSERT INTO users (id, primary_email)
VALUES ('baseline-schema-fixture', 'baseline-schema-fixture.invalid')
ON CONFLICT (id) DO NOTHING" >/dev/null
second_run=$(COCKROACH_DATABASE="$database" sh scripts/db-migrate.sh)
case "$second_run" in
  *"already applied: 0001_baseline.sql"*) ;;
  *) echo "baseline migration was not idempotent" >&2; exit 1 ;;
esac

sql --file=/workspace/scripts/baseline-catalog-fingerprint.sql | tail -n +2 | tr -d '\r' >"$populated_catalog"
diff -u scripts/baseline-catalog.expected.tsv "$empty_catalog"
diff -u scripts/baseline-catalog.expected.tsv "$populated_catalog"
rows=$(sql --execute="SELECT count(*) FROM users WHERE id = 'baseline-schema-fixture'" | tail -n 1 | tr -d '\r')
if [ "$rows" != 1 ]; then
  echo "second migration run did not preserve existing rows" >&2
  exit 1
fi
echo "baseline count, idempotency, populated-row preservation, and catalog fingerprint passed"
