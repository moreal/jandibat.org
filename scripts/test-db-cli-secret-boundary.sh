#!/bin/sh
set -eu

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-cli-secret-boundary.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM
mkdir "$temporary_directory/bin" "$temporary_directory/migrations"

cat >"$temporary_directory/bin/cockroach" <<'FAKE_COCKROACH'
#!/bin/sh
set -eu
for arg in "$@"; do
	case "$arg" in
		--url*) echo argv-url >>"$TEST_EVENTS"; exit 91 ;;
	esac
done
if [ -z "${COCKROACH_URL:-}" ]; then
	echo missing-url >>"$TEST_EVENTS"
	exit 92
fi
case "$COCKROACH_URL" in
	"$TEST_MIGRATION_URL"|"$TEST_API_URL"|"$TEST_WORKER_URL"|"$TEST_MAINTENANCE_URL") ;;
	*) echo wrong-url >>"$TEST_EVENTS"; exit 93 ;;
esac
[ "${1:-}" = sql ] || { echo wrong-command >>"$TEST_EVENTS"; exit 94; }

statement=
for arg in "$@"; do
	case "$arg" in --execute=*) statement=${arg#--execute=} ;; esac
done
case "$statement" in
	*'SELECT current_database()'*) printf 'current_database\njandibat\n'; exit 0 ;;
	'SHOW USERS') printf 'username\toptions\njandibat_migrator\tLOGIN\njandibat_api\tLOGIN\njandibat_worker\tLOGIN\njandibat_maintenance\tLOGIN\n'; exit 0 ;;
	*'SHOW GRANTS ON SCHEMA public'*)
		[ "$COCKROACH_URL" = "$TEST_MIGRATION_URL" ] || { echo wrong-grants-url >>"$TEST_EVENTS"; exit 95; }
		printf 'database_name\tschema_name\tgrantee\tprivilege_type\njandibat\tpublic\tjandibat_migrator\tCREATE\n'
		exit 0 ;;
	*'SELECT COALESCE(max(checksum)'*) printf 'checksum\n\n'; exit 0 ;;
esac

if [ "${TEST_MODE:-}" = verify ]; then
	count=0
	[ ! -f "$TEST_COUNT" ] || count=$(sed -n '1p' "$TEST_COUNT")
	count=$((count + 1))
	printf '%s\n' "$count" >"$TEST_COUNT"
	if grep -qx "$count" "$TEST_DENIAL_CALLS"; then exit 1; fi
fi

case "$statement" in
	*'ALTER TABLE migration_schema_lock_probe SET (schema_locked = false)'*) echo unlock >>"$TEST_EVENTS" ;;
	*'ALTER TABLE migration_schema_lock_probe SET (schema_locked = true)'*) echo relock >>"$TEST_EVENTS" ;;
esac
if [ "${TEST_MODE:-}" = migration_failure ] && [ -z "$statement" ]; then
	echo migration-failed >>"$TEST_EVENTS"
	exit 1
fi
exit 0
FAKE_COCKROACH
chmod +x "$temporary_directory/bin/cockroach"

printf '%s\n' 'SELECT 1;' >"$temporary_directory/migrations/0002_probe.sql"
canary='cli-boundary-canary-password'
TEST_MIGRATION_URL="postgresql://migrator:$canary@localhost:26257/jandibat?sslmode=disable"
TEST_API_URL="postgresql://api:$canary@localhost:26257/jandibat?sslmode=disable"
TEST_WORKER_URL="postgresql://worker:$canary@localhost:26257/jandibat?sslmode=disable"
TEST_MAINTENANCE_URL="postgresql://maintenance:$canary@localhost:26257/jandibat?sslmode=disable"
TEST_EVENTS="$temporary_directory/events"
TEST_COUNT="$temporary_directory/count"
TEST_DENIAL_CALLS="$temporary_directory/denial-calls"
awk '/^sql "/ { call++ } /^expect_denied "/ { call++; print call }' scripts/db-verify-runtime-roles.sh >"$TEST_DENIAL_CALLS"
export TEST_MIGRATION_URL TEST_API_URL TEST_WORKER_URL TEST_MAINTENANCE_URL TEST_EVENTS TEST_COUNT TEST_DENIAL_CALLS
PATH="$temporary_directory/bin:$PATH"
export PATH

run_success() {
	name=$1
	shift
	: >"$TEST_EVENTS"
	if ! "$@" >"$temporary_directory/output" 2>&1; then
		if [ -s "$TEST_EVENTS" ]; then
			echo "FAIL $name: fake CLI rejected a child process (redacted): $(sed -n '1p' "$TEST_EVENTS")" >&2
		else
			echo "FAIL $name: script exited unsuccessfully (redacted)" >&2
		fi
		exit 1
	fi
	if grep -Fq "$canary" "$temporary_directory/output"; then
		echo "FAIL $name: script printed the canary (redacted)" >&2
		exit 1
	fi
}

run_missing() {
	name=$1
	shift
	: >"$TEST_EVENTS"
	if "$@" >"$temporary_directory/output" 2>&1; then
		echo "FAIL $name: missing URL was accepted (redacted)" >&2
		exit 1
	fi
	if [ -s "$TEST_EVENTS" ] || grep -Fq "$canary" "$temporary_directory/output"; then
		echo "FAIL $name: unexpected child call or secret output (redacted)" >&2
		exit 1
	fi
}

run_missing migrate env -u MIGRATION_DATABASE_URL COCKROACH_DATABASE=jandibat MIGRATIONS_DIR="$temporary_directory/migrations" sh scripts/db-migrate-url.sh
run_success migrate env MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" COCKROACH_DATABASE=jandibat MIGRATIONS_DIR="$temporary_directory/migrations" sh scripts/db-migrate-url.sh
run_missing configure env -u MIGRATION_DATABASE_URL -u DATABASE_URL sh scripts/db-configure-runtime-roles.sh
run_success configure env -u MIGRATION_DATABASE_URL DATABASE_URL="$TEST_MIGRATION_URL" sh scripts/db-configure-runtime-roles.sh
run_success configure-migration-url env MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" sh scripts/db-configure-runtime-roles.sh
run_missing verify env -u MIGRATION_DATABASE_URL API_DATABASE_URL="$TEST_API_URL" WORKER_DATABASE_URL="$TEST_WORKER_URL" MAINTENANCE_DATABASE_URL="$TEST_MAINTENANCE_URL" sh scripts/db-verify-runtime-roles.sh
run_missing verify-api env -u API_DATABASE_URL MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" WORKER_DATABASE_URL="$TEST_WORKER_URL" MAINTENANCE_DATABASE_URL="$TEST_MAINTENANCE_URL" sh scripts/db-verify-runtime-roles.sh
run_missing verify-worker env -u WORKER_DATABASE_URL MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" API_DATABASE_URL="$TEST_API_URL" MAINTENANCE_DATABASE_URL="$TEST_MAINTENANCE_URL" sh scripts/db-verify-runtime-roles.sh
run_missing verify-maintenance env -u MAINTENANCE_DATABASE_URL MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" API_DATABASE_URL="$TEST_API_URL" WORKER_DATABASE_URL="$TEST_WORKER_URL" sh scripts/db-verify-runtime-roles.sh
TEST_MODE=verify
export TEST_MODE
rm -f "$TEST_COUNT"
run_success verify env MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" API_DATABASE_URL="$TEST_API_URL" WORKER_DATABASE_URL="$TEST_WORKER_URL" MAINTENANCE_DATABASE_URL="$TEST_MAINTENANCE_URL" sh scripts/db-verify-runtime-roles.sh
unset TEST_MODE

printf '%s\n' '-- jandibat:schema-unlock migration_schema_lock_probe' 'SELECT 1;' >"$temporary_directory/migrations/0002_probe.sql"
TEST_MODE=migration_failure
export TEST_MODE
: >"$TEST_EVENTS"
if env MIGRATION_DATABASE_URL="$TEST_MIGRATION_URL" COCKROACH_DATABASE=jandibat MIGRATIONS_DIR="$temporary_directory/migrations" sh scripts/db-migrate-url.sh >"$temporary_directory/output" 2>&1; then
	echo 'FAIL migrate-failure: failed transaction was accepted (redacted)' >&2
	exit 1
fi
if ! grep -q '^unlock$' "$TEST_EVENTS" || ! grep -q '^migration-failed$' "$TEST_EVENTS" || ! grep -q '^relock$' "$TEST_EVENTS"; then
	echo 'FAIL migrate-failure: unlock/failure/relock path incomplete (redacted)' >&2
	exit 1
fi
if grep -Fq "$canary" "$temporary_directory/output"; then
	echo 'FAIL migrate-failure: script printed the canary (redacted)' >&2
	exit 1
fi

echo 'Cockroach CLI secret boundary checks passed (redacted)'
