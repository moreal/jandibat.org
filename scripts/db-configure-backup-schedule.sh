#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

phase=input
invalid() { echo "backup schedule state or policy invalid ($phase)" >&2; exit 2; }
failed() { echo "backup schedule SQL operation failed ($phase)" >&2; exit 1; }

[ -n "${BACKUP_RUNNER_DATABASE_URL:-}" ] || invalid
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule-capture.XXXXXX") || failed
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM

sql() {
 # Fixed SQL only. Client output can contain destination details and is never
 # copied to stdout, stderr, logs or a report.
 if ! printf '%s\n' "$1" | (ulimit -f 128 || exit 1
  COCKROACH_URL="$BACKUP_RUNNER_DATABASE_URL" \
  "$sql_bin" sql --set=errexit=true --format=tsv) >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then failed; fi
 [ "$(wc -c <"$capture_dir/stdout")" -le 65536 ] || failed
 [ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || failed
 [ ! -s "$capture_dir/stderr" ] || failed
}

phase=identity
sql 'SELECT current_user() AS actor;'
awk 'NR==1 {if ($0!="actor") bad=1;next} NR==2 {if ($0!="jandibat_backup_runner") bad=1;next} {bad=1} END {exit bad || NR!=2}' "$capture_dir/stdout" || invalid

# The root bootstrap and read-only role verifier also audit this view. Repeat
# the catalog attestation as runner before touching schedule state.
phase=view-audit
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

phase=raw-table-denial
if printf '%s\n' 'SET allow_unsafe_internals = true; SELECT id FROM system.scheduled_jobs WHERE false;' |
 (ulimit -f 128 || exit 1
  COCKROACH_URL="$BACKUP_RUNNER_DATABASE_URL" \
  "$sql_bin" sql --set=errexit=true --format=tsv) >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then
 invalid
fi
[ "$(wc -c <"$capture_dir/stdout")" -le 65536 ] || invalid
[ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || invalid
grep -qx 'SQLSTATE: 42501' "$capture_dir/stderr" || invalid

view_sql='SELECT * FROM defaultdb.jandibat_backup_admin.schedule_policy_v1 ORDER BY schedule_id;'
read_state() {
 sql "$view_sql"
 awk -F '\t' '
  function flag(x) {
   if (x=="true" || x=="t") return 1
   if (x=="false" || x=="f") return 0
   bad=1; return -1
  }
  NR==1 {
   if ($0!="schedule_id\tdependent_id\tunpause_id\tlabel_ok\towner_ok\tactive_ok\tinitial_pause_ok\tincremental_cron_ok\tfull_cron_ok\toverlap_wait\tretry_soon\tmetric_disabled\tincremental_command_ok\tfull_command_ok") bad=1
   next
  }
  NF!=14 || NR>3 {bad=1;next}
  {
   if ($1 !~ /^[1-9][0-9]*$/) bad=1
   for (i=4;i<=14;i++) b[i]=flag($i)
   if (b[4]!=1 || b[5]!=1 || b[10]!=1 || b[11]!=1 || b[12]!=1) bad=1
   if (b[13]==1 && b[14]==0) {
    inc++; inc_id=$1 ""; dependent=$2 ""
    if ($3!="" || $2 !~ /^[1-9][0-9]*$/ || b[8]!=1 || b[9]!=0 ||
        !((b[6]==1 && b[7]==0) || (b[6]==0 && b[7]==1))) bad=1
   } else if (b[14]==1 && b[13]==0) {
    full++; full_id=$1 ""; full_dependent=$2 ""; unpause=$3 ""
    if ($2 !~ /^[1-9][0-9]*$/ || $3 !~ /^[1-9][0-9]*$/ || b[8]!=0 || b[9]!=1 ||
        b[6]!=1 || b[7]!=0) bad=1
   } else bad=1
  }
  END {
   if (bad || NR<1) exit 1
   if (NR==1) {print "absent"; exit}
   if (NR!=3 || inc!=1 || full!=1) exit 1
   print "matched|" inc_id "|" dependent "|" full_id "|" full_dependent "|" unpause
  }
 ' "$capture_dir/stdout" >"$capture_dir/state" || invalid
 IFS='|' read -r state inc_id dependent full_id full_dependent unpause <"$capture_dir/state" || invalid
 if [ "$state" = matched ]; then
  # Shell equality remains exact for native-sized decimal IDs.
  [ "$inc_id" != "$full_id" ] && [ "$dependent" = "$full_id" ] &&
   [ "$full_dependent" = "$inc_id" ] && [ "$unpause" = "$inc_id" ] || invalid
 fi
}

phase=pre-read
read_state
case "$state" in
 matched) echo 'backup schedule pair verified'; exit 0;;
 absent) :;;
 *) invalid;;
esac

# One SQL client owns the SERIALIZABLE read and CREATE. IF NOT EXISTS is only
# a race guard. A skipped CREATE, 40001, missing COMMIT or provisional rows
# must fail this invocation; no automatic retry or adoption.
phase=create
sql "BEGIN;
SELECT count(*) AS candidate_count FROM defaultdb.jandibat_backup_admin.schedule_policy_v1;
CREATE SCHEDULE IF NOT EXISTS jandibat_backup_schedule_v1
 FOR BACKUP DATABASE jandibat INTO 'external://jandibat_backup_v1'
 WITH revision_history
 RECURRING '10 * * * *'
 FULL BACKUP '10 0 * * *'
 WITH SCHEDULE OPTIONS first_run = 'now', on_execution_failure = 'retry',
  on_previous_running = 'wait';
COMMIT;"
phase=create-result
awk -F '\t' '
 NR==1 {if ($0!="BEGIN") bad=1;next}
 NR==2 {if ($0!="candidate_count") bad=1;next}
 NR==3 {if ($0!="0") bad=1;next}
 NR==4 {if ($0!="schedule_id\tlabel\tstatus\tfirst_run\tschedule\tbackup_stmt") bad=1;next}
 NR==5 || NR==6 {
  if (NF!=6 || $1 !~ /^[1-9][0-9]*$/ || $2!="jandibat_backup_schedule_v1") bad=1
  else if ($5=="10 * * * *") {inc++; inc_id=$1 ""}
  else if ($5=="10 0 * * *") {full++; full_id=$1 ""}
  else bad=1
  next
 }
 NR==7 {if ($0!="COMMIT") bad=1;next}
 {bad=1}
 END {
  if (bad || NR!=7 || inc!=1 || full!=1) exit 1
  print inc_id "|" full_id
 }
' "$capture_dir/stdout" >"$capture_dir/created" || invalid
IFS='|' read -r created_inc created_full <"$capture_dir/created" || invalid
case "$created_inc:$created_full" in *[!0-9:]*|:|*:) invalid;; esac
[ "$created_inc" != "$created_full" ] || invalid

# This is a new client and therefore an independent post-COMMIT view read.
phase=post-read
read_state
[ "$state" = matched ] && [ "$inc_id" = "$created_inc" ] &&
 [ "$full_id" = "$created_full" ] || invalid
echo 'backup schedule pair verified'
