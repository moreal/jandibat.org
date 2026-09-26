#!/bin/sh
set -eu

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
umask 077
export TEST_SQL_LOG="$test_dir/sql-log" TEST_SQL_CURRENT="$test_dir/sql-current" TEST_REVOKE_STATE="$test_dir/public-create-revoked"
mkdir "$test_dir/bin"
cat >"$test_dir/bin/cockroach" <<'FAKE'
#!/bin/sh
set -eu
case " $* " in *' --url'*|*' --execute'*) exit 90;; esac
[ -n "${COCKROACH_URL:-}" ] || exit 91
cat >"$TEST_SQL_CURRENT"
cat "$TEST_SQL_CURRENT" >>"$TEST_SQL_LOG"
if grep -q 'REVOKE CREATE ON SCHEMA jandibat.public FROM public' "$TEST_SQL_CURRENT"; then
 printf 'revoked\n' >"$TEST_REVOKE_STATE"
fi
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
case "$COCKROACH_URL" in
 *jandibat_backup_verifier@*)
  if grep -Eq 'SELECT connection_name FROM defaultdb.jandibat_backup_admin|SELECT connection_details FROM system.external_connections|SELECT id FROM system.scheduled_jobs' "$TEST_SQL_CURRENT"; then
   [ "${TEST_EXTRA_METADATA_ACCESS:-}" != 1 ] || exit 0
   echo 'SQLSTATE: 42501' >&2
   exit 1
  fi;;
esac
if grep -q 'information_schema.schemata WHERE catalog_name' "$TEST_SQL_CURRENT" && grep -q '::STRING' "$TEST_SQL_CURRENT"; then
 printf 'state\n%s\n' "${TEST_STATE:-0:0:0}"
 exit 0
fi
if grep -q "table_name = 'schedule_policy_v1'" "$TEST_SQL_CURRENT" && grep -q 'AS schedule_view_count' "$TEST_SQL_CURRENT"; then
 printf 'schedule_view_count\n%s\n' "${TEST_SCHEDULE_VIEW_COUNT:-0}"
 exit 0
fi
if grep -q 'SELECT database_name FROM \[SHOW DATABASES\]' "$TEST_SQL_CURRENT"; then
 printf 'database_name\ndefaultdb\njandibat\npostgres\nsystem\n'
 exit 0
fi
if grep -q 'SELECT schema_name FROM \[SHOW SCHEMAS FROM' "$TEST_SQL_CURRENT"; then
 database=$(sed -n 's/.*SHOW SCHEMAS FROM "\([A-Za-z0-9_.-]*\)".*/\1/p' "$TEST_SQL_CURRENT" | head -n 1)
 if [ "${TEST_BAD_OBJECT:-}" = bad_schema_header ] || [ "${TEST_BAD_SCHEMA_HEADER:-}" = 1 ]; then
  printf 'wrong_header\n'
 else
  printf 'schema_name\n'
 fi
 case "$COCKROACH_URL:$database" in *jandibat_backup_verifier@*:system) :;; *) printf 'public\n';; esac
 [ "$database" != defaultdb ] || printf 'jandibat_backup_admin\n'
 [ "$database" != jandibat ] || printf 'private_probe\n'
 if [ "$database" = jandibat ] && { [ "${TEST_BAD_OBJECT:-}" = header_named_schema_default ] || [ "${TEST_HEADER_NAMED_SCHEMA_DEFAULT:-}" = 1 ]; }; then
  printf 'schema_name\n'
 fi
 exit 0
fi
if grep -q 'SHOW GRANTS FOR jandibat_backup_bootstrap' "$TEST_SQL_CURRENT"; then
 database=$(sed -n 's/^USE "\([A-Za-z0-9_.-]*\)";.*/\1/p' "$TEST_SQL_CURRENT" | head -n 1)
 printf 'SET\ndatabase_name\tschema_name\tobject_name\tobject_type\tgrantee\tprivilege_type\tis_grantable\n'
 if [ "$database" != jandibat ] || { [ "${TEST_FRESH_PUBLIC_CREATE:-}" = 1 ] && [ ! -s "$TEST_REVOKE_STATE" ]; }; then
  printf '%s\tpublic\tNULL\tschema\tpublic\tCREATE\tf\n' "$database"
 fi
 printf '%s\tpublic\tNULL\tschema\tpublic\tUSAGE\tf\n' "$database"
 case "$database" in
  defaultdb)
   printf 'defaultdb\tNULL\tNULL\tdatabase\tjandibat_backup_bootstrap\tCONNECT\tf\n'
   printf 'defaultdb\tjandibat_backup_admin\tNULL\tschema\tjandibat_backup_bootstrap\tUSAGE\tf\n'
   printf 'defaultdb\tjandibat_backup_admin\tconnection_policy\ttable\tjandibat_backup_bootstrap\tSELECT\tf\n'
   printf 'defaultdb\tjandibat_backup_admin\tconnection_policy\ttable\tjandibat_backup_bootstrap\tINSERT\tf\n'
   printf 'defaultdb\tjandibat_backup_admin\tconnection_live_digest\ttable\tjandibat_backup_bootstrap\tSELECT\tf\n'
   for role in jandibat_backup_runner jandibat_backup_verifier; do
    printf 'defaultdb\tjandibat_backup_admin\tNULL\tschema\t%s\tUSAGE\tf\n' "$role"
    if [ "${TEST_MISSING_SCHEDULE_GRANT:-}" != 1 ] || [ "$role" != jandibat_backup_verifier ]; then
     printf 'defaultdb\tjandibat_backup_admin\tschedule_policy_v1\ttable\t%s\tSELECT\tf\n' "$role"
    fi
   done
   [ "${TEST_EXTRA_SCHEDULE_TABLE_GRANT:-}" != 1 ] || printf 'defaultdb\tjandibat_backup_admin\tschedule_policy_v1\ttable\tjandibat_backup_runner\tINSERT\tf\n'
   ;;
  jandibat)
   printf 'jandibat\tNULL\tNULL\tdatabase\tjandibat_backup_runner\tBACKUP\tf\n'
   if [ "${TEST_BAD_OBJECT:-}" = extra_app_table_grant ] || [ "${TEST_EXTRA_APP_GRANT:-}" = 1 ]; then
    printf 'jandibat\tpublic\tapp_probe\ttable\tjandibat_backup_verifier\tSELECT\tf\n'
   fi;;
  system)
   printf 'system\tpublic\tcomments\ttable\tpublic\tSELECT\tf\n'
   [ "${TEST_EXTRA_SYSTEM_SCHEDULED_JOBS_GRANT:-}" != 1 ] || printf 'system\tpublic\tscheduled_jobs\ttable\tjandibat_backup_verifier\tSELECT\tf\n';;
 esac
 exit 0
fi
if grep -q 'SELECT IF(' "$TEST_SQL_CURRENT"; then
 if [ "${TEST_EXTRA_SYSTEM_GRANT:-}" = 1 ] && grep -q 'SHOW SYSTEM GRANTS' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 if [ "${TEST_EXTRA_DATABASE_GRANT:-}" = 1 ] && grep -q 'SHOW GRANTS ON DATABASE jandibat' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 if [ "${TEST_EXTRA_MEMBERSHIP:-}" = 1 ] && grep -q 'SHOW GRANTS ON ROLE FOR' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 if [ "${TEST_EXTRA_DEFAULT_GRANT:-}" = 1 ] && grep -q 'SHOW DEFAULT PRIVILEGES FOR GRANTEE' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 if [ "${TEST_OTHER_SCHEMA_DEFAULT:-}" = 1 ] && grep -q 'IN SCHEMA "public"' "$TEST_SQL_CURRENT" && grep -q 'USE "jandibat"' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 if [ "${TEST_NAMED_SCHEMA_DEFAULT:-}" = 1 ] && grep -q 'IN SCHEMA "private_probe"' "$TEST_SQL_CURRENT" && grep -q 'USE "jandibat"' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 if [ "${TEST_HEADER_NAMED_SCHEMA_DEFAULT:-}" = 1 ] && grep -q 'IN SCHEMA "schema_name"' "$TEST_SQL_CURRENT" && grep -q 'USE "jandibat"' "$TEST_SQL_CURRENT"; then
  printf 'valid\n0\n'
  exit 0
 fi
 case "${TEST_BAD_OBJECT:-}" in
  missing_private_schema) grep -q 'SHOW SCHEMAS FROM defaultdb' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_owner) grep -q 'pg_catalog.pg_class' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_view_predicate) grep -q 'view_definition' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_schedule_view_definition) grep -q 'schedule_policy_v1' "$TEST_SQL_CURRENT" && grep -q 'view_definition' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_schedule_view_owner) grep -q 'schedule_policy_v1' "$TEST_SQL_CURRENT" && grep -q 'pg_catalog.pg_class' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  nullable_digest) grep -q 'information_schema.columns' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  unexpected_public_grant) grep -q 'SHOW GRANTS ON SCHEMA' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  extra_role_membership) grep -q 'system.role_members' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  public_default_grant) grep -q 'SHOW DEFAULT PRIVILEGES' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  unexpected_public_system_grant) grep -q "grantee = 'public'" "$TEST_SQL_CURRENT" && grep -q 'SHOW SYSTEM GRANTS' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  extra_app_table_grant) grep -q 'SHOW GRANTS FOR' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  cross_creator_default_grant) grep -q 'SHOW DEFAULT PRIVILEGES FOR GRANTEE' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  wrong_fixed_name_constraint) grep -q 'SHOW CONSTRAINTS FROM' "$TEST_SQL_CURRENT" && grep -q 'details' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  unrelated_private_grant) grep -q 'SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin' "$TEST_SQL_CURRENT" && grep -q "grantee = 'admin'" "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  other_schema_default_grant) grep -q 'IN SCHEMA "public"' "$TEST_SQL_CURRENT" && grep -q 'USE "jandibat"' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  other_named_schema_default_grant) grep -q 'IN SCHEMA "private_probe"' "$TEST_SQL_CURRENT" && grep -q 'USE "jandibat"' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
  header_named_schema_default) grep -q 'IN SCHEMA "schema_name"' "$TEST_SQL_CURRENT" && grep -q 'USE "jandibat"' "$TEST_SQL_CURRENT" && { printf 'valid\n0\n'; exit 0; };;
 esac
 printf 'valid\n1\n'
 exit 0
fi
if grep -q 'SELECT \* FROM defaultdb.jandibat_backup_admin.schedule_policy_v1 LIMIT 0' "$TEST_SQL_CURRENT"; then
 printf 'schedule_id\tdependent_id\tunpause_id\tlabel_ok\towner_ok\tactive_ok\tinitial_pause_ok\tincremental_cron_ok\tfull_cron_ok\toverlap_wait\tretry_soon\tmetric_disabled\tincremental_command_ok\tfull_command_ok\n'
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
if grep -q 'REVOKE CREATE ON SCHEMA jandibat.public' "$TEST_SQL_LOG"; then fail 'app-only bootstrap hardened backup public schema'; fi
: >"$TEST_SQL_LOG"
if run_bootstrap env -u JANDIBAT_BACKUP_RUNNER_PASSWORD -u JANDIBAT_BACKUP_VERIFIER_PASSWORD JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD=unset; then
 fail 'present literal unset backup password treated as absent'
fi
[ ! -s "$TEST_SQL_LOG" ] || fail 'present literal unset backup password reached SQL'
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
if ! run_bootstrap env; then fail 'hardened_jandibat_public_schema_baseline rejected'; fi
: >"$TEST_SQL_LOG"
: >"$TEST_REVOKE_STATE"
if ! run_bootstrap env TEST_FRESH_PUBLIC_CREATE=1 TEST_STATE=0:0:0; then fail 'fresh backup-enabled public CREATE was not revoked before audit'; fi
[ -s "$TEST_REVOKE_STATE" ] || fail 'fresh backup-enabled public CREATE revoke missing'
for expected in \
 'CREATE USER IF NOT EXISTS jandibat_backup_bootstrap' \
 'CREATE USER IF NOT EXISTS jandibat_backup_runner' \
 'CREATE USER IF NOT EXISTS jandibat_backup_verifier' \
 'CREATE SCHEMA defaultdb.jandibat_backup_admin AUTHORIZATION root' \
 'CREATE TABLE defaultdb.jandibat_backup_admin.connection_policy' \
 'CREATE VIEW defaultdb.jandibat_backup_admin.connection_live_digest' \
 'CREATE VIEW defaultdb.jandibat_backup_admin.schedule_policy_v1' \
 'SET allow_unsafe_internals = true' \
 'GRANT SYSTEM EXTERNALCONNECTION TO jandibat_backup_bootstrap' \
 'GRANT BACKUP ON DATABASE jandibat TO jandibat_backup_runner' \
 'GRANT USAGE ON SCHEMA defaultdb.jandibat_backup_admin TO jandibat_backup_runner, jandibat_backup_verifier' \
 'GRANT SELECT ON TABLE defaultdb.jandibat_backup_admin.schedule_policy_v1 TO jandibat_backup_runner, jandibat_backup_verifier'; do
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
for bad in missing_private_schema wrong_owner wrong_view_predicate wrong_schedule_view_definition wrong_schedule_view_owner nullable_digest unexpected_public_grant unexpected_public_system_grant extra_role_membership public_default_grant extra_app_table_grant cross_creator_default_grant wrong_fixed_name_constraint unrelated_private_grant other_schema_default_grant other_named_schema_default_grant header_named_schema_default bad_schema_header; do
 : >"$TEST_SQL_LOG"
 if run_bootstrap env TEST_STATE=1:1:1 TEST_SCHEDULE_VIEW_COUNT=1 TEST_BAD_OBJECT="$bad"; then fail "$bad accepted"; fi
 if grep -Eq '^CREATE (SCHEMA|TABLE|VIEW) ' "$TEST_SQL_LOG"; then fail "$bad replaced existing object"; fi
done
if ! env COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'valid verifier grant set rejected'; fi
if env TEST_EXTRA_GRANT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted unexpected public connection grant'; fi
if env TEST_EXTRA_SYSTEM_GRANT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted extra SYSTEM authority'; fi
if env TEST_EXTRA_DATABASE_GRANT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted extra database authority'; fi
if env TEST_EXTRA_MEMBERSHIP=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted role membership'; fi
if env TEST_EXTRA_APP_GRANT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted app-table authority'; fi
for bad in wrong_schedule_view_definition wrong_schedule_view_owner; do
 if env TEST_BAD_OBJECT="$bad" COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
  sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail "verifier accepted $bad"; fi
done
for fault in TEST_MISSING_SCHEDULE_GRANT TEST_EXTRA_SCHEDULE_TABLE_GRANT TEST_EXTRA_SYSTEM_SCHEDULED_JOBS_GRANT; do
 if env "$fault=1" COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
  sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail "verifier accepted $fault"; fi
done
if env TEST_EXTRA_METADATA_ACCESS=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted private metadata access'; fi
if env TEST_EXTRA_DEFAULT_GRANT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted future default-grant authority'; fi
if env TEST_OTHER_SCHEMA_DEFAULT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted jandibat.public future default-grant authority'; fi
if env TEST_NAMED_SCHEMA_DEFAULT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted visible nonpublic future default-grant authority'; fi
if env TEST_HEADER_NAMED_SCHEMA_DEFAULT=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier skipped literal schema_name schema default-grant authority'; fi
if env TEST_BAD_SCHEMA_HEADER=1 COCKROACH_SQL_BIN=cockroach BACKUP_VERIFIER_DATABASE_URL='postgresql://jandibat_backup_verifier@localhost/jandibat' \
 sh scripts/db-verify-backup-roles.sh >"$test_dir/output" 2>&1; then fail 'verifier accepted malformed schema inventory header'; fi
echo 'backup metadata opt-in and SQL contract checks passed'
