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
sql 'SELECT current_user();'
[ "$(tail -n 1 "$capture_dir/stdout" | tr -d '\r')" = jandibat_backup_verifier ] || {
 echo 'backup verifier identity mismatch' >&2; exit 1;
}
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
