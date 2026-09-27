#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-chain-test.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
umask 077

fail() { echo "backup chain test failed: $1" >&2; exit 1; }

# This substitutes only the external SQL client. Assertions consume the real
# verifier's exit status and safe output, not the fake's implementation.
# shellcheck disable=SC2016 # The generated fixture must expand these at runtime.
printf '%s\n' '#!/bin/sh' \
 'set -eu' \
 'case "${COCKROACH_URL:-}" in verifier-test-url) :;; *) exit 1;; esac' \
 'statement=$(cat)' \
 'case "$statement" in' \
 ' *"SELECT current_user() AS actor"*) printf "actor\njandibat_backup_verifier\n";;' \
 ' *"system.scheduled_jobs"*) printf "SQLSTATE: 42501\n" >&2; exit 1;;' \
 ' *"information_schema.views"*"schedule_policy_v1"*)' \
 '  if [ "${CHAIN_CASE:-pass}" = schedule_view_denied ]; then printf "SQLSTATE: 42501\n" >&2; exit 1; fi' \
 '  printf "SET\naudit_ok\n1\n";;' \
 ' *"schedule_policy_v1"*)' \
 '  printf "schedule_id\tdependent_id\tunpause_id\tlabel_ok\towner_ok\tactive_ok\tinitial_pause_ok\tincremental_cron_ok\tfull_cron_ok\toverlap_wait\tretry_soon\tmetric_disabled\tincremental_command_ok\tfull_command_ok\n"' \
 '  case "${CHAIN_CASE:-pass}" in' \
 '   wrong_pair) printf "9007199254740993\t9007199254740994\t\ttrue\ttrue\ttrue\tfalse\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n9007199254740994\t\t9007199254740995\ttrue\ttrue\ttrue\tfalse\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\ttrue\n";;' \
 '   schedule_bad_status) printf "9007199254740993\t9007199254740994\t\ttrue\ttrue\tfalse\ttrue\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n9007199254740994\t\t9007199254740993\ttrue\ttrue\ttrue\tfalse\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\ttrue\n";;' \
 '   *) printf "9007199254740993\t9007199254740994\t\ttrue\ttrue\ttrue\tfalse\ttrue\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\n9007199254740994\t\t9007199254740993\ttrue\ttrue\ttrue\tfalse\tfalse\ttrue\ttrue\ttrue\ttrue\tfalse\ttrue\n";;' \
 '  esac;;' \
 ' *"SHOW BACKUPS IN"*)' \
 '  case "${CHAIN_CASE:-pass}" in' \
 '   empty) printf "path\n";;' \
 '   paginated) printf "id\tbackup_time\nabc\t2026-09-26 10:00:00\n";;' \
 '   alternate_latest) printf "SET\npath\n2026/09/26-010000.00\n/2026/09/27-020000.00\n";;' \
 '   alternate_no_slash) printf "SET\npath\n/2026/09/26-010000.00\n2026/09/28-030000.00\n";;' \
 '   duplicate_canonical) printf "path\n/2026/09/26-010000.00\n2026/09/26-010000.00\n";;' \
 '   multiple_path) printf "path\n/2026/09/26-010000.00\t/2026/09/27-020000.00\n";;' \
 '   injected_path) printf "path\n/2026/09/27-020000.00\047; SELECT secret; --\n";;' \
 '   malformed_path) printf "path\n//2026/09/27-020000.00\n";;' \
 '   *) printf "SET\npath\n/2026/09/25-010000.00\n/2026/09/26-010000.00\n";;' \
 '  esac;;' \
 ' *"SHOW BACKUP FROM"*"check_files"*)' \
 '  expected_path=${CHAIN_EXPECTED_SQL_PATH:-/2026/09/26-010000.00}' \
 '  case "$statement" in *"SHOW BACKUP FROM '\''$expected_path'\'' IN '\''external://jandibat_backup_v1'\'' WITH check_files"*) :;; *) printf "synthetic secret in wrong path error" >&2; exit 1;; esac' \
 '  case "${CHAIN_CASE:-pass}" in' \
 '   missing_file|healthy_schedule_missing_file) printf "synthetic secret verifier-test-url external://jandibat_backup_v1 in raw SQL error" >&2; exit 1;;' \
 '   no_full) printf "backup_type\tstart_time\tend_time\tnot_future\nincremental\t2026-09-26 00:00:00\t2026-09-26 01:00:00\ttrue\n";;' \
 '   no_incremental) printf "backup_type\tstart_time\tend_time\tnot_future\nfull\tNULL\t2026-09-26 00:00:00\ttrue\n";;' \
 '   future) printf "backup_type\tstart_time\tend_time\tnot_future\nfull\tNULL\t2027-09-26 00:00:00\tfalse\n";;' \
 '   unverified_incremental) printf "backup_type\tstart_time\tend_time\tnot_future\nfull\tNULL\t2026-09-26 00:00:00\ttrue\nincremental\t2026-09-26 00:00:00\tNULL\tfalse\n";;' \
 '   gap) printf "backup_type\tstart_time\tend_time\tnot_future\nfull\tNULL\t2026-09-26 00:00:00\ttrue\nincremental\t2026-09-26 00:10:00\t2026-09-26 01:00:00\ttrue\n";;' \
 '   fractional_future) printf "SET\nbackup_type\tstart_time\tend_time\tnot_future\nfull\tNULL\t2026-09-26 00:00:00\ttrue\nincremental\t2026-09-26 00:00:00\t2026-09-26 01:00:00.5\ttrue\n";;' \
 '   *) printf "SET\nbackup_type\tstart_time\tend_time\tnot_future\nfull\tNULL\t2026-09-26 00:00:00\ttrue\nincremental\t2026-09-26 00:00:00\t2026-09-26 01:00:00\ttrue\n";;' \
 '  esac;;' \
 ' *"AS checked_at"*)' \
 '  if [ "${CHAIN_CASE:-pass}" = fractional_future ]; then printf "checked_at\n2026-09-26 01:00:00\n"; else printf "checked_at\n2026-09-26 01:01:00\n"; fi;;' \
 ' *) exit 1;;' \
 'esac' >"$test_dir/sql"
chmod 700 "$test_dir/sql"

run_case() {
 scenario=$1
 expected_sql_path=/2026/09/26-010000.00
 case "$scenario" in
  alternate_latest) expected_sql_path=/2026/09/27-020000.00;;
  alternate_no_slash) expected_sql_path=2026/09/28-030000.00;;
 esac
 if CHAIN_CASE="$scenario" CHAIN_EXPECTED_SQL_PATH="$expected_sql_path" BACKUP_VERIFIER_DATABASE_URL=verifier-test-url \
  COCKROACH_SQL_BIN="$test_dir/sql" sh "$root/scripts/db-verify-backup-chain.sh" \
  >"$test_dir/stdout" 2>"$test_dir/stderr"; then
  status=0
 else
  status=$?
 fi
}

expected='{"schemaVersion":2,"chainId":"2026.09.26-010000.00","collectionId":"jandibat_backup_v1","checkedAt":"2026-09-26T01:01:00Z","recoveryTimestamp":"2026-09-26T01:00:00Z","fileChecked":true,"passed":true,"backupPath":"2026/09/26-010000.00"}'
for scenario in pass schedule_view_denied schedule_bad_status; do
 run_case "$scenario"
 [ "$status" -eq 0 ] || fail "$scenario valid checked chain refused ($(sed -n '1p' "$test_dir/stderr"))"
 [ "$(wc -l <"$test_dir/stdout")" -eq 1 ] || fail "$scenario success is not one line"
 [ "$(sed -n '1p' "$test_dir/stdout")" = "$expected" ] || fail "$scenario eight-field v2 result contract"
 [ ! -s "$test_dir/stderr" ] || fail "$scenario success emitted stderr"
done

for scenario in alternate_latest alternate_no_slash; do
 run_case "$scenario"
 [ "$status" -eq 0 ] || fail "$scenario valid checked chain refused ($(sed -n '1p' "$test_dir/stderr"))"
 [ "$(wc -l <"$test_dir/stdout")" -eq 1 ] || fail "$scenario success is not one line"
 case "$scenario" in
  alternate_latest) expected_alternate='{"schemaVersion":2,"chainId":"2026.09.27-020000.00","collectionId":"jandibat_backup_v1","checkedAt":"2026-09-26T01:01:00Z","recoveryTimestamp":"2026-09-26T01:00:00Z","fileChecked":true,"passed":true,"backupPath":"2026/09/27-020000.00"}';;
  alternate_no_slash) expected_alternate='{"schemaVersion":2,"chainId":"2026.09.28-030000.00","collectionId":"jandibat_backup_v1","checkedAt":"2026-09-26T01:01:00Z","recoveryTimestamp":"2026-09-26T01:00:00Z","fileChecked":true,"passed":true,"backupPath":"2026/09/28-030000.00"}';;
 esac
 [ "$(sed -n '1p' "$test_dir/stdout")" = "$expected_alternate" ] || fail "$scenario selected path absent from result"
 [ ! -s "$test_dir/stderr" ] || fail "$scenario success emitted stderr"
done

for scenario in empty paginated duplicate_canonical multiple_path injected_path malformed_path missing_file healthy_schedule_missing_file no_full no_incremental future unverified_incremental gap fractional_future; do
 run_case "$scenario"
 [ "$status" -ne 0 ] || fail "$scenario accepted"
 [ ! -s "$test_dir/stdout" ] || fail "$scenario emitted stdout"
 if grep -Eq 'synthetic secret|verifier-test-url|external://' "$test_dir/stderr"; then fail "$scenario leaked raw error, DSN, or provider URI"; fi
done

echo 'backup chain verifier contract passed'
