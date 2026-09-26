#!/bin/sh
set -eu

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
umask 077
export TEST_SQL_LOG="$test_dir/sql-log" TEST_SQL_CURRENT="$test_dir/sql-current"
mkdir "$test_dir/bin"
cat >"$test_dir/bin/cockroach" <<'FAKE'
#!/bin/sh
set -eu
case " $* " in *' --url'*|*' --execute'*) exit 90;; esac
[ -n "${COCKROACH_URL:-}" ] || exit 91
cat >"$TEST_SQL_CURRENT"
cat "$TEST_SQL_CURRENT" >>"$TEST_SQL_LOG"
if grep -q 'CREATE VIEW defaultdb.jandibat_backup_admin.connection_live_digest' "$TEST_SQL_CURRENT" &&
 ! grep -q 'SET allow_unsafe_internals = true' "$TEST_SQL_CURRENT"; then
 echo 'unsafe_internal_session_missing' >&2
 exit 93
fi
if grep -q 'system.role_members' "$TEST_SQL_CURRENT" &&
 ! grep -q 'SET allow_unsafe_internals = true' "$TEST_SQL_CURRENT"; then
 echo 'unsafe_internal_session_missing' >&2
 exit 93
fi
if grep -q 'information_schema.schemata WHERE catalog_name' "$TEST_SQL_CURRENT" && grep -q '::STRING' "$TEST_SQL_CURRENT"; then
 printf 'state\n%s\n' "${TEST_STATE:-0:0:0}"
 exit 0
fi
if grep -q 'SELECT IF(' "$TEST_SQL_CURRENT"; then
 case "${TEST_BAD_OBJECT:-}" in
  missing_private_schema) grep -q 'SHOW SCHEMAS FROM defaultdb' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_owner) grep -q 'pg_catalog.pg_class' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_view_predicate) grep -q 'view_definition' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  nullable_digest) grep -q 'information_schema.columns' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  unexpected_public_grant) grep -q 'SHOW GRANTS ON SCHEMA' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  extra_role_membership) grep -q 'system.role_members' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  public_default_grant) grep -q 'SHOW DEFAULT PRIVILEGES' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  unexpected_public_system_grant) grep -q "grantee = 'public'" "$TEST_SQL_CURRENT" && grep -q 'SHOW SYSTEM GRANTS' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
 esac
 printf 'valid\n1\n'
 exit 0
fi
if grep -q 'SELECT current_user()' "$TEST_SQL_CURRENT"; then
 case "$COCKROACH_URL" in
  *jandibat_migrator@*) identity=jandibat_migrator;;
  *jandibat_api@*) identity=jandibat_api;;
  *jandibat_worker@*) identity=jandibat_worker;;
  *jandibat_maintenance@*) identity=jandibat_maintenance;;
  *jandibat_backup_bootstrap@*) identity=jandibat_backup_bootstrap;;
  *jandibat_backup_runner@*) identity=jandibat_backup_runner;;
  *jandibat_backup_verifier@*) identity=jandibat_backup_verifier;;
  *) exit 92;;
 esac
 printf 'current_user\n%s\n' "$identity"
fi
if grep -q 'SHOW GRANTS ON EXTERNAL CONNECTION' "$TEST_SQL_CURRENT"; then
 printf 'grantee\tprivilege_type\tis_grantable\n'
 printf 'root\tALL\tf\n'
 printf 'jandibat_backup_bootstrap\tDROP\tt\n'
 printf 'jandibat_backup_bootstrap\tUSAGE\tt\n'
 printf 'jandibat_backup_runner\tUSAGE\tf\n'
 printf 'jandibat_backup_verifier\tUSAGE\tf\n'
 [ "${TEST_EXTRA_GRANT:-}" != 1 ] || printf 'public\tUSAGE\tf\n'
fi
FAKE
chmod +x "$test_dir/bin/cockroach"
PATH="$test_dir/bin:$PATH"
export PATH
password_a=$(awk 'BEGIN { for (i=0;i<43;i++) printf "A" }')
password_b=$(awk 'BEGIN { for (i=0;i<43;i++) printf "B" }')
password_c=$(awk 'BEGIN { for (i=0;i<43;i++) printf "C" }')
password_d=$(awk 'BEGIN { for (i=0;i<43;i++) printf "D" }')
password_e=$(awk 'BEGIN { for (i=0;i<43;i++) printf "E" }')
password_f=$(awk 'BEGIN { for (i=0;i<43;i++) printf "F" }')
password_g=$(awk 'BEGIN { for (i=0;i<43;i++) printf "G" }')
run_bootstrap() {
 env COCKROACH_SQL_BIN=cockroach \
  COCKROACH_ROOT_URL='postgresql://root@localhost/defaultdb' \
  MIGRATION_DATABASE_URL='postgresql://jandibat_migrator@localhost/jandibat' \
  API_DATABASE_URL='postgresql://jandibat_api@localhost/jandibat' \
  WORKER_DATABASE_URL='postgresql://jandibat_worker@localhost/jandibat' \
  MAINTENANCE_DATABASE_URL='postgresql://jandibat_maintenance@localhost/jandibat' \
  BACKUP_BOOTSTRAP_DATABASE_URL='postgresql://jandibat_backup_bootstrap@localhost/defaultdb' \
  BACKUP_RUNNER_DATABASE_URL='postgresql://jandibat_backup_runner@localhost/jandibat' \
  BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
  JANDIBAT_MIGRATOR_PASSWORD="$password_a" JANDIBAT_API_PASSWORD="$password_b" \
  JANDIBAT_WORKER_PASSWORD="$password_c" JANDIBAT_MAINTENANCE_PASSWORD="$password_d" \
  JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD="$password_e" \
  JANDIBAT_BACKUP_RUNNER_PASSWORD="$password_f" \
  JANDIBAT_BACKUP_VERIFIER_PASSWORD="$password_g" \
  "$@" sh scripts/db-bootstrap-roles.sh >"$test_dir/output" 2>&1
}
fail() { echo "FAIL: $1" >&2; exit 1; }
for role in BOOTSTRAP RUNNER VERIFIER; do
 : >"$TEST_SQL_LOG"
 if run_bootstrap env -u "JANDIBAT_BACKUP_${role}_PASSWORD"; then fail "partial $role backup input accepted"; fi
 [ ! -s "$TEST_SQL_LOG" ] || fail "partial $role backup input reached SQL"
done
for keep in BOOTSTRAP RUNNER VERIFIER; do
 : >"$TEST_SQL_LOG"
 case "$keep" in
  BOOTSTRAP) missing='JANDIBAT_BACKUP_RUNNER_PASSWORD JANDIBAT_BACKUP_VERIFIER_PASSWORD';;
  RUNNER) missing='JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD JANDIBAT_BACKUP_VERIFIER_PASSWORD';;
  VERIFIER) missing='JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD JANDIBAT_BACKUP_RUNNER_PASSWORD';;
 esac
 set -- $missing
 if run_bootstrap env -u "$1" -u "$2"; then fail "single $keep backup input accepted"; fi
 [ ! -s "$TEST_SQL_LOG" ] || fail "single $keep backup input reached SQL"
done
if ! run_bootstrap env -u JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD -u JANDIBAT_BACKUP_RUNNER_PASSWORD -u JANDIBAT_BACKUP_VERIFIER_PASSWORD; then
 fail 'existing four-role bootstrap rejected without backup inputs'
fi
if grep -q 'jandibat_backup_' "$TEST_SQL_LOG"; then fail 'backup SQL ran without backup inputs'; fi
for role in BOOTSTRAP RUNNER VERIFIER; do
 : >"$TEST_SQL_LOG"
 if run_bootstrap env "JANDIBAT_BACKUP_${role}_PASSWORD="; then fail "empty $role backup password accepted"; fi
 [ ! -s "$TEST_SQL_LOG" ] || fail "empty $role backup password reached SQL"
done
for role in BOOTSTRAP RUNNER VERIFIER; do
 : >"$TEST_SQL_LOG"
 if run_bootstrap env "JANDIBAT_BACKUP_${role}_PASSWORD=REPLACE_ME"; then fail "placeholder $role backup password accepted"; fi
 [ ! -s "$TEST_SQL_LOG" ] || fail "placeholder $role backup password reached SQL"
done
: >"$TEST_SQL_LOG"
if run_bootstrap env JANDIBAT_BACKUP_RUNNER_PASSWORD="$password_e"; then fail 'duplicate backup password accepted'; fi
[ ! -s "$TEST_SQL_LOG" ] || fail 'duplicate backup password reached SQL'
: >"$TEST_SQL_LOG"
if ! run_bootstrap env; then fail 'complete backup inputs rejected'; fi
for expected in \
 'CREATE USER IF NOT EXISTS jandibat_backup_bootstrap' \
 'CREATE USER IF NOT EXISTS jandibat_backup_runner' \
 'CREATE USER IF NOT EXISTS jandibat_backup_verifier' \
 'CREATE SCHEMA defaultdb.jandibat_backup_admin AUTHORIZATION root' \
 'CREATE TABLE defaultdb.jandibat_backup_admin.connection_policy' \
 'CREATE VIEW defaultdb.jandibat_backup_admin.connection_live_digest' \
 'SET allow_unsafe_internals = true' \
 'GRANT SYSTEM EXTERNALCONNECTION TO jandibat_backup_bootstrap' \
 'GRANT BACKUP ON DATABASE jandibat TO jandibat_backup_runner'; do
 grep -Fq "$expected" "$TEST_SQL_LOG" || fail "missing backup SQL: $expected"
done
if grep -Eiq 'CREATE OR REPLACE|CREATE (SCHEMA|TABLE|VIEW) IF NOT EXISTS defaultdb.jandibat_backup_admin' "$TEST_SQL_LOG"; then
 fail 'metadata DDL could mask an existing incompatible object'
fi
for state in 1:0:0 1:1:0 0:1:1; do
 : >"$TEST_SQL_LOG"
 if run_bootstrap env TEST_STATE="$state"; then fail "partial existing object state $state accepted"; fi
 if grep -Eq '^CREATE (SCHEMA|TABLE|VIEW) ' "$TEST_SQL_LOG"; then fail "partial existing object state $state replaced"; fi
done
for bad in missing_private_schema wrong_owner wrong_view_predicate nullable_digest unexpected_public_grant extra_role_membership public_default_grant unexpected_public_system_grant; do
 : >"$TEST_SQL_LOG"
 if run_bootstrap env TEST_STATE=1:1:1 TEST_BAD_OBJECT="$bad"; then fail "$bad accepted"; fi
 if grep -Eq '^CREATE (SCHEMA|TABLE|VIEW) ' "$TEST_SQL_LOG"; then fail "$bad replaced existing object"; fi
done
if ! env COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'valid verifier grant set rejected'; fi
if env TEST_EXTRA_GRANT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted unexpected public connection grant'; fi
echo 'backup metadata opt-in and SQL contract checks passed'
