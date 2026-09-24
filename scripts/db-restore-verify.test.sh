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
  *"information_schema.tables"*)
    if [ -n "${FAKE_MISSING_TABLE:-}" ]; then
      case "$statement" in *"table_name = '$FAKE_MISSING_TABLE'"*) printf '%s\n' 0; exit 0 ;; esac
    fi
    printf '%s\n' 1 ;;
  *"concat_ws"*) printf '0\t0\t0\t0\t0\t0\n' ;;
  *"schema_migrations"*) printf '%s\n' '0010:fixture,0011:fixture' ;;
  *"information_schema.table_constraints"*) [ "${FAKE_SQL_FAIL:-false}" != true ] || exit 1; printf '%s\n' inventory ;;
  *"information_schema.statistics"*) printf '%s\n' inventory ;;
  *"AS OF SYSTEM TIME"*) printf '%s\n' 0 ;;
  *"SELECT count(*)"*"_restore_"*)
    if [ "${FAKE_COUNT_MISMATCH:-false}" = true ]; then printf '%s\n' 1; exit 0; fi
    if [ -n "${FAKE_MISMATCH_TABLE:-}" ]; then
      case "$statement" in *".public.$FAKE_MISMATCH_TABLE"*) printf '%s\n' 1; exit 0 ;; esac
    fi
    printf '%s\n' 0 ;;
  *"status = 'completed'"*) printf '%s\n' 0 ;;
  *) printf '%s\n' 0 ;;
esac
EOF
chmod 700 "$fake"

http="$tmp/http"
cat >"$http" <<'EOF'
#!/bin/sh
output=/dev/null
previous=
for argument in "$@"; do
  if [ "$previous" = -O ] || [ "$previous" = --output ]; then output=$argument; fi
  previous=$argument
done
if [ "${0##*/}" = curl ]; then
  case "$*" in *" wget "*|*"--post-data="*) exit 1 ;; esac
  case "$*" in *"--fail"*"--max-time 5"*) ;; *) exit 1 ;; esac
  case "$*" in *"/graphql"*)
    case "$*" in *"--data-binary "*"RestoreSnapshot"*) ;; *) exit 1 ;; esac ;;
  esac
fi
case "$*" in
  *"/readyz"*|*"/v1/render/restore-fixture.svg"*) exit 0 ;;
  *"/graphql"*)
    case "$*" in
      *"RestoreSnapshot"*"restore-fixture"*)
        if [ "${FAKE_GRAPHQL_ERROR:-false}" = true ]; then
          printf '%s\n' '{"errors":[{"message":"fixture failure"}],"data":{"subject":null}}' >"$output"
        else
          printf '%s\n' '{"data":{"subject":{"handle":"restore-fixture","activitySnapshot":{"revision":"fixture-revision","generatedAt":"2026-08-13T00:00:00Z","dataUpdatedAt":null}}}}' >"$output"
        fi
        exit 0 ;;
    esac ;;
esac
exit 1
EOF
chmod 700 "$http"
ln -s "$http" "$tmp/curl"

common_env="BACKUP_EXTERNAL_CONNECTION=fixture RESTORE_DATABASE_URL=postgresql://restore SOURCE_DATABASE_URL=postgresql://source COCKROACH_SQL_BIN=$fake RESTORE_MAX_RPO_SECONDS=999999999 RESTORE_MAX_RTO_SECONDS=999999999"

# shellcheck disable=SC2086
output=$(env $common_env sh "$root/scripts/db-restore-verify.sh")
printf '%s' "$output" | grep -q '"result":"not-run"'
printf '%s' "$output" | grep -q '"rowCounts"'

for table in custom_provider_secrets activity_snapshot_changes; do
  if missing_result=$(env $common_env FAKE_MISSING_TABLE="$table" sh "$root/scripts/db-restore-verify.sh" 2>&1); then
    echo "missing $table unexpectedly passed restore verification" >&2
    exit 1
  fi
  case "$missing_result" in *"restore is missing expected table: $table"*) ;; *) echo "missing $table did not fail at the table inventory" >&2; exit 1 ;; esac

  if mismatch_result=$(env $common_env FAKE_MISMATCH_TABLE="$table" sh "$root/scripts/db-restore-verify.sh" 2>&1); then
    echo "row-count mismatch for $table unexpectedly passed restore verification" >&2
    exit 1
  fi
  case "$mismatch_result" in *"restore row count differs from backup-time source for $table"*) ;; *) echo "row-count mismatch for $table did not fail at the count comparison" >&2; exit 1 ;; esac
done
for table in custom_provider_secrets activity_snapshot_changes; do
  printf '%s' "$output" | grep -Fq "\"table\":\"$table\""
done

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

smoke_output=$(env $common_env RESTORE_API_BASE_URL=http://127.0.0.1:18083 RESTORE_FIXTURE_SUBJECT=restore-fixture RESTORE_HTTP_BIN="$http" sh "$root/scripts/db-restore-verify.sh")
printf '%s' "$smoke_output" | grep -q '"apiSmoke":"passed"'
default_smoke_output=$(env PATH="$tmp:$PATH" $common_env RESTORE_API_BASE_URL=http://127.0.0.1:18083 RESTORE_FIXTURE_SUBJECT=restore-fixture sh "$root/scripts/db-restore-verify.sh")
printf '%s' "$default_smoke_output" | grep -q '"apiSmoke":"passed"'
if env $common_env RESTORE_API_BASE_URL=http://127.0.0.1:18083 RESTORE_FIXTURE_SUBJECT=restore-fixture RESTORE_HTTP_BIN="$http" FAKE_GRAPHQL_ERROR=true sh "$root/scripts/db-restore-verify.sh" | grep -q '"apiSmoke":"passed"'; then
	echo "GraphQL errors unexpectedly passed restore API smoke" >&2
	exit 1
fi

echo "restore verifier artifact, GraphQL API smoke, full-gate, row-count, and SQL failure fixtures passed"
