#!/bin/sh
set -eu

invalid() { echo 'invalid database bootstrap input' >&2; exit 2; }
for value in "${COCKROACH_ROOT_URL:-}" "${MIGRATION_DATABASE_URL:-}" "${API_DATABASE_URL:-}" "${WORKER_DATABASE_URL:-}" "${MAINTENANCE_DATABASE_URL:-}"; do
 [ -n "$value" ] || invalid
done
validate_password() {
 password=$1
 case "$password" in ''|*[!A-Za-z0-9_-]*) invalid;; esac
 [ "${#password}" -ge 43 ] || invalid
 [ "$password" != REPLACE_ME ] || invalid
}
for password in "${JANDIBAT_MIGRATOR_PASSWORD:-}" "${JANDIBAT_API_PASSWORD:-}" "${JANDIBAT_WORKER_PASSWORD:-}" "${JANDIBAT_MAINTENANCE_PASSWORD:-}"; do
 validate_password "$password"
done
[ "$JANDIBAT_MIGRATOR_PASSWORD" != "$JANDIBAT_API_PASSWORD" ] || invalid
[ "$JANDIBAT_MIGRATOR_PASSWORD" != "$JANDIBAT_WORKER_PASSWORD" ] || invalid
[ "$JANDIBAT_MIGRATOR_PASSWORD" != "$JANDIBAT_MAINTENANCE_PASSWORD" ] || invalid
[ "$JANDIBAT_API_PASSWORD" != "$JANDIBAT_WORKER_PASSWORD" ] || invalid
[ "$JANDIBAT_API_PASSWORD" != "$JANDIBAT_MAINTENANCE_PASSWORD" ] || invalid
[ "$JANDIBAT_WORKER_PASSWORD" != "$JANDIBAT_MAINTENANCE_PASSWORD" ] || invalid

sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d) || { echo 'bootstrap capture unavailable' >&2; exit 1; }
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
sql() {
 url=$1
 statement=$2
 expected_user=${3:-}
 if ! printf '%s\n' "$statement" | COCKROACH_URL="$url" "$sql_bin" sql --set=errexit=true --format=tsv >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then
  echo 'database bootstrap SQL operation failed' >&2
  exit 1
 fi
 if [ -n "$expected_user" ] && [ "$(tail -n 1 "$capture_dir/stdout" | tr -d '\r')" != "$expected_user" ]; then
  echo 'database bootstrap role identity mismatch' >&2
  exit 1
 fi
}
sql "$COCKROACH_ROOT_URL" 'CREATE DATABASE IF NOT EXISTS jandibat;'
sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_migrator; ALTER USER jandibat_migrator WITH PASSWORD '$JANDIBAT_MIGRATOR_PASSWORD';"
sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_api; ALTER USER jandibat_api WITH PASSWORD '$JANDIBAT_API_PASSWORD';"
sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_worker; ALTER USER jandibat_worker WITH PASSWORD '$JANDIBAT_WORKER_PASSWORD';"
sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_maintenance; ALTER USER jandibat_maintenance WITH PASSWORD '$JANDIBAT_MAINTENANCE_PASSWORD';"
sql "$COCKROACH_ROOT_URL" 'GRANT CONNECT ON DATABASE jandibat TO jandibat_migrator WITH GRANT OPTION;
USE jandibat;
GRANT USAGE, CREATE ON SCHEMA public TO jandibat_migrator WITH GRANT OPTION;'
sql "$MIGRATION_DATABASE_URL" 'SELECT current_user();' jandibat_migrator
sql "$API_DATABASE_URL" 'SELECT current_user();' jandibat_api
sql "$WORKER_DATABASE_URL" 'SELECT current_user();' jandibat_worker
sql "$MAINTENANCE_DATABASE_URL" 'SELECT current_user();' jandibat_maintenance
echo 'database accounts bootstrapped'
