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

backup_inputs=0
[ "${JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD+x}" != x ] || backup_inputs=$((backup_inputs + 1))
[ "${JANDIBAT_BACKUP_RUNNER_PASSWORD+x}" != x ] || backup_inputs=$((backup_inputs + 1))
[ "${JANDIBAT_BACKUP_VERIFIER_PASSWORD+x}" != x ] || backup_inputs=$((backup_inputs + 1))
case "$backup_inputs" in 0|3) :;; *) invalid;; esac
if [ "$backup_inputs" -eq 3 ]; then
 for value in "$JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD" "$JANDIBAT_BACKUP_RUNNER_PASSWORD" "$JANDIBAT_BACKUP_VERIFIER_PASSWORD"; do
  validate_password "$value"
 done
 for value in "${BACKUP_BOOTSTRAP_DATABASE_URL:-}" "${BACKUP_RUNNER_DATABASE_URL:-}" "${BACKUP_VERIFIER_DATABASE_URL:-}"; do
  [ -n "$value" ] || invalid
 done
 for value in "$JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD" "$JANDIBAT_BACKUP_RUNNER_PASSWORD" "$JANDIBAT_BACKUP_VERIFIER_PASSWORD"; do
  for prior in "$JANDIBAT_MIGRATOR_PASSWORD" "$JANDIBAT_API_PASSWORD" "$JANDIBAT_WORKER_PASSWORD" "$JANDIBAT_MAINTENANCE_PASSWORD"; do
   [ "$value" != "$prior" ] || invalid
  done
 done
 [ "$JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD" != "$JANDIBAT_BACKUP_RUNNER_PASSWORD" ] || invalid
 [ "$JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD" != "$JANDIBAT_BACKUP_VERIFIER_PASSWORD" ] || invalid
 [ "$JANDIBAT_BACKUP_RUNNER_PASSWORD" != "$JANDIBAT_BACKUP_VERIFIER_PASSWORD" ] || invalid
fi

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
sql_value() {
 sql "$1" "$2"
 tail -n 1 "$capture_dir/stdout" | tr -d '\r'
}
expect_one() {
 value=$(sql_value "$COCKROACH_ROOT_URL" "SET allow_unsafe_internals = true; $1")
 [ "$value" = 1 ] || { echo 'database backup metadata contract mismatch' >&2; exit 1; }
}
audit_database_grants() {
 database=$1
 case "$database" in ''|*[!A-Za-z0-9_.-]*) echo 'database backup grant audit unsupported identifier' >&2; exit 1;; esac
 sql "$COCKROACH_ROOT_URL" "USE \"$database\";
SELECT database_name, schema_name, object_name, object_type, grantee, privilege_type, is_grantable
FROM [SHOW GRANTS FOR jandibat_backup_bootstrap, jandibat_backup_runner, jandibat_backup_verifier]
ORDER BY database_name, schema_name, object_name, object_type, grantee, privilege_type;"
 awk -F '\t' -v db="$database" '
  function allow(schema, object, kind, grantee, privilege, grantable) {
   expected[db FS schema FS object FS kind FS grantee FS privilege FS grantable]=1
  }
  BEGIN {
   if (db!="jandibat") allow("public", "NULL", "schema", "public", "CREATE", "f")
   allow("public", "NULL", "schema", "public", "USAGE", "f")
   if (db=="defaultdb") {
    allow("NULL", "NULL", "database", "jandibat_backup_bootstrap", "CONNECT", "f")
    allow("jandibat_backup_admin", "NULL", "schema", "jandibat_backup_bootstrap", "USAGE", "f")
    allow("jandibat_backup_admin", "connection_policy", "table", "jandibat_backup_bootstrap", "SELECT", "f")
    allow("jandibat_backup_admin", "connection_policy", "table", "jandibat_backup_bootstrap", "INSERT", "f")
    allow("jandibat_backup_admin", "connection_live_digest", "table", "jandibat_backup_bootstrap", "SELECT", "f")
   } else if (db=="jandibat") {
    allow("NULL", "NULL", "database", "jandibat_backup_runner", "BACKUP", "f")
   } else if (db=="system") {
    allow("public", "comments", "table", "public", "SELECT", "f")
   }
  }
  NR==1 && $0=="SET" { next }
  $1=="database_name" && $2=="schema_name" && $3=="object_name" && $4=="object_type" && $5=="grantee" && $6=="privilege_type" && $7=="is_grantable" { header++; next }
  {
   if (NF!=7 || $1!=db) { bad=1; next }
   grantable=($7=="false" ? "f" : ($7=="true" ? "t" : $7))
   key=$1 FS $2 FS $3 FS $4 FS $5 FS $6 FS grantable
   if (!(key in expected) || ++seen[key]!=1) bad=1
  }
  END {
   if (header!=1) bad=1
   for (key in expected) if (seen[key]!=1) bad=1
   exit bad
  }
 ' "$capture_dir/stdout" || { echo 'database backup effective grant mismatch' >&2; exit 1; }
 sql "$COCKROACH_ROOT_URL" "SELECT schema_name FROM [SHOW SCHEMAS FROM \"$database\"]
WHERE schema_name NOT IN ('crdb_internal', 'information_schema', 'pg_catalog', 'pg_extension')
ORDER BY schema_name;"
 cp "$capture_dir/stdout" "$capture_dir/schemas"
 seen_public=false
 {
  IFS= read -r schema_header && [ "$schema_header" = schema_name ] || {
   echo 'database backup schema inventory malformed' >&2; exit 1;
  }
  while IFS= read -r schema; do
   case "$schema" in ''|*[!A-Za-z0-9_.-]*) echo 'database backup default grant audit unsupported schema' >&2; exit 1;; esac
   [ "$schema" != public ] || seen_public=true
   for grantee in public jandibat_backup_bootstrap jandibat_backup_runner jandibat_backup_verifier; do
    value=$(sql_value "$COCKROACH_ROOT_URL" "USE \"$database\"; SELECT IF((SELECT count(*) FROM [SHOW DEFAULT PRIVILEGES FOR GRANTEE $grantee IN SCHEMA \"$schema\"]) = 0, 1, 0);")
    [ "$value" = 1 ] || { echo 'database backup schema default grant mismatch' >&2; exit 1; }
   done
  done
 } <"$capture_dir/schemas"
 [ "$seen_public" = true ] || { echo 'database backup schema inventory incomplete' >&2; exit 1; }
 for grantee in public jandibat_backup_bootstrap jandibat_backup_runner jandibat_backup_verifier; do
  if [ "$grantee" = public ]; then
   predicate="object_type IN ('tables', 'schemas')"
  else
   predicate='role IS DISTINCT FROM grantee OR for_all_roles'
  fi
  value=$(sql_value "$COCKROACH_ROOT_URL" "USE \"$database\"; SELECT IF((SELECT count(*) FROM [SHOW DEFAULT PRIVILEGES FOR GRANTEE $grantee] WHERE $predicate) = 0, 1, 0);")
  [ "$value" = 1 ] || { echo 'database backup default grant mismatch' >&2; exit 1; }
 done
}
expect_private_grants() {
 target=$1
 kind=$2
 case "$kind" in
  schema) bootstrap_privilege="privilege_type = 'USAGE'"; bootstrap_count=1;;
  table) bootstrap_privilege="privilege_type IN ('SELECT', 'INSERT')"; bootstrap_count=2;;
  view) bootstrap_privilege="privilege_type = 'SELECT'"; bootstrap_count=1;;
 esac
 expect_one "SELECT IF(
 (SELECT count(*) FROM [SHOW GRANTS ON $target] WHERE NOT (
  (grantee = 'root' AND privilege_type = 'ALL' AND is_grantable) OR
  (grantee = 'admin' AND privilege_type = 'ALL' AND is_grantable) OR
  (grantee = 'jandibat_backup_bootstrap' AND $bootstrap_privilege AND NOT is_grantable)
 )) = 0 AND
 (SELECT count(*) FROM [SHOW GRANTS ON $target] WHERE grantee = 'root' AND privilege_type = 'ALL' AND is_grantable) = 1 AND
 (SELECT count(*) FROM [SHOW GRANTS ON $target] WHERE grantee = 'admin' AND privilege_type = 'ALL' AND is_grantable) = 1 AND
 (SELECT count(*) FROM [SHOW GRANTS ON $target] WHERE grantee = 'jandibat_backup_bootstrap' AND $bootstrap_privilege AND NOT is_grantable) = $bootstrap_count,
 1, 0);"
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
if [ "$backup_inputs" -eq 3 ]; then
 # The fresh database still has public CREATE. Match the runtime-role baseline
 # before any backup authority audit; this REVOKE is safe on hardened reruns.
 sql "$COCKROACH_ROOT_URL" 'REVOKE CREATE ON SCHEMA jandibat.public FROM public;'
 # A partial pre-existing set is never adopted. The post-create catalog checks
 # below also reject incompatible definitions and owners on every rerun.
 state=$(sql_value "$COCKROACH_ROOT_URL" "SELECT (SELECT count(*) FROM information_schema.schemata WHERE catalog_name = 'defaultdb' AND schema_name = 'jandibat_backup_admin')::STRING || ':' || (SELECT count(*) FROM information_schema.tables WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_policy' AND table_type = 'BASE TABLE')::STRING || ':' || (SELECT count(*) FROM information_schema.views WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_live_digest')::STRING;")
 case "$state" in
  0:0:0)
   sql "$COCKROACH_ROOT_URL" "CREATE SCHEMA defaultdb.jandibat_backup_admin AUTHORIZATION root;
CREATE TABLE defaultdb.jandibat_backup_admin.connection_policy (
  connection_name STRING PRIMARY KEY CHECK (connection_name = 'jandibat_backup_v1'),
  policy_version INT NOT NULL CHECK (policy_version = 1),
  input_digest STRING NOT NULL CHECK (input_digest ~ '^[0-9a-f]{64}$'),
  catalog_digest STRING NOT NULL CHECK (catalog_digest ~ '^[0-9a-f]{64}$')
);
SET allow_unsafe_internals = true;
CREATE VIEW defaultdb.jandibat_backup_admin.connection_live_digest AS
  SELECT connection_name, sha256(connection_details) AS catalog_digest
  FROM system.external_connections
  WHERE connection_name = 'jandibat_backup_v1';"
   ;;
  1:1:1) :;;
  *) echo 'database backup metadata contract mismatch' >&2; exit 1;;
 esac
 # Structural checks are deliberately read-only on an existing installation.
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW SCHEMAS FROM defaultdb] WHERE schema_name = 'jandibat_backup_admin' AND owner = 'root') = 1, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM information_schema.columns WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_policy' AND ((column_name = 'connection_name' AND data_type = 'text' AND is_nullable = 'NO') OR (column_name = 'policy_version' AND data_type = 'bigint' AND is_nullable = 'NO') OR (column_name IN ('input_digest', 'catalog_digest') AND data_type = 'text' AND is_nullable = 'NO'))) = 4, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM information_schema.columns WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_policy') = 4, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM information_schema.columns WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_live_digest' AND ((column_name = 'connection_name' AND data_type = 'text') OR (column_name = 'catalog_digest' AND data_type = 'text'))) = 2 AND (SELECT count(*) FROM information_schema.columns WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_live_digest') = 2, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM information_schema.columns WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_policy' AND column_default IS NOT NULL) = 0, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW CONSTRAINTS FROM defaultdb.jandibat_backup_admin.connection_policy] WHERE validated AND (
  (constraint_name = 'connection_policy_pkey' AND constraint_type = 'PRIMARY KEY' AND details = 'PRIMARY KEY (connection_name ASC)') OR
  (constraint_name = 'check_connection_name' AND constraint_type = 'CHECK' AND details = 'CHECK ((connection_name = ''jandibat_backup_v1''::STRING))') OR
  (constraint_name = 'check_policy_version' AND constraint_type = 'CHECK' AND details = 'CHECK ((policy_version = 1))') OR
  (constraint_name = 'check_input_digest' AND constraint_type = 'CHECK' AND details = 'CHECK ((input_digest ~ ''^[0-9a-f]{64}$''::STRING))') OR
  (constraint_name = 'check_catalog_digest' AND constraint_type = 'CHECK' AND details = 'CHECK ((catalog_digest ~ ''^[0-9a-f]{64}$''::STRING))')
 )) = 5 AND (SELECT count(*) FROM [SHOW CONSTRAINTS FROM defaultdb.jandibat_backup_admin.connection_policy]) = 5, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM information_schema.views WHERE table_catalog = 'defaultdb' AND table_schema = 'jandibat_backup_admin' AND table_name = 'connection_live_digest' AND view_definition = 'SELECT connection_name, sha256(connection_details) AS catalog_digest FROM system.public.external_connections WHERE connection_name = ''jandibat_backup_v1''') = 1, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace JOIN pg_catalog.pg_roles r ON r.oid = c.relowner WHERE n.nspname = 'jandibat_backup_admin' AND c.relname IN ('connection_policy', 'connection_live_digest') AND r.rolname = 'root') = 2, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM system.role_members WHERE member IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier')) = 0, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON SCHEMA defaultdb.jandibat_backup_admin] WHERE grantee = 'public') = 0, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.connection_policy] WHERE grantee = 'public') = 0, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.connection_live_digest] WHERE grantee = 'public') = 0, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW SYSTEM GRANTS] WHERE grantee = 'public') = 0, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON DATABASE jandibat] WHERE grantee = 'public' AND privilege_type IN ('ALL', 'BACKUP', 'RESTORE')) = 0, 1, 0);"
 sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_backup_bootstrap; ALTER USER jandibat_backup_bootstrap WITH PASSWORD '$JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD';"
 sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_backup_runner; ALTER USER jandibat_backup_runner WITH PASSWORD '$JANDIBAT_BACKUP_RUNNER_PASSWORD';"
 sql "$COCKROACH_ROOT_URL" "CREATE USER IF NOT EXISTS jandibat_backup_verifier; ALTER USER jandibat_backup_verifier WITH PASSWORD '$JANDIBAT_BACKUP_VERIFIER_PASSWORD';"
 sql "$COCKROACH_ROOT_URL" 'GRANT SYSTEM EXTERNALCONNECTION TO jandibat_backup_bootstrap;
GRANT BACKUP ON DATABASE jandibat TO jandibat_backup_runner;
GRANT CONNECT ON DATABASE defaultdb TO jandibat_backup_bootstrap;
GRANT USAGE ON SCHEMA defaultdb.jandibat_backup_admin TO jandibat_backup_bootstrap;
GRANT SELECT, INSERT ON TABLE defaultdb.jandibat_backup_admin.connection_policy TO jandibat_backup_bootstrap;
GRANT SELECT ON TABLE defaultdb.jandibat_backup_admin.connection_live_digest TO jandibat_backup_bootstrap;'
 sql "$BACKUP_BOOTSTRAP_DATABASE_URL" 'SELECT current_user();' jandibat_backup_bootstrap
 sql "$BACKUP_RUNNER_DATABASE_URL" 'SELECT current_user();' jandibat_backup_runner
 sql "$BACKUP_VERIFIER_DATABASE_URL" 'SELECT current_user();' jandibat_backup_verifier
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW USERS] WHERE username IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier')) = 3, 1, 0);"
 # Exact direct grants are checked after idempotent GRANTs. Inherited authority
 # is excluded by the membership and public checks above.
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW SYSTEM GRANTS] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') AND NOT (grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'EXTERNALCONNECTION' AND NOT is_grantable)) = 0 AND (SELECT count(*) FROM [SHOW SYSTEM GRANTS] WHERE grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'EXTERNALCONNECTION' AND NOT is_grantable) = 1, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON DATABASE jandibat] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') AND NOT (grantee = 'jandibat_backup_runner' AND privilege_type = 'BACKUP' AND NOT is_grantable)) = 0 AND (SELECT count(*) FROM [SHOW GRANTS ON DATABASE jandibat] WHERE grantee = 'jandibat_backup_runner' AND privilege_type = 'BACKUP' AND NOT is_grantable) = 1, 1, 0);"
 expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON DATABASE defaultdb] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') AND NOT (grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'CONNECT' AND NOT is_grantable)) = 0 AND (SELECT count(*) FROM [SHOW GRANTS ON DATABASE defaultdb] WHERE grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'CONNECT' AND NOT is_grantable) = 1, 1, 0);"
 expect_private_grants 'SCHEMA defaultdb.jandibat_backup_admin' schema
 expect_private_grants 'TABLE defaultdb.jandibat_backup_admin.connection_policy' table
 expect_private_grants 'TABLE defaultdb.jandibat_backup_admin.connection_live_digest' view
 sql "$COCKROACH_ROOT_URL" 'SELECT database_name FROM [SHOW DATABASES] ORDER BY database_name;'
 cp "$capture_dir/stdout" "$capture_dir/databases"
 seen_defaultdb=false seen_jandibat=false seen_system=false
 while IFS= read -r database; do
  [ "$database" != database_name ] || continue
  case "$database" in defaultdb) seen_defaultdb=true;; jandibat) seen_jandibat=true;; system) seen_system=true;; esac
  audit_database_grants "$database"
 done <"$capture_dir/databases"
 [ "$seen_defaultdb" = true ] && [ "$seen_jandibat" = true ] && [ "$seen_system" = true ] || {
  echo 'database backup grant inventory incomplete' >&2; exit 1;
 }
fi
echo 'database accounts bootstrapped'
