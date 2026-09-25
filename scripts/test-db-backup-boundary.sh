#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-backup-boundary.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM

cat >"$test_dir/cockroach" <<'FAKE_COCKROACH'
#!/bin/sh
printf '%s\n' "$@" >>"$TEST_CLI_ARGS"
env | sed 's/=.*//' >>"$TEST_CLI_ENV_KEYS"
printf '%s\n' 'fake SQL failure: backup-boundary-secret-sentinel' >&2
exit 42
FAKE_COCKROACH
chmod 700 "$test_dir/cockroach"

sentinel=backup-boundary-secret-sentinel
TEST_CLI_ARGS="$test_dir/cli-args"
TEST_CLI_ENV_KEYS="$test_dir/cli-env-keys"
export TEST_CLI_ARGS TEST_CLI_ENV_KEYS

run_retired() {
	name=$1
	shift
	: >"$TEST_CLI_ARGS"
	: >"$TEST_CLI_ENV_KEYS"
	if env COCKROACH_SQL_BIN="$test_dir/cockroach" \
		BACKUP_DATABASE_URL="postgresql://backup:$sentinel@localhost:26257/jandibat" \
		DATABASE_URL="postgresql://backup:$sentinel@localhost:26257/jandibat" \
		MIGRATION_DATABASE_URL="postgresql://migrator:$sentinel@localhost:26257/jandibat" \
		SOURCE_DATABASE_URL="postgresql://source:$sentinel@localhost:26257/jandibat" \
		RESTORE_DATABASE_URL="postgresql://restore:$sentinel@localhost:26257/jandibat" \
		BACKUP_EXTERNAL_CONNECTION=fixture \
		"$@" >"$test_dir/output" 2>&1; then
		echo "FAIL $name: obsolete entry point succeeded" >&2
		exit 1
	fi
	if ! grep -qi 'obsolete' "$test_dir/output"; then
		echo "FAIL $name: no obsolete-policy diagnostic" >&2
		exit 1
	fi
	if grep -Fq "$sentinel" "$test_dir/output" || grep -Fq "$sentinel" "$TEST_CLI_ARGS"; then
		echo "FAIL $name: secret reached output or SQL argv" >&2
		exit 1
	fi
	if [ -s "$TEST_CLI_ARGS" ] || [ -s "$TEST_CLI_ENV_KEYS" ]; then
		echo "FAIL $name: obsolete entry point called SQL (including possible DROP)" >&2
		exit 1
	fi
}

run_retired backup sh "$root/scripts/db-backup.sh"
run_retired schedule sh "$root/scripts/db-configure-backup-schedule.sh"
run_retired roles sh "$root/scripts/db-verify-backup-roles.sh"
run_retired restore sh "$root/scripts/db-restore-verify.sh"
run_retired restore-explicit env RESTORE_TARGET_DATABASE=isolated_fixture \
	RESTORE_TARGET_DISPOSITION=preserve sh "$root/scripts/db-restore-verify.sh"
run_retired restore-delete-disposition env RESTORE_TARGET_DATABASE=isolated_fixture \
	RESTORE_TARGET_DISPOSITION=delete sh "$root/scripts/db-restore-verify.sh"
run_retired restore-unsafe-target env RESTORE_TARGET_DATABASE='fixture;DROP' \
	RESTORE_TARGET_DISPOSITION=preserve sh "$root/scripts/db-restore-verify.sh"

if rg -q -- '--url=|ignore_existing_backups|AS OF SYSTEM TIME|jandibat_backup([^_A-Za-z0-9]|$)' \
	"$root/scripts/db-backup.sh" "$root/scripts/db-configure-backup-schedule.sh" \
	"$root/scripts/db-verify-backup-roles.sh" "$root/scripts/db-restore-verify.sh"; then
	echo 'FAIL: unsafe legacy SQL policy remains in an entry point' >&2
	exit 1
fi

echo 'retired backup/restore entry points fail closed without SQL or credential output'
