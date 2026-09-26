#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

invalid() { echo 'backup chain state or policy invalid' >&2; exit 2; }
failed() { echo 'backup chain SQL operation failed' >&2; exit 1; }

[ -n "${BACKUP_VERIFIER_DATABASE_URL:-}" ] || invalid
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-chain-capture.XXXXXX") || failed
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM

# Statements contain only fixed identifiers and a path validated below. SQL
# client output is private: it may contain remote-storage errors or credentials.
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
[ "$(wc -l <"$capture_dir/stdout")" -eq 2 ] || invalid
[ "$(sed -n '1p' "$capture_dir/stdout")" = actor ] || invalid
[ "$(sed -n '2p' "$capture_dir/stdout")" = jandibat_backup_verifier ] || invalid

# This audit does not expose the view definition, command, URI or grant rows.
# The verifier remains a distinct, read-only identity.
sql "USE defaultdb;
SELECT IF(
 (SELECT count(*) FROM information_schema.views WHERE table_catalog = 'defaultdb'
  AND table_schema = 'jandibat_backup_admin' AND table_name = 'schedule_policy_v1'
  AND sha256(view_definition) = '22618fec289bcb499108404f98ce9a05d2701e6bfeeacd491d399171f483085c') = 1
 AND (SELECT count(*) FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_roles r ON r.oid = c.relowner
  WHERE n.nspname = 'jandibat_backup_admin' AND c.relname = 'schedule_policy_v1'
  AND r.rolname = 'root') = 1
 AND (SELECT count(*) FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.schedule_policy_v1]
  WHERE NOT ((grantee IN ('root', 'admin') AND privilege_type = 'ALL' AND is_grantable)
   OR (grantee IN ('jandibat_backup_runner', 'jandibat_backup_verifier')
    AND privilege_type = 'SELECT' AND NOT is_grantable))) = 0
 AND (SELECT count(*) FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.schedule_policy_v1]) = 4,
 1, 0) AS audit_ok;"
[ "$(wc -l <"$capture_dir/stdout")" -eq 3 ] || invalid
[ "$(sed -n '1p' "$capture_dir/stdout")" = SET ] || invalid
[ "$(sed -n '2p' "$capture_dir/stdout")" = audit_ok ] || invalid
[ "$(sed -n '3p' "$capture_dir/stdout")" = 1 ] || invalid

# A newly broadened verifier role must not silently turn this read-only
# process into a raw schedule-catalog reader. Only a privilege denial is
# accepted; connectivity or syntax errors are not evidence of least privilege.
if printf '%s\n' 'SET allow_unsafe_internals = true; SELECT id FROM system.scheduled_jobs WHERE false;' | \
 COCKROACH_URL="$BACKUP_VERIFIER_DATABASE_URL" \
 "$sql_bin" sql --set=errexit=true --format=tsv \
 >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then invalid; fi
[ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || failed
grep -Eq 'SQLSTATE: 42501' "$capture_dir/stderr" || failed

sql 'SELECT * FROM defaultdb.jandibat_backup_admin.schedule_policy_v1 ORDER BY schedule_id;'
awk -F '\t' '
 NR==1 {
  if ($0!="schedule_id\tdependent_id\tunpause_id\tlabel_ok\towner_ok\tactive_ok\tinitial_pause_ok\tincremental_cron_ok\tfull_cron_ok\toverlap_wait\tretry_soon\tmetric_disabled\tincremental_command_ok\tfull_command_ok") bad=1
  next
 }
 NF!=14 || NR>3 {bad=1; next}
 {
  id=$1 ""
  if (id !~ /^[1-9][0-9]*$/ || ids[id]++) bad=1
  for (i=4;i<=14;i++) {
   if ($i=="true" || $i=="t") flag[i]=1
   else if ($i=="false" || $i=="f") flag[i]=0
   else bad=1
  }
  if (!flag[4] || !flag[5] || !flag[6] || flag[7] || !flag[10] || !flag[11] || !flag[12]) bad=1
  if (flag[13] && !flag[14]) {
   inc++; inc_id=id; dep=$2 ""
   if ($3!="" || !flag[8] || flag[9]) bad=1
  } else if (flag[14] && !flag[13]) {
   full++; full_id=id; unpause=$3 ""
   if ($2!="" || flag[8] || !flag[9]) bad=1
  } else bad=1
 }
 END {
  if (bad || NR!=3 || inc!=1 || full!=1 || inc_id==full_id || dep!=full_id || unpause!=inc_id) exit 1
  print full_id "\t" inc_id
 }
' "$capture_dir/stdout" >"$capture_dir/schedule-ids" || invalid
full_id=$(awk -F '\t' '{print $1}' "$capture_dir/schedule-ids")
inc_id=$(awk -F '\t' '{print $2}' "$capture_dir/schedule-ids")
case "$full_id:$inc_id" in *[!0-9:]*|:|:*|*:) invalid;; esac

# Legacy path mode has no 50-row ID-page boundary. Force it explicitly and
# reject any ID-mode or malformed listing instead of mistaking a page for a
# complete collection. The chosen literal path is syntax-validated below.
sql "SET use_backups_with_ids = false;
SHOW BACKUPS IN 'external://jandibat_backup_v1';"
awk '
 NR==1 && $0=="SET" {next}
 !header {if ($0!="path") bad=1; header=1; next}
 {
  path=$0
  if (path !~ /^\/?[0-9][0-9][0-9][0-9]\/[0-9][0-9]\/[0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]\.[0-9][0-9]$/ || seen[path]++) bad=1
  if (path>latest) latest=path
 }
 END {if (bad || !header || latest=="") exit 1; print latest}
' "$capture_dir/stdout" >"$capture_dir/path" || invalid
path=$(sed -n '1p' "$capture_dir/path")
case "$path" in
 /[0-9]*|[0-9]*) :;;
 *) invalid;;
esac

# SHOW BACKUP with check_files must succeed before any recovery timestamp is
# published. Selecting only the database summary keeps raw object names and
# storage details out of the parser and public result.
sql "SET TIME ZONE 'UTC';
SELECT backup_type,
 (start_time AT TIME ZONE 'UTC')::STRING AS start_time,
 (end_time AT TIME ZONE 'UTC')::STRING AS end_time,
 (end_time <= now()) AS not_future
FROM [SHOW BACKUP FROM '$path' IN 'external://jandibat_backup_v1' WITH check_files]
WHERE object_type = 'database' AND object_name = 'jandibat'
ORDER BY end_time;"
awk -F '\t' '
 NR==1 && $0=="SET" {next}
 !header {if ($0!="backup_type\tstart_time\tend_time\tnot_future") bad=1; header=1; next}
 NF!=4 {bad=1; next}
 {
  if ($3 !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9](\.[0-9]+)?$/ || ($4!="true" && $4!="t")) bad=1
  if ($1=="full" && count==0 && $2=="NULL") {full=1}
  else if ($1=="incremental" && full && count>0 && $2==previous) {incremental++}
  else bad=1
  if (count>0 && $3<=previous) bad=1
  previous=$3; count++
 }
 END {
  if (bad || !header || !full || count<1) exit 1
  gsub(/ /,"T",previous)
  print previous "Z"
 }
' "$capture_dir/stdout" >"$capture_dir/recovery" || invalid
recovery=$(sed -n '1p' "$capture_dir/recovery")
chain_id=$(printf '%s' "$path" | sed 's@^/@@; s@/@.@g')
case "$chain_id" in ''|*[!A-Za-z0-9_.-]*) invalid;; esac
[ "${#chain_id}" -le 128 ] || invalid

# Read the server clock only after check_files has returned successfully.
# The backend persists this as evidence age, distinct from recovery time.
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
awk -v checked="$checked" -v recovery="$recovery" 'BEGIN {exit checked<recovery}' || invalid

printf '{"schemaVersion":1,"chainId":"%s","collectionId":"jandibat_backup_v1","fullScheduleId":"%s","incrementalScheduleId":"%s","recoveryTimestamp":"%s","checkedAt":"%s","fileChecked":true,"passed":true}\n' "$chain_id" "$full_id" "$inc_id" "$recovery" "$checked"
