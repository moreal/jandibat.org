#!/bin/sh
set -eu

fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
mkdir "$fixture_dir/bin"
canary_a=$(awk 'BEGIN { for (i=0;i<43;i++) printf "A" }')
canary_b=$(awk 'BEGIN { for (i=0;i<43;i++) printf "B" }')
password_c=$(awk 'BEGIN { for (i=0;i<43;i++) printf "C" }')
password_d=$(awk 'BEGIN { for (i=0;i<43;i++) printf "D" }')
export TEST_CALLS="$fixture_dir/calls" TEST_SQL="$fixture_dir/sql" TEST_ALL_SQL="$fixture_dir/all-sql"
cat >"$fixture_dir/bin/cockroach" <<'FAKE'
#!/bin/sh
set -eu
for arg in "$@"; do
 case "$arg" in --url*|--execute*) echo forbidden-argv >>"$TEST_CALLS"; exit 91;; esac
done
[ "${1:-}" = sql ] && [ "${2:-}" = --set=errexit=true ] || exit 92
[ -n "${COCKROACH_URL:-}" ] || exit 93
printf 'call\n' >>"$TEST_CALLS"
cat >"$TEST_SQL"
cat "$TEST_SQL" >>"$TEST_ALL_SQL"
if grep -q 'SELECT current_user()' "$TEST_SQL"; then
 case "$COCKROACH_URL" in
  *'jandibat_migrator@'*) identity=jandibat_migrator ;;
  *'jandibat_api@'*) identity=jandibat_api ;;
  *'jandibat_worker@'*) identity=jandibat_worker ;;
  *'jandibat_maintenance@'*) identity=jandibat_maintenance ;;
  *) exit 95 ;;
 esac
 printf 'current_user\n%s\n' "$identity"
fi
if [ "${TEST_FAIL_CREATE:-}" = 1 ] && grep -q 'CREATE USER' "$TEST_SQL"; then
 cat "$TEST_SQL" >&2
 exit 94
fi
FAKE
chmod +x "$fixture_dir/bin/cockroach"
PATH="$fixture_dir/bin:$PATH"
export PATH

run_bootstrap() {
 env COCKROACH_SQL_BIN=cockroach \
  COCKROACH_ROOT_URL='postgresql://root@localhost:26257/defaultdb?sslmode=verify-full' \
  MIGRATION_DATABASE_URL='postgresql://jandibat_migrator@localhost:26257/jandibat' \
  API_DATABASE_URL='postgresql://jandibat_api@localhost:26257/jandibat' \
  WORKER_DATABASE_URL='postgresql://jandibat_worker@localhost:26257/jandibat' \
  MAINTENANCE_DATABASE_URL='postgresql://jandibat_maintenance@localhost:26257/jandibat' \
  JANDIBAT_MIGRATOR_PASSWORD="$canary_a" JANDIBAT_API_PASSWORD="$canary_b" \
  JANDIBAT_WORKER_PASSWORD="$password_c" JANDIBAT_MAINTENANCE_PASSWORD="$password_d" \
  "$@" sh scripts/db-bootstrap-roles.sh >"$fixture_dir/output" 2>&1
}
assert_no_canary() {
 if grep -Fq "$canary_a" "$fixture_dir/output" || grep -Fq "$canary_b" "$fixture_dir/output"; then
  echo 'FAIL: bootstrap disclosed a password' >&2; exit 1
 fi
}
reject() {
 name=$1; shift
 : >"$TEST_CALLS"
 if run_bootstrap "$@"; then echo "FAIL: accepted $name" >&2; exit 1; fi
 assert_no_canary
 if [ -s "$TEST_CALLS" ]; then echo "FAIL: connected before rejecting $name" >&2; exit 1; fi
}
reject missing-root env -u COCKROACH_ROOT_URL
reject empty env JANDIBAT_API_PASSWORD=
reject duplicate env JANDIBAT_API_PASSWORD="$canary_a"
reject placeholder env JANDIBAT_API_PASSWORD=REPLACE_ME
reject short env JANDIBAT_API_PASSWORD="${canary_b%?}"
reject quote env JANDIBAT_API_PASSWORD="${canary_b}'"
reject semicolon env JANDIBAT_API_PASSWORD="${canary_b};"
newline=$(printf '\nX')
reject newline env JANDIBAT_API_PASSWORD="${canary_b}${newline}"
reject missing-role-url env -u API_DATABASE_URL

: >"$TEST_CALLS"
: >"$TEST_ALL_SQL"
if ! run_bootstrap env; then echo 'FAIL: valid bootstrap rejected' >&2; exit 1; fi
assert_no_canary
[ "$(wc -l <"$TEST_CALLS" | tr -d ' ')" -eq 10 ] || { echo 'FAIL: expected database, user, scoped authority and login calls' >&2; exit 1; }
grep -q '^CREATE DATABASE IF NOT EXISTS jandibat;' "$TEST_ALL_SQL" || { echo 'FAIL: missing database create' >&2; exit 1; }
grep -q '^GRANT CONNECT ON DATABASE jandibat TO jandibat_migrator WITH GRANT OPTION;' "$TEST_ALL_SQL" || { echo 'FAIL: migrator cannot delegate database CONNECT' >&2; exit 1; }
grep -q '^GRANT USAGE, CREATE ON SCHEMA public TO jandibat_migrator WITH GRANT OPTION;' "$TEST_ALL_SQL" || { echo 'FAIL: migrator cannot delegate public schema privileges' >&2; exit 1; }
if grep -Eq 'GRANT (SYSTEM|ALL)|GRANT .* ON TABLE .* TO jandibat_migrator|GRANT .* TO jandibat_(api|worker|maintenance)' "$TEST_ALL_SQL"; then
 echo 'FAIL: bootstrap exceeded database/schema authority' >&2; exit 1
fi

if run_bootstrap env API_DATABASE_URL='postgresql://jandibat_migrator@localhost:26257/jandibat'; then
 echo 'FAIL: role DSN authenticated as a different account' >&2; exit 1
fi
assert_no_canary

: >"$TEST_CALLS"
if run_bootstrap env TEST_FAIL_CREATE=1; then echo 'FAIL: account creation failure was accepted' >&2; exit 1; fi
assert_no_canary
[ -s "$TEST_CALLS" ] || { echo 'FAIL: forced CLI failure did not execute' >&2; exit 1; }
echo 'bootstrap input, argv and failure-redaction checks passed'
