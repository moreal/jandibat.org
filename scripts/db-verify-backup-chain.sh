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
  if (path !~ /^\/?[0-9][0-9][0-9][0-9]\/[0-9][0-9]\/[0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]\.[0-9][0-9]$/) bad=1
  canonical=path
  sub(/^\//,"",canonical)
  if (seen[canonical]++) bad=1
  if (canonical>latest_key) {latest_key=canonical; latest=path}
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
  if (bad || !header || !full || incremental<1) exit 1
  gsub(/ /,"T",previous)
  print previous "Z"
 }
' "$capture_dir/stdout" >"$capture_dir/recovery" || invalid
recovery=$(sed -n '1p' "$capture_dir/recovery")
backup_path=${path#/}
chain_id=$(printf '%s' "$backup_path" | tr / .)
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
awk -v checked="$checked" -v recovery="$recovery" '
 function key(value, parts, count, whole, fraction) {
  sub(/Z$/, "", value)
  count=split(value, parts, ".")
  if (count>2) exit 1
  whole=parts[1]
  gsub(/[-:T]/, "", whole)
  fraction=(count==2 ? parts[2] : "")
  if (length(fraction)>9) exit 1
  while (length(fraction)<9) fraction=fraction "0"
  return "T" whole fraction
 }
 BEGIN {exit key(checked)<key(recovery)}
' || invalid

printf '{"schemaVersion":2,"chainId":"%s","collectionId":"jandibat_backup_v1","checkedAt":"%s","recoveryTimestamp":"%s","fileChecked":true,"passed":true,"backupPath":"%s"}\n' "$chain_id" "$checked" "$recovery" "$backup_path"
