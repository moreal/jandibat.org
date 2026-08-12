#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-restore-test.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

fake="$tmp/cockroach"
cat >"$fake" <<'EOF'
#!/bin/sh
statement=
for argument in "$@"; do case "$argument" in --execute=*) statement=${argument#--execute=} ;; esac; done
case "$statement" in
  *"DROP DATABASE"*|*"RESTORE DATABASE"*) exit 0 ;;
  *"SHOW BACKUP FROM LATEST"*) printf '%s\n' '2026-08-13 00:00:00+00:00' ;;
  *"extract(epoch"*) printf '%s\n' 1 ;;
  *"information_schema.tables"*) printf '%s\n' 1 ;;
  *"concat_ws"*) printf '0\t0\t0\t0\t0\t0\n' ;;
  *"schema_migrations"*) printf '%s\n' '0010:fixture,0011:fixture' ;;
  *"information_schema.table_constraints"*) [ "${FAKE_SQL_FAIL:-false}" != true ] || exit 1; printf '%s\n' inventory ;;
  *"information_schema.statistics"*) printf '%s\n' inventory ;;
  *"AS OF SYSTEM TIME"*) printf '%s\n' 0 ;;
  *"SELECT count(*)"*"_restore_"*) [ "${FAKE_COUNT_MISMATCH:-false}" != true ] || { printf '%s\n' 1; exit 0; }; printf '%s\n' 0 ;;
  *"status = 'completed'"*) printf '%s\n' 0 ;;
  *) printf '%s\n' 0 ;;
esac
EOF
chmod 700 "$fake"

common_env="BACKUP_EXTERNAL_CONNECTION=fixture RESTORE_DATABASE_URL=postgresql://restore SOURCE_DATABASE_URL=postgresql://source COCKROACH_SQL_BIN=$fake RESTORE_MAX_RPO_SECONDS=999999999 RESTORE_MAX_RTO_SECONDS=999999999"

# shellcheck disable=SC2086
output=$(env $common_env sh "$root/scripts/db-restore-verify.sh")
printf '%s' "$output" | grep -q '"result":"not-run"'
printf '%s' "$output" | grep -q '"rowCounts"'

if env $common_env RESTORE_REQUIRE_FULL_DRILL=true sh "$root/scripts/db-restore-verify.sh" >/dev/null 2>&1; then
	echo "full drill unexpectedly passed without API/audit evidence" >&2
	exit 1
fi
if env $common_env FAKE_COUNT_MISMATCH=true sh "$root/scripts/db-restore-verify.sh" >/dev/null 2>&1; then
	echo "restore row-count mismatch unexpectedly passed" >&2
	exit 1
fi
if env $common_env FAKE_SQL_FAIL=true sh "$root/scripts/db-restore-verify.sh" >/dev/null 2>&1; then
	echo "restore SQL failure was not fail-closed" >&2
	exit 1
fi

echo "restore verifier artifact, full-gate, row-count, and SQL failure fixtures passed"
