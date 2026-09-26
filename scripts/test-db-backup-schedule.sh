#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
umask 077

cat >"$test_dir/cockroach" <<'FAKE'
#!/bin/sh
set -eu
[ "$#" -eq 3 ] && [ "$1" = sql ] && [ "$2" = --set=errexit=true ] && [ "$3" = --format=tsv ] || exit 90
[ "${COCKROACH_URL:-}" = 'postgresql://jandibat_backup_runner:secret-sentinel@localhost:26257/jandibat' ] || exit 91
sql=$(cat)
printf '%s\n' "$sql" >>"$TEST_SQL_LOG"
case "$sql" in
 *'SELECT current_user() AS actor;'*)
  printf 'actor\njandibat_backup_runner\n';;
 *'CREATE SCHEDULE '*)
  [ "$(cat "$TEST_STATE")" = empty ] || exit 92
  case "$sql" in
   *"CREATE SCHEDULE jandibat_backup_schedule_v1"*"FOR BACKUP DATABASE jandibat INTO 'external://jandibat_backup_v1'"*"WITH revision_history"*"RECURRING '10 * * * *'"*"FULL BACKUP '10 0 * * *'"*"first_run = 'now'"*"on_execution_failure = 'retry'"*"on_previous_running = 'wait'"*"updates_cluster_last_backup_time_metric"*) :;;
   *) exit 93;;
  esac
  case "$sql" in *'AS OF SYSTEM TIME'*|*ignore_existing_backups*|*'ALTER '*|*'DROP '*) exit 94;; esac
  printf '%s\n' pair >"$TEST_STATE"
  printf 'schedule_id\tname\tstatus\tfirst_run\tschedule\tbackup_stmt\n'
  printf '101\tjandibat_backup_schedule_v1\tPAUSED\tNULL\t10 * * * *\tbackup\n'
  printf '202\tjandibat_backup_schedule_v1\tACTIVE\t2026-09-26 00:00:00+00:00\t10 0 * * *\tbackup\n';;
 *'SHOW SCHEDULES FOR BACKUP'*)
  printf 'id\tlabel\tschedule_status\tnext_run\tstate\trecurrence\towner\ton_previous_running\ton_execution_failure\tkind\tlinked_id\n'
  case "$(cat "$TEST_STATE")" in
   empty) :;;
   pair) printf '101\tjandibat_backup_schedule_v1\tPAUSED\tNULL\tWaiting for initial backup to complete\t10 * * * *\tjandibat_backup_runner\tWAIT\tRETRY_SOON\tINCREMENTAL\t\n202\tjandibat_backup_schedule_v1\tACTIVE\t2026-09-26 00:00:00+00:00\t\t10 0 * * *\tjandibat_backup_runner\tWAIT\tRETRY_SOON\tFULL\t101\n';;
   active) printf '101\tjandibat_backup_schedule_v1\tACTIVE\t2026-09-26 01:10:00+00:00\t\t10 * * * *\tjandibat_backup_runner\tWAIT\tRETRY_SOON\tINCREMENTAL\t\n202\tjandibat_backup_schedule_v1\tACTIVE\t2026-09-27 00:10:00+00:00\t\t10 0 * * *\tjandibat_backup_runner\tWAIT\tRETRY_SOON\tFULL\t101\n';;
   *) cat "$TEST_STATE";;
  esac;;
 *) exit 95;;
esac
FAKE
chmod 700 "$test_dir/cockroach"

TEST_STATE="$test_dir/state"
TEST_SQL_LOG="$test_dir/sql-log"
export TEST_STATE TEST_SQL_LOG
url='postgresql://jandibat_backup_runner:secret-sentinel@localhost:26257/jandibat'

run() {
 env COCKROACH_SQL_BIN="$test_dir/cockroach" BACKUP_RUNNER_DATABASE_URL="$url" \
  sh "$root/scripts/db-configure-backup-schedule.sh" >"$test_dir/out" 2>&1
}
reject() {
 name=$1
 before=$(grep -c 'CREATE SCHEDULE' "$TEST_SQL_LOG" || true)
 if run; then echo "FAIL: accepted $name" >&2; exit 1; fi
 if grep -Fq secret-sentinel "$test_dir/out"; then echo "FAIL: leaked secret in $name" >&2; exit 1; fi
 after=$(grep -c 'CREATE SCHEDULE' "$TEST_SQL_LOG" || true)
 [ "$before" -eq "$after" ] || { echo "FAIL: wrote an invalid $name" >&2; exit 1; }
}
accept() {
 name=$1
 if ! run; then echo "FAIL: rejected $name" >&2; exit 1; fi
 if grep -Fq secret-sentinel "$test_dir/out"; then echo "FAIL: leaked secret in $name" >&2; exit 1; fi
}

: >"$TEST_SQL_LOG"
printf '%s\n' empty >"$TEST_STATE"
accept 'fresh schedule creation'
[ "$(grep -c 'CREATE SCHEDULE' "$TEST_SQL_LOG")" -eq 1 ] || { echo 'FAIL: fresh creation count' >&2; exit 1; }
accept 'linked initial pair rerun'
[ "$(grep -c 'CREATE SCHEDULE' "$TEST_SQL_LOG")" -eq 1 ] || { echo 'FAIL: rerun created a duplicate' >&2; exit 1; }
printf '%s\n' active >"$TEST_STATE"
accept 'linked active pair'

header='101	jandibat_backup_schedule_v1	PAUSED	NULL	Waiting for initial backup to complete	10 * * * *	jandibat_backup_runner	WAIT	RETRY_SOON	INCREMENTAL	'
full='202	jandibat_backup_schedule_v1	ACTIVE	2026-09-26 00:00:00+00:00		10 0 * * *	jandibat_backup_runner	WAIT	RETRY_SOON	FULL	101'
printf '%s\n' empty >"$TEST_STATE"
# Existing zero rows are the only state in which creation is allowed.
printf '%s\n' "$header" >"$TEST_STATE"; reject 'one row'
printf '%s\n' "$header" "$full" "$full" >"$TEST_STATE"; reject 'three rows'
printf '%s\n' "$header" "${full%101}999" >"$TEST_STATE"; reject 'unlinked same-label pair'
printf '%s\n' "$header" "$(printf '%s\n' "$full" | sed 's/10 0 \* \* \*/11 0 * * */')" >"$TEST_STATE"; reject 'altered cron'
printf '%s\n' "$header" "$(printf '%s\n' "$full" | sed 's/FULL/UNKNOWN/')" >"$TEST_STATE"; reject 'altered destination or command'
printf '%s\n' "$header" "$(printf '%s\n' "$full" | sed 's/RETRY_SOON/RETRY_SCHED/')" >"$TEST_STATE"; reject 'default reschedule option'
printf '%s\n' "$header" "$(printf '%s\n' "$full" | sed 's/ACTIVE/PAUSED/')" >"$TEST_STATE"; reject 'paused full'
printf '%s\n' "$(printf '%s\n' "$header" | sed 's/Waiting for initial backup to complete/manual pause/')" "$full" >"$TEST_STATE"; reject 'manual incremental pause'
printf '%s\n' "$header" "$full" '303	other_schedule	ACTIVE	2026-09-27 00:10:00+00:00		10 0 * * *	jandibat_backup_runner	WAIT	RETRY_SOON	UNKNOWN	' >"$TEST_STATE"; reject 'another schedule targeting destination'

if env -u BACKUP_RUNNER_DATABASE_URL COCKROACH_SQL_BIN="$test_dir/cockroach" sh "$root/scripts/db-configure-backup-schedule.sh" >"$test_dir/out" 2>&1; then
 echo 'FAIL: missing runner URL accepted' >&2; exit 1
fi
if grep -Fq secret-sentinel "$test_dir/out"; then echo 'FAIL: leaked secret' >&2; exit 1; fi
echo 'backup schedule preflight fake boundary passed'
