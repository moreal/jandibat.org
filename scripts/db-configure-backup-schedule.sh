#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

invalid() { echo 'backup schedule state or policy invalid' >&2; exit 2; }
failed() { echo 'backup schedule SQL operation failed' >&2; exit 1; }

[ -n "${BACKUP_RUNNER_DATABASE_URL:-}" ] || invalid
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule-capture.XXXXXX") || failed
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM

sql() {
 # The SQL text is fixed and contains only an external connection name.
 # Raw client output can contain destination credentials and is private.
 if ! printf '%s\n' "$1" | (ulimit -f 128 || exit 1
  COCKROACH_URL="$BACKUP_RUNNER_DATABASE_URL" \
  "$sql_bin" sql --set=errexit=true --format=tsv) >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then failed; fi
 [ "$(wc -c <"$capture_dir/stdout")" -le 65536 ] || failed
 [ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || failed
 [ ! -s "$capture_dir/stderr" ] || failed
}

sql 'SELECT current_user() AS actor;'
[ "$(wc -l <"$capture_dir/stdout")" -eq 2 ] || invalid
[ "$(sed -n '1p' "$capture_dir/stdout")" = actor ] || invalid
[ "$(sed -n '2p' "$capture_dir/stdout")" = jandibat_backup_runner ] || invalid

# Compare the generated backup statements with the documented v26.2 shape.
# The query emits a bounded projection, never the raw command JSON. Every
# runner-visible schedule with this label or destination enters the result.
read_schedules() {
 sql "SELECT id, label, schedule_status, next_run, COALESCE(state, '') AS state,
 recurrence, owner, on_previous_running, on_execution_failure,
 CASE
  WHEN command::JSONB->>'backup_statement' =
   'BACKUP DATABASE jandibat INTO LATEST IN ''external://jandibat_backup_v1'' WITH revision_history, detached'
   AND command::JSONB->>'backup_type' = '1'
   AND command::JSONB->>'unpause_on_success' IS NULL THEN 'INCREMENTAL'
  WHEN command::JSONB->>'backup_statement' =
   'BACKUP DATABASE jandibat INTO ''external://jandibat_backup_v1'' WITH revision_history, detached'
   AND command::JSONB->>'backup_type' IS NULL
   AND command::JSONB->>'unpause_on_success' IS NOT NULL THEN 'FULL'
  ELSE 'UNKNOWN'
 END AS kind,
 COALESCE(command::JSONB->>'unpause_on_success', '') AS linked_id
 FROM [SHOW SCHEDULES FOR BACKUP]
 WHERE label = 'jandibat_backup_schedule_v1'
    OR command::STRING LIKE '%external://jandibat_backup_v1%'
 ORDER BY id;"
 awk -F '\t' '
  NR==1 {
   if ($0!="id\tlabel\tschedule_status\tnext_run\tstate\trecurrence\towner\ton_previous_running\ton_execution_failure\tkind\tlinked_id") bad=1
   next
  }
  NF!=11 || NR>3 {bad=1;next}
  {
   if ($1 !~ /^[1-9][0-9]*$/ || ids[$1]++) bad=1
   if ($2!="jandibat_backup_schedule_v1" || $7!="jandibat_backup_runner" ||
       $8!="WAIT" || $9!="RETRY_SOON") bad=1
   if ($10=="INCREMENTAL") {
    incremental++; inc_id=$1
    if ($6!="10 * * * *" || $11!="") bad=1
    if ($3=="PAUSED") {
     if ($4!="NULL" || $5!="Waiting for initial backup to complete") bad=1
    } else if ($3=="ACTIVE") {
     if ($4=="" || $4=="NULL" || $5!="") bad=1
    } else bad=1
   } else if ($10=="FULL") {
    full++; linked=$11
    if ($6!="10 0 * * *" || $3!="ACTIVE" || $4=="" || $4=="NULL" || $5!="") bad=1
   } else bad=1
  }
  END {
   if (bad || NR<1) exit 1
   if (NR==1) {print "absent"; exit 0}
   if (NR!=3 || incremental!=1 || full!=1 || linked!=inc_id) exit 1
   print "matched"
  }
 ' "$capture_dir/stdout" >"$capture_dir/state" || invalid
 state=$(sed -n '1p' "$capture_dir/state")
}

read_schedules
case "$state" in
 matched) echo 'backup schedule pair verified'; exit 0;;
 absent) :;;
 *) invalid;;
esac

# A single CREATE generates the linked pair. A failed or uncertain CREATE is
# never retried in this invocation. No IF NOT EXISTS, adoption, ALTER or DROP.
sql "CREATE SCHEDULE jandibat_backup_schedule_v1
 FOR BACKUP DATABASE jandibat INTO 'external://jandibat_backup_v1'
 WITH revision_history
 RECURRING '10 * * * *'
 FULL BACKUP '10 0 * * *'
 WITH SCHEDULE OPTIONS first_run = 'now', on_execution_failure = 'retry',
  on_previous_running = 'wait', updates_cluster_last_backup_time_metric;"

# CREATE returns two rows. The authoritative validation is a new read of
# SHOW SCHEDULES, which checks both generated statements and the ID link.
read_schedules
[ "$state" = matched ] || invalid
echo 'backup schedule pair verified'
