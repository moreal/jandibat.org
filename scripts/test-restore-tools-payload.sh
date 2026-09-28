#!/bin/sh
# shellcheck disable=SC2016 # The quoted program expands inside the image.
set -eu

image_id=${1:?imported restore-tools image ID is required}
case "$image_id" in
	sha256:*)
		hex=${image_id#sha256:}
		case "$hex" in *[!0-9a-f]*) echo 'expected an imported sha256 image ID' >&2; exit 2;; esac
		[ "${#hex}" -eq 64 ] || { echo 'expected an imported sha256 image ID' >&2; exit 2; }
		;;
	*) echo 'expected an imported sha256 image ID' >&2; exit 2 ;;
esac

# All reads are bound to the imported image ID. Docker diagnostics may contain
# daemon configuration, so only fixed contract failures reach the caller.
image_busybox() { docker run --rm --entrypoint /busybox "$image_id" "$@" 2>/dev/null; }
require_test() {
	label=$1
	shift
	if ! image_busybox test "$@" >/dev/null; then
		echo "restore-tools payload check failed: $label" >&2
		exit 1
	fi
}
require_mode() {
	path=$1
	want=$2
	if ! got=$(image_busybox stat -c %a "$path") || [ "$got" != "$want" ]; then
		echo "restore-tools payload mode differs: $path" >&2
		exit 1
	fi
}
require_directory_mode() {
	path=$1
	got=$(image_busybox stat -c %a "$path" 2>/dev/null) || got=''
	case "$got" in
		555|755) ;;
		*) echo "restore-tools payload mode differs: $path" >&2; exit 1 ;;
	esac
	if [ "$(image_busybox stat -c %u:%g "$path" 2>/dev/null)" != 0:0 ]; then
		echo "restore-tools payload owner differs: $path" >&2
		exit 1
	fi
}
require_test 'Nix store' ! -e /nix/store
require_test 'metrics proxy' ! -e /bin/metrics-proxy
for binary in /jandibat-api /jandibat-maintenance /busybox /workspace/bin/backup-tools; do
	require_test "$binary" -f "$binary"
	require_test "$binary" ! -L "$binary"
	require_test "$binary" -x "$binary"
	require_mode "$binary" 555
done
require_test '/cockroach/cockroach' -f /cockroach/cockroach
require_test '/cockroach/cockroach' ! -L /cockroach/cockroach
require_test '/cockroach/cockroach' -x /cockroach/cockroach
require_test 'source tree' ! -e /workspace/apps

# The base's package database is a release identity, not disposable metadata.
apk_db=/usr/lib/apk/db/installed
require_test 'APK database' -f "$apk_db"
require_test 'APK database' ! -L "$apk_db"
apk_hash=$(image_busybox sha256sum "$apk_db" 2>/dev/null | cut -d ' ' -f 1) || {
	echo 'restore-tools APK database cannot be hashed' >&2
	exit 1
}
if [ "$apk_hash" != 1ab604c045cac5dba49efa80d7baed9b083f7f9821074e0b572ac7b9ccc7d4f8 ]; then
	echo 'restore-tools APK database hash differs' >&2
	exit 1
fi
require_test 'CA bundle' -f /etc/ssl/certs/ca-certificates.crt
require_test 'CA bundle' -s /etc/ssl/certs/ca-certificates.crt
require_test 'CA bundle' -r /etc/ssl/certs/ca-certificates.crt
if [ "$(image_busybox readlink /bin)" != usr/bin ]; then
	echo 'restore-tools /bin link differs from APK base' >&2
	exit 1
fi
for applet in awk chmod cp mktemp rm sed sha256sum sh tail tr; do
	require_test "$applet applet" -L "/usr/bin/$applet"
	if [ "$(image_busybox readlink "/usr/bin/$applet")" != /busybox ]; then
		echo "restore-tools BusyBox applet differs: $applet" >&2
		exit 1
	fi
done

# Match the complete image inventory so a missing or extra file cannot hide
# behind a successful digest check of the files that happen to be present.
expected='/workspace
/workspace/bin
/workspace/db
/workspace/db/migrations
/workspace/scripts
'
for file in db/migrations/*.sql; do
	test -f "$file" || { echo 'migration source inventory is empty' >&2; exit 1; }
	expected="$expected/workspace/$file
"
done
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh db-verify-backup-chain.sh db-bootstrap-backup-connection.sh db-configure-backup-schedule.sh db-verify-backup-roles.sh db-observe-backup-schedule.sh; do
	test -f "scripts/$file" || { echo "missing source script: $file" >&2; exit 1; }
	require_test "/workspace/scripts/$file" -f "/workspace/scripts/$file"
	expected="$expected/workspace/scripts/$file
"
done
expected="$expected/workspace/bin/backup-tools
"
actual=$(image_busybox find /workspace | sort)
expected=$(printf '%s' "$expected" | sort)
if [ "$actual" != "$expected" ]; then
	echo 'restore-tools payload inventory differs from checked-in sources' >&2
	exit 1
fi

for directory in /workspace /workspace/bin /workspace/db /workspace/db/migrations /workspace/scripts; do
	require_test "$directory" -d "$directory"
	require_directory_mode "$directory"
done
for file in db/migrations/*.sql; do
	require_test "/workspace/$file" -f "/workspace/$file"
	require_test "/workspace/$file" ! -L "/workspace/$file"
	require_mode "/workspace/$file" 444
done

for file in db/migrations/*.sql scripts/db-migrate-url.sh scripts/db-configure-runtime-roles.sh scripts/db-verify-runtime-roles.sh scripts/db-bootstrap-roles.sh scripts/db-verify-backup-chain.sh scripts/db-bootstrap-backup-connection.sh scripts/db-configure-backup-schedule.sh scripts/db-verify-backup-roles.sh scripts/db-observe-backup-schedule.sh; do
	source_hash=$(sha256sum "$file" | cut -d ' ' -f 1)
	image_hash=$(image_busybox sha256sum "/workspace/$file" | cut -d ' ' -f 1)
	if [ "$source_hash" != "$image_hash" ]; then
		echo "restore-tools payload hash differs: $file" >&2
		exit 1
	fi
done
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh db-verify-backup-chain.sh db-bootstrap-backup-connection.sh db-configure-backup-schedule.sh db-verify-backup-roles.sh db-observe-backup-schedule.sh; do
	require_test "/workspace/scripts/$file" -f "/workspace/scripts/$file"
	require_test "/workspace/scripts/$file" ! -L "/workspace/scripts/$file"
	require_mode "/workspace/scripts/$file" 555
done
if [ "$(docker image inspect --format '{{.Config.User}}' "$image_id" 2>/dev/null)" != 65532:65532 ]; then
	echo 'restore-tools image must configure non-root User 65532:65532' >&2
	exit 1
fi

# Both configured and explicit identities must load the shell and native CLI
# under the same restricted container settings used by workloads. The resolver
# file check is a prerequisite only; hostname DNS is proved by the later
# secure Cockroach fixture.
restricted_run() {
	identity=$1
	shift
	if [ "$identity" = default ]; then
		docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges "$@"
	else
		docker run --rm --network none --user 65532:65532 --read-only --cap-drop ALL --security-opt no-new-privileges "$@"
	fi
}
for identity in default explicit; do
	if ! restricted_run "$identity" --entrypoint /cockroach/cockroach "$image_id" version >/dev/null 2>&1 ||
		! restricted_run "$identity" --entrypoint /bin/sh "$image_id" -c 'test "$SSL_CERT_FILE" = /etc/ssl/certs/ca-certificates.crt && test -r "$SSL_CERT_FILE" && test -r /etc/resolv.conf' >/dev/null 2>&1; then
		echo "restore-tools restricted $identity CLI or shell failed" >&2
		exit 1
	fi
done
for identity in default explicit; do
	if ! restricted_run "$identity" --entrypoint /bin/sh "$image_id" -c 'if ( : >/tmp/restore-tools-write-probe ) 2>/dev/null; then exit 1; fi' >/dev/null 2>&1; then
		echo "restore-tools /tmp write succeeded without tmpfs for $identity user" >&2
		exit 1
	fi
	if ! restricted_run "$identity" --tmpfs /tmp --entrypoint /bin/sh "$image_id" -c ': >/tmp/restore-tools-write-probe' >/dev/null 2>&1; then
		echo "restore-tools /tmp write failed with tmpfs for $identity user" >&2
		exit 1
	fi
done
if docker run --rm --network none --user 65532:65532 --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
	--env BACKUP_VERIFIED_RECORD_FILE=/tmp/verified.json \
	--env BACKUP_VERIFIER_DATABASE_URL=postgresql://fixture.invalid/fixture \
	--env BACKUP_METRICS_LISTEN_ADDR=127.0.0.1:8099 \
	--entrypoint /busybox "$image_id" timeout 10s /workspace/bin/backup-tools run-verifier >/dev/null 2>&1; then
	echo 'backup verifier started without its metrics token' >&2
	exit 1
else
	status=$?
	if [ "$status" -ne 1 ]; then
		echo 'backup verifier missing-token smoke did not exit cleanly' >&2
		exit 1
	fi
fi
# Production mounts only the verifier record emptyDir. Prove that the
# packaged checker reaches its SQL client with a read-only root and no /tmp
# tmpfs: its private capture directory must live beside the record.
scratch_probe=$(mktemp -d)
trap 'rm -r "$scratch_probe"' EXIT
mkdir "$scratch_probe/record"
chmod 0777 "$scratch_probe/record"
printf '%s\n' '#!/bin/sh' 'printf reached > /var/run/jandibat-backup/sql-reached' 'exit 1' >"$scratch_probe/fake-sql"
chmod 0555 "$scratch_probe/fake-sql"
if docker run --rm --network none --user 65532:65532 --read-only --cap-drop ALL --security-opt no-new-privileges \
	--mount "type=bind,src=$scratch_probe/record,dst=/var/run/jandibat-backup" \
	--mount "type=bind,src=$scratch_probe/fake-sql,dst=/workspace/bin/fake-sql,readonly" \
	--env BACKUP_VERIFIED_RECORD_FILE=/var/run/jandibat-backup/verified.json \
	--env BACKUP_VERIFIER_DATABASE_URL=postgresql://fixture.invalid/fixture \
	--env COCKROACH_SQL_BIN=/workspace/bin/fake-sql \
	--entrypoint /busybox "$image_id" timeout 10s /workspace/bin/backup-tools check-once >/dev/null 2>&1; then
	echo 'backup checker unexpectedly accepted synthetic SQL failure' >&2
	exit 1
else
	status=$?
	if [ "$status" -ne 1 ]; then
		echo 'backup checker scratch smoke did not exit cleanly' >&2
		exit 1
	fi
fi
test -f "$scratch_probe/record/sql-reached" || {
	echo 'backup checker did not reach SQL with only record storage writable' >&2
	exit 1
}
printf '%s\n' 'restore-tools payload, permissions, Cockroach CLI and non-root /tmp passed'
