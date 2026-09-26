#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
umask 077

inc_id=588819866656997377
full_id=588819866656997378
header='schedule_id	dependent_id	unpause_id	label_ok	owner_ok	active_ok	initial_pause_ok	incremental_cron_ok	full_cron_ok	overlap_wait	retry_soon	metric_disabled	incremental_command_ok	full_command_ok'
inc="$inc_id	$full_id		true	true	false	true	true	false	true	true	true	true	false"
full="$full_id	$inc_id	$inc_id	true	true	true	false	false	true	true	true	true	false	true"
printf '%s\n' "$inc" "$full" >"$test_dir/pair"
printf '%s\n' empty >"$test_dir/state"

cat >"$test_dir/cockroach" <<'FAKE'
#!/bin/sh
set -eu
[ "$#" -eq 3 ] && [ "$1" = sql ] && [ "$2" = --set=errexit=true ] && [ "$3" = --format=tsv ] || exit 90
[ "${COCKROACH_URL:-}" = 'postgresql://jandibat_backup_runner:secret-sentinel@localhost:26257/jandibat' ] || exit 91
sql=$(cat)
printf '%s\n' "$sql" >>"$TEST_SQL_LOG"
case "$sql" in
 *'SELECT current_user() AS actor;'*) printf 'actor\njandibat_backup_runner\n';;
 *'sha256(view_definition)'*)
  case "$sql" in *'USE defaultdb;'*'schedule_policy_v1'*) :;; *) exit 92;; esac
  printf 'SET\nvalid\n1\n';;
 *'CREATE SCHEDULE IF NOT EXISTS '*)
  case "$sql" in
   *'BEGIN;'*'SELECT count(*) AS candidate_count FROM defaultdb.jandibat_backup_admin.schedule_policy_v1;'*"CREATE SCHEDULE IF NOT EXISTS jandibat_backup_schedule_v1"*"FOR BACKUP DATABASE jandibat INTO 'external://jandibat_backup_v1'"*"WITH revision_history"*"RECURRING '10 * * * *'"*"FULL BACKUP '10 0 * * *'"*"first_run = 'now'"*"on_execution_failure = 'retry'"*"on_previous_running = 'wait'"*'COMMIT;'*) :;;
   *) exit 93;;
  esac
  case "$sql" in *'AS OF SYSTEM TIME'*|*ignore_existing_backups*|*'updates_cluster_last_backup_time_metric'*|*'ALTER '*|*'DROP '*) exit 94;; esac
  case "${TEST_TXN_MODE:-normal}" in
   error) printf 'secret-sentinel SQLSTATE: 40001\n' >&2; exit 1;;
   provisional)
    printf 'BEGIN\ncandidate_count\n0\nschedule_id\tlabel\tstatus\tfirst_run\tschedule\tbackup_stmt\n'
    printf '588819866656997377\tjandibat_backup_schedule_v1\tPAUSED\tNULL\t10 * * * *\tsecret-sentinel\n'
    printf '588819866656997378\tjandibat_backup_schedule_v1\tACTIVE\t2026-09-26 00:10:00+00:00\t10 0 * * *\tsecret-sentinel\n'
    printf 'SQLSTATE: 40001 secret-sentinel\n' >&2; exit 1;;
   oversize) awk 'BEGIN {for (i=0;i<70000;i++) printf "s"; print "secret-sentinel"}'; exit 0;;
   *) :;;
  esac
  if [ "${TEST_RACE:-}" = 1 ]; then
   if ! mkdir "$TEST_RACE_LOCK" 2>/dev/null; then
    printf 'SQLSTATE: 40001 secret-sentinel\n' >&2
    exit 1
   fi
  fi
  case "${TEST_TXN_MODE:-normal}" in
   skipped) cp "$TEST_PAIR" "$TEST_STATE"; printf 'BEGIN\ncandidate_count\n0\nschedule_id\tlabel\tstatus\tfirst_run\tschedule\tbackup_stmt\nCOMMIT\n'; exit 0;;
   commit_only) printf 'BEGIN\ncandidate_count\n0\nCOMMIT\n'; exit 0;;
   nonzero) cp "$TEST_PAIR" "$TEST_STATE"; printf 'BEGIN\ncandidate_count\n2\nschedule_id\tlabel\tstatus\tfirst_run\tschedule\tbackup_stmt\nCOMMIT\n'; exit 0;;
   *) :;;
  esac
  cp "$TEST_PAIR" "$TEST_STATE"
  printf 'BEGIN\ncandidate_count\n0\nschedule_id\tlabel\tstatus\tfirst_run\tschedule\tbackup_stmt\n'
  printf '588819866656997377\tjandibat_backup_schedule_v1\tPAUSED\tNULL\t10 * * * *\tsecret-sentinel\n'
  printf '588819866656997378\tjandibat_backup_schedule_v1\tACTIVE\t2026-09-26 00:10:00+00:00\t10 0 * * *\tsecret-sentinel\n'
  if [ "${TEST_TXN_MODE:-}" = post_drift ]; then
   sed 's/true	true	false	true$/true	false	false	true/' "$TEST_PAIR" >"$TEST_STATE"
  fi
  [ "${TEST_TXN_MODE:-}" = ambiguous ] || printf 'COMMIT\n';;
 *'SELECT * FROM defaultdb.jandibat_backup_admin.schedule_policy_v1 ORDER BY schedule_id;'*)
  if [ "${TEST_RACE:-}" = 1 ] && [ ! -d "$TEST_RACE_LOCK" ]; then
   printf 'seen\n' >>"$TEST_RACE_READS"
   n=0
   while [ "$(wc -l <"$TEST_RACE_READS")" -lt 2 ]; do
    n=$((n+1)); [ "$n" -lt 200 ] || exit 96
    sleep 0.01
   done
   printf '%s\n' "$TEST_HEADER"
   exit 0
  fi
  [ "${TEST_VIEW_HEADER:-}" != malformed ] || { printf 'wrong_header\n'; exit 0; }
  printf '%s\n' "$TEST_HEADER"
  [ "$(cat "$TEST_STATE")" = empty ] || cat "$TEST_STATE";;
 *) exit 95;;
esac
FAKE
chmod 700 "$test_dir/cockroach"

TEST_STATE="$test_dir/state"
TEST_PAIR="$test_dir/pair"
TEST_HEADER="$header"
TEST_SQL_LOG="$test_dir/sql-log"
TEST_RACE_READS="$test_dir/race-reads"
TEST_RACE_LOCK="$test_dir/race-lock"
export TEST_STATE TEST_PAIR TEST_HEADER TEST_SQL_LOG TEST_RACE_READS TEST_RACE_LOCK
: >"$TEST_SQL_LOG"
: >"$TEST_RACE_READS"
url='postgresql://jandibat_backup_runner:secret-sentinel@localhost:26257/jandibat'

run() {
 output=${1:-$test_dir/out}
 env COCKROACH_SQL_BIN="$test_dir/cockroach" BACKUP_RUNNER_DATABASE_URL="$url" \
  sh "$root/scripts/db-configure-backup-schedule.sh" >"$output" 2>&1
}
private() {
 if grep -Fq secret-sentinel "$test_dir/out"; then
  echo "FAIL: raw client output escaped during $1" >&2; exit 1
 fi
}
accept() { if ! run; then echo "FAIL: rejected $1" >&2; exit 1; fi; private "$1"; }
reject() { if run; then echo "FAIL: accepted $1" >&2; exit 1; fi; private "$1"; }

accept 'fresh transaction and independent post-commit pair'
[ "$(grep -c 'CREATE SCHEDULE IF NOT EXISTS' "$TEST_SQL_LOG")" -eq 1 ] || { echo 'FAIL: fresh create count' >&2; exit 1; }
accept 'linked initial pair rerun'
[ "$(grep -c 'CREATE SCHEDULE IF NOT EXISTS' "$TEST_SQL_LOG")" -eq 1 ] || { echo 'FAIL: rerun created a duplicate' >&2; exit 1; }

printf '%s\n' "$inc" "$(printf '%s\n' "$full" | sed "s/	$inc_id	/	$full_id	/")" >"$TEST_STATE"
reject 'one-off native-sized full dependent link'
printf '%s\n' "$inc" "$(printf '%s\n' "$full" | sed "s/	$inc_id	true/	$full_id	true/")" >"$TEST_STATE"
reject 'one-off native-sized full unpause link'
printf '%s\n' "$(printf '%s\n' "$inc" | sed "s/$full_id/$inc_id/")" "$full" >"$TEST_STATE"
reject 'wrong incremental-to-full link'
active_inc=$(printf '%s\n' "$inc" | sed 's/	false	true	true	false/	true	false	true	false/')
printf '%s\n' "$active_inc" "$full" >"$TEST_STATE"; accept 'linked active pair'
printf '%s\n' "$inc" >"$TEST_STATE"; reject 'one row'
printf '%s\n' "$inc" "$full" "$full" >"$TEST_STATE"; reject 'three rows'
printf '%s\n' "$inc" "$(printf '%s\n' "$full" | sed 's/	true	true	true	false	false	true/	true	false	true	false	false	true/')" >"$TEST_STATE"; reject 'other-owner reserved destination'
printf '%s\n' "$inc" "$full" "$(printf '%s\n' "$full" | sed 's/	true	true	true	false	false	true/	true	false	true	false	false	true/')" >"$TEST_STATE"; reject 'third other-owner reserved label'
printf '%s\n' "$(printf '%s\n' "$inc" | sed 's/	true	false$/	false	false/')" "$full" >"$TEST_STATE"; reject 'incremental command drift'
printf '%s\n' "$inc" "$(printf '%s\n' "$full" | sed 's/	true	true	false	true$/	true	true	false	false/')" >"$TEST_STATE"; reject 'full command or destination drift'
printf '%s\n' "$inc" "$(printf '%s\n' "$full" | sed 's/	true	true	false	true$/	true	false	false	true/')" >"$TEST_STATE"; reject 'persisted metric true'
TEST_VIEW_HEADER=malformed; export TEST_VIEW_HEADER
printf '%s\n' "$inc" "$full" >"$TEST_STATE"; reject 'view header mismatch'
unset TEST_VIEW_HEADER
printf '%s\n' empty >"$TEST_STATE"
for mode in error provisional oversize skipped commit_only nonzero ambiguous post_drift; do
 TEST_TXN_MODE=$mode; export TEST_TXN_MODE
 reject "$mode transaction"
 printf '%s\n' empty >"$TEST_STATE"
done
unset TEST_TXN_MODE

# Both creators observe zero. The fake models Cockroach's one-commit/one-40001.
printf '%s\n' empty >"$TEST_STATE"
: >"$TEST_RACE_READS"
TEST_RACE=1; export TEST_RACE
(if run "$test_dir/first-out"; then echo 0; else echo 1; fi >"$test_dir/first-status") &
first=$!
(if run "$test_dir/second-out"; then echo 0; else echo 1; fi >"$test_dir/second-status") &
second=$!
wait "$first" || :
wait "$second" || :
unset TEST_RACE
first_status=$(cat "$test_dir/first-status")
second_status=$(cat "$test_dir/second-status")
case "$first_status:$second_status" in 0:1|1:0) :;; *) echo 'FAIL: concurrent result not one success and one refusal' >&2; exit 1;; esac
[ ! -e "$test_dir/first-out" ] || ! grep -Fq secret-sentinel "$test_dir/first-out" || { echo 'FAIL: first racer leaked raw client output' >&2; exit 1; }
[ ! -e "$test_dir/second-out" ] || ! grep -Fq secret-sentinel "$test_dir/second-out" || { echo 'FAIL: second racer leaked raw client output' >&2; exit 1; }
[ "$(wc -l <"$TEST_STATE")" -eq 2 ] || { echo 'FAIL: concurrent creation left wrong row count' >&2; exit 1; }

if env -u BACKUP_RUNNER_DATABASE_URL COCKROACH_SQL_BIN="$test_dir/cockroach" sh "$root/scripts/db-configure-backup-schedule.sh" >"$test_dir/out" 2>&1; then
 echo 'FAIL: missing runner URL accepted' >&2; exit 1
fi
private 'missing DSN'
echo 'backup schedule safe-view, transaction and race fake boundary passed'
