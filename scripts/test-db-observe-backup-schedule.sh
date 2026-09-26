#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule-observe-test.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
umask 077

fail() { echo "backup schedule observer test failed: $1" >&2; exit 1; }

cat >"$test_dir/sql" <<'FAKE'
#!/bin/sh
set -eu
[ "${COCKROACH_URL:-}" = verifier-test-url ] || exit 90
statement=$(cat)
case "$statement" in
 *'SELECT current_user() AS actor'*) printf 'actor\njandibat_backup_verifier\n';;
 *'information_schema.views'*)
  case "${SCHED_CASE:-normal}" in
   audit_bad) printf 'SET\nvalid\n0\n';;
   secret_error) printf 'synthetic secret in SQL error\n' >&2; exit 1;;
   *) printf 'SET\nvalid\n1\n';;
  esac;;
 *'system.scheduled_jobs'*)
  if [ "${SCHED_CASE:-normal}" = raw_allowed ]; then printf 'SET\nid\n'; else printf 'SQLSTATE: 42501\n' >&2; exit 1; fi;;
 *'SELECT * FROM defaultdb.jandibat_backup_admin.schedule_policy_v1'*)
  if [ "${SCHED_CASE:-normal}" = view_denied ]; then printf 'SQLSTATE: 42501\n' >&2; exit 1; fi
  if [ "${SCHED_CASE:-normal}" = bad_header ]; then printf 'raw_command\n'; exit 0; fi
  printf 'schedule_id\tdependent_id\tunpause_id\tlabel_ok\towner_ok\tactive_ok\tinitial_pause_ok\tincremental_cron_ok\tfull_cron_ok\toverlap_wait\tretry_soon\tmetric_disabled\tincremental_command_ok\tfull_command_ok\n'
  [ "${SCHED_CASE:-normal}" != empty ] || exit 0
  case "${SCHED_CASE:-normal}" in
   initial) printf '9007199254740993\t9007199254740994\t\ttrue\ttrue\tfalse\ttrue\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n';;
   other_pause) printf '9007199254740993\t9007199254740994\t\ttrue\ttrue\tfalse\tfalse\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n';;
   metric) printf '9007199254740993\t9007199254740994\t\ttrue\ttrue\ttrue\tfalse\ttrue\tfalse\ttrue\ttrue\tfalse\ttrue\tfalse\n';;
   owner) printf '9007199254740993\t9007199254740994\t\ttrue\tfalse\ttrue\tfalse\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n';;
   command) printf '9007199254740993\t9007199254740994\t\ttrue\ttrue\ttrue\tfalse\ttrue\tfalse\ttrue\ttrue\ttrue\tfalse\tfalse\n';;
   *) printf '9007199254740993\t9007199254740994\t\ttrue\ttrue\ttrue\tfalse\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n';;
  esac
  [ "${SCHED_CASE:-normal}" != one ] || exit 0
  case "${SCHED_CASE:-normal}" in
   bad_link) printf '9007199254740994\t9007199254740993\t9007199254740995\ttrue\ttrue\ttrue\tfalse\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\ttrue\n';;
   *) printf '9007199254740994\t9007199254740993\t9007199254740993\ttrue\ttrue\ttrue\tfalse\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\ttrue\n';;
  esac
  if [ "${SCHED_CASE:-normal}" = third ]; then printf '9007199254740995\t9007199254740993\t9007199254740993\ttrue\tfalse\ttrue\tfalse\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\ttrue\n'; fi;;
 *'AS checked_at'*) printf 'SET\nchecked_at\n2026-09-26 01:01:00\n';;
 *) exit 91;;
esac
FAKE
chmod 700 "$test_dir/sql"

run_case() {
 scenario=$1
 if SCHED_CASE="$scenario" BACKUP_VERIFIER_DATABASE_URL=verifier-test-url \
  COCKROACH_SQL_BIN="$test_dir/sql" sh "$root/scripts/db-observe-backup-schedule.sh" \
  >"$test_dir/stdout" 2>"$test_dir/stderr"; then status=0; else status=$?; fi
}

for scenario in normal initial empty one third bad_link other_pause metric owner command; do
 run_case "$scenario"
 [ "$status" -eq 0 ] || fail "$scenario refused"
 case "$scenario" in
  normal) expected='{"schemaVersion":1,"checkedAt":"2026-09-26T01:01:00Z","healthy":true,"initializing":false}';;
  initial) expected='{"schemaVersion":1,"checkedAt":"2026-09-26T01:01:00Z","healthy":true,"initializing":true}';;
  *) expected='{"schemaVersion":1,"checkedAt":"2026-09-26T01:01:00Z","healthy":false,"initializing":false}';;
 esac
 [ "$(wc -l <"$test_dir/stdout")" -eq 1 ] || fail "$scenario output cardinality"
 [ "$(sed -n '1p' "$test_dir/stdout")" = "$expected" ] || fail "$scenario exact JSON"
 [ ! -s "$test_dir/stderr" ] || fail "$scenario stderr"
done

for scenario in view_denied bad_header audit_bad raw_allowed secret_error; do
 run_case "$scenario"
 [ "$status" -ne 0 ] || fail "$scenario accepted"
 [ ! -s "$test_dir/stdout" ] || fail "$scenario emitted success JSON"
 if grep -Fq 'synthetic secret' "$test_dir/stderr"; then fail "$scenario leaked raw error"; fi
done

if env -u BACKUP_VERIFIER_DATABASE_URL COCKROACH_SQL_BIN="$test_dir/sql" \
 sh "$root/scripts/db-observe-backup-schedule.sh" >"$test_dir/stdout" 2>"$test_dir/stderr"; then
 fail 'missing verifier URL accepted'
fi
[ ! -s "$test_dir/stdout" ] || fail 'missing verifier URL emitted JSON'

echo 'backup schedule observer contract passed'
