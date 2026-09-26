#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

invalid() { echo 'backup schedule observation invalid' >&2; exit 2; }
failed() { echo 'backup schedule observation SQL failed' >&2; exit 1; }

[ -n "${BACKUP_VERIFIER_DATABASE_URL:-}" ] || invalid
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule-observe.XXXXXX") || failed
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM

# The client sees only fixed SQL. Never forward its stdout/stderr: catalog
# errors may contain a storage URI, raw command, or sensitive detail.
sql() {
 if ! printf '%s\n' "$1" | (ulimit -f 128 || exit 1
  COCKROACH_URL="$BACKUP_VERIFIER_DATABASE_URL" \
  "$sql_bin" sql --set=errexit=true --format=tsv) \
  >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then failed; fi
 [ "$(wc -c <"$capture_dir/stdout")" -le 65536 ] || failed
 [ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || failed
 [ ! -s "$capture_dir/stderr" ] || failed
}

sql 'SELECT current_user() AS actor;'
awk 'NR==1 {if ($0!="actor") bad=1;next} NR==2 {if ($0!="jandibat_backup_verifier") bad=1;next} {bad=1} END {exit bad || NR!=2}' "$capture_dir/stdout" || invalid

# Re-attest the fixed root-owned projection and exact grants. A changed view
# cannot silently redefine the policy booleans consumed below.
sql "USE defaultdb;
SELECT IF(
 (SELECT count(*) FROM information_schema.views WHERE table_catalog = 'defaultdb'
  AND table_schema = 'jandibat_backup_admin' AND table_name = 'schedule_policy_v1'
  AND sha256(view_definition) = '22618fec289bcb499108404f98ce9a05d2701e6bfeeacd491d399171f483085c') = 1
 AND (SELECT count(*) FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_roles r ON r.oid = c.relowner
  WHERE n.nspname = 'jandibat_backup_admin'
   AND c.relname = 'schedule_policy_v1' AND r.rolname = 'root') = 1
 AND (SELECT count(*) FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.schedule_policy_v1]
  WHERE NOT ((grantee IN ('root','admin') AND privilege_type = 'ALL' AND is_grantable)
   OR (grantee IN ('jandibat_backup_runner','jandibat_backup_verifier')
    AND privilege_type = 'SELECT' AND NOT is_grantable))) = 0
 AND (SELECT count(*) FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.schedule_policy_v1]) = 4
 AND (SELECT count(*) FROM [SHOW GRANTS ON SCHEMA defaultdb.jandibat_backup_admin]
  WHERE NOT ((grantee IN ('root','admin') AND privilege_type = 'ALL' AND is_grantable)
   OR (grantee IN ('jandibat_backup_bootstrap','jandibat_backup_runner','jandibat_backup_verifier')
    AND privilege_type = 'USAGE' AND NOT is_grantable))) = 0
 AND (SELECT count(*) FROM [SHOW GRANTS ON SCHEMA defaultdb.jandibat_backup_admin]) = 5,
 1, 0) AS valid;"
awk 'NR==1 {if ($0!="SET") bad=1;next} NR==2 {if ($0!="valid") bad=1;next} NR==3 {if ($0!="1") bad=1;next} {bad=1} END {exit bad || NR!=3}' "$capture_dir/stdout" || invalid

# The verifier must still be unable to inspect raw scheduled-jobs rows.
if printf '%s\n' 'SET allow_unsafe_internals = true; SELECT id FROM system.scheduled_jobs WHERE false;' |
 (ulimit -f 128 || exit 1
  COCKROACH_URL="$BACKUP_VERIFIER_DATABASE_URL" \
  "$sql_bin" sql --set=errexit=true --format=tsv) \
  >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then invalid; fi
[ "$(wc -c <"$capture_dir/stdout")" -le 65536 ] || invalid
[ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || invalid
grep -qx 'SQLSTATE: 42501' "$capture_dir/stderr" || invalid

sql 'SELECT * FROM defaultdb.jandibat_backup_admin.schedule_policy_v1 ORDER BY schedule_id;'
awk -F '\t' '
 function flag(x) {
  if (x=="true" || x=="t") return 1
  if (x=="false" || x=="f") return 0
  bad=1; return -1
 }
 NR==1 {
  if ($0!="schedule_id\tdependent_id\tunpause_id\tlabel_ok\towner_ok\tactive_ok\tinitial_pause_ok\tincremental_cron_ok\tfull_cron_ok\toverlap_wait\tretry_soon\tmetric_disabled\tincremental_command_ok\tfull_command_ok") exit 1
  next
 }
 NF!=14 {bad=1; next}
 {
  if (++rows>2 || $1 !~ /^[1-9][0-9]*$/) bad=1
  for (i=4;i<=14;i++) b[i]=flag($i)
  if (b[4]!=1 || b[5]!=1 || b[10]!=1 || b[11]!=1 || b[12]!=1) bad=1
  if (b[13]==1 && b[14]==0) {
   inc++; inc_id=$1 ""; dependent=$2 ""
   if ($3!="" || $2 !~ /^[1-9][0-9]*$/ || b[8]!=1 || b[9]!=0 ||
       !((b[6]==1 && b[7]==0) || (b[6]==0 && b[7]==1))) bad=1
   inc_active=b[6]; inc_initial=b[7]
  } else if (b[14]==1 && b[13]==0) {
   full++; full_id=$1 ""; full_dependent=$2 ""; unpause=$3 ""
   if ($2 !~ /^[1-9][0-9]*$/ || $3 !~ /^[1-9][0-9]*$/ || b[8]!=0 || b[9]!=1 ||
       b[6]!=1 || b[7]!=0) bad=1
  } else bad=1
 }
 END {
  if (NR<1) exit 1
  if (rows!=2 || inc!=1 || full!=1 || inc_id==full_id ||
      dependent!=full_id || full_dependent!=inc_id || unpause!=inc_id) bad=1
  if (bad) print "false\tfalse"
  else if (inc_initial==1 && inc_active==0) print "true\ttrue"
  else print "true\tfalse"
 }
' "$capture_dir/stdout" >"$capture_dir/state" || invalid
awk -F '\t' 'NR==1 {if (NF!=2 || ($1!="true" && $1!="false") || ($2!="true" && $2!="false")) bad=1;next} {bad=1} END {exit bad || NR!=1}' "$capture_dir/state" || invalid
healthy=$(awk -F '\t' '{print $1}' "$capture_dir/state")
initializing=$(awk -F '\t' '{print $2}' "$capture_dir/state")

sql "SET TIME ZONE 'UTC'; SELECT (now() AT TIME ZONE 'UTC')::STRING AS checked_at;"
awk '
 NR==1 && $0=="SET" {next}
 !header {if ($0!="checked_at") bad=1; header=1; next}
 {
  if (++rows!=1 || $0 !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9](\.[0-9]+)?$/) bad=1
  checked=$0
 }
 END {if (bad || !header || rows!=1) exit 1; gsub(/ /,"T",checked); print checked "Z"}
' "$capture_dir/stdout" >"$capture_dir/checked" || invalid
checked=$(sed -n '1p' "$capture_dir/checked")

printf '{"schemaVersion":1,"checkedAt":"%s","healthy":%s,"initializing":%s}\n' "$checked" "$healthy" "$initializing"
