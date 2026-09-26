#!/bin/sh
set -eu

[ -n "${BACKUP_VERIFIER_DATABASE_URL:-}" ] || { echo 'backup verifier database URL required' >&2; exit 2; }
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d) || { echo 'backup verifier capture unavailable' >&2; exit 1; }
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
sql() {
 if ! printf '%s\n' "$1" | COCKROACH_URL="$BACKUP_VERIFIER_DATABASE_URL" "$sql_bin" sql --set=errexit=true --format=tsv >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then
  echo 'backup role verification failed' >&2
  exit 1
 fi
}
sql_value() {
 sql "$1"
 tail -n 1 "$capture_dir/stdout" | tr -d '\r'
}
expect_one() {
 [ "$(sql_value "$1")" = 1 ] || { echo 'backup role authority mismatch' >&2; exit 1; }
}
expect_denied() {
 if printf '%s\n' "$1" | COCKROACH_URL="$BACKUP_VERIFIER_DATABASE_URL" "$sql_bin" sql --set=errexit=true --format=tsv >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then
  echo 'backup verifier unexpectedly has metadata access' >&2; exit 1
 fi
 grep -Eq 'SQLSTATE: 42501' "$capture_dir/stderr" || {
  echo 'backup verifier denial was not a privilege refusal' >&2; exit 1;
 }
}
audit_database_grants() {
 database=$1
 case "$database" in ''|*[!A-Za-z0-9_.-]*) echo 'backup grant audit unsupported identifier' >&2; exit 1;; esac
 sql "USE \"$database\";
SELECT database_name, schema_name, object_name, object_type, grantee, privilege_type, is_grantable
FROM [SHOW GRANTS FOR jandibat_backup_bootstrap, jandibat_backup_runner, jandibat_backup_verifier]
ORDER BY database_name, schema_name, object_name, object_type, grantee, privilege_type;"
 awk -F '\t' -v db="$database" '
  function allow(schema, object, kind, grantee, privilege, grantable) {
   expected[db FS schema FS object FS kind FS grantee FS privilege FS grantable]=1
  }
  BEGIN {
   allow("public", "NULL", "schema", "public", "CREATE", "f")
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
 ' "$capture_dir/stdout" || { echo 'backup role effective grant mismatch' >&2; exit 1; }
 for grantee in public jandibat_backup_bootstrap jandibat_backup_runner jandibat_backup_verifier; do
  if [ "$grantee" = public ]; then
   predicate="object_type IN ('tables', 'schemas')"
  else
   predicate='role IS DISTINCT FROM grantee OR for_all_roles'
  fi
  expect_one "USE \"$database\"; SELECT IF((SELECT count(*) FROM [SHOW DEFAULT PRIVILEGES FOR GRANTEE $grantee] WHERE $predicate) = 0, 1, 0);"
  if [ "$database" = defaultdb ]; then
   expect_one "USE defaultdb; SELECT IF((SELECT count(*) FROM [SHOW DEFAULT PRIVILEGES FOR GRANTEE $grantee IN SCHEMA jandibat_backup_admin]) = 0, 1, 0);"
  fi
 done
}
sql 'SELECT current_user();'
[ "$(tail -n 1 "$capture_dir/stdout" | tr -d '\r')" = jandibat_backup_verifier ] || {
 echo 'backup verifier identity mismatch' >&2; exit 1;
}
expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON ROLE FOR jandibat_backup_bootstrap, jandibat_backup_runner, jandibat_backup_verifier]) = 0, 1, 0);"
expect_one "SELECT IF((SELECT count(*) FROM [SHOW SYSTEM GRANTS] WHERE grantee IN ('public', 'jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') AND NOT (grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'EXTERNALCONNECTION' AND NOT is_grantable)) = 0 AND (SELECT count(*) FROM [SHOW SYSTEM GRANTS] WHERE grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'EXTERNALCONNECTION' AND NOT is_grantable) = 1, 1, 0);"
expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON DATABASE jandibat] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') AND NOT (grantee = 'jandibat_backup_runner' AND privilege_type = 'BACKUP' AND NOT is_grantable)) = 0 AND (SELECT count(*) FROM [SHOW GRANTS ON DATABASE jandibat] WHERE grantee = 'jandibat_backup_runner' AND privilege_type = 'BACKUP' AND NOT is_grantable) = 1, 1, 0);"
expect_one "SELECT IF((SELECT count(*) FROM [SHOW GRANTS ON DATABASE defaultdb] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') AND NOT (grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'CONNECT' AND NOT is_grantable)) = 0 AND (SELECT count(*) FROM [SHOW GRANTS ON DATABASE defaultdb] WHERE grantee = 'jandibat_backup_bootstrap' AND privilege_type = 'CONNECT' AND NOT is_grantable) = 1, 1, 0);"
sql 'SELECT database_name FROM [SHOW DATABASES] ORDER BY database_name;'
cp "$capture_dir/stdout" "$capture_dir/databases"
seen_defaultdb=false seen_jandibat=false
while IFS= read -r database; do
 [ "$database" != database_name ] || continue
 case "$database" in defaultdb) seen_defaultdb=true;; jandibat) seen_jandibat=true;; esac
 audit_database_grants "$database"
done <"$capture_dir/databases"
[ "$seen_defaultdb" = true ] && [ "$seen_jandibat" = true ] || {
 echo 'backup grant inventory incomplete' >&2; exit 1;
}
audit_database_grants system
expect_denied 'SET allow_unsafe_internals = true; SELECT connection_name FROM defaultdb.jandibat_backup_admin.connection_policy WHERE false;'
expect_denied 'SET allow_unsafe_internals = true; SELECT connection_name FROM defaultdb.jandibat_backup_admin.connection_live_digest WHERE false;'
expect_denied 'SET allow_unsafe_internals = true; SELECT connection_details FROM system.external_connections WHERE false;'
sql 'SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON EXTERNAL CONNECTION jandibat_backup_v1] ORDER BY grantee, privilege_type;'
awk -F '\t' '
 NR==1 { if ($1!="grantee" || $2!="privilege_type" || $3!="is_grantable") bad=1; next }
 $1=="root" && $2=="ALL" && ($3=="false" || $3=="f") { root_all++; next }
 $1=="jandibat_backup_bootstrap" && $2=="DROP" && ($3=="true" || $3=="t") { owner_drop++; next }
 $1=="jandibat_backup_bootstrap" && $2=="USAGE" && ($3=="true" || $3=="t") { owner_usage++; next }
 $1=="jandibat_backup_runner" && $2=="USAGE" && ($3=="false" || $3=="f") { runner_usage++; next }
 $1=="jandibat_backup_verifier" && $2=="USAGE" && ($3=="false" || $3=="f") { verifier_usage++; next }
 { bad=1 }
 END { exit bad || root_all!=1 || owner_drop!=1 || owner_usage!=1 || runner_usage!=1 || verifier_usage!=1 }
' "$capture_dir/stdout" || { echo 'backup connection grant mismatch' >&2; exit 1; }
echo 'backup verifier connection grants checked'
