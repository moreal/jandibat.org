#!/bin/sh
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

# The fifth image must not inherit the API store closure (and its proxy).
docker run --rm --entrypoint /busybox "$image_id" test ! -e /nix/store
docker run --rm --entrypoint /busybox "$image_id" test ! -e /bin/metrics-proxy
for binary in /jandibat-api /jandibat-maintenance /busybox; do
	docker run --rm --entrypoint /busybox "$image_id" test -f "$binary"
	docker run --rm --entrypoint /busybox "$image_id" test ! -L "$binary"
	docker run --rm --entrypoint /busybox "$image_id" test -x "$binary"
done
if ! docker run --rm --entrypoint /busybox "$image_id" test -f /workspace/bin/backup-tools; then
	echo 'restore-tools payload is missing /workspace/bin/backup-tools' >&2
	exit 1
fi
docker run --rm --entrypoint /busybox "$image_id" test ! -L /workspace/bin/backup-tools
docker run --rm --entrypoint /busybox "$image_id" test -x /workspace/bin/backup-tools
if ! docker run --rm --entrypoint /busybox "$image_id" test ! -e /workspace/apps; then
	echo 'restore-tools image contains an application source tree' >&2
	exit 1
fi
docker run --rm --entrypoint /busybox "$image_id" test ! -e /nix/store

# Match the complete image inventory so a missing or extra file cannot hide
# behind a successful digest check of the files that happen to be present.
expected=''
for file in db/migrations/*.sql; do
	test -f "$file" || { echo 'migration source inventory is empty' >&2; exit 1; }
	expected="$expected/workspace/$file
"
done
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh db-verify-backup-chain.sh db-bootstrap-backup-connection.sh db-configure-backup-schedule.sh db-verify-backup-roles.sh; do
	test -f "scripts/$file" || { echo "missing source script: $file" >&2; exit 1; }
	if ! docker run --rm --entrypoint /busybox "$image_id" test -f "/workspace/scripts/$file"; then
		echo "restore-tools payload is missing /workspace/scripts/$file" >&2
		exit 1
	fi
	expected="$expected/workspace/scripts/$file
"
done
expected="$expected/workspace/bin/backup-tools
"
actual=$(docker run --rm --entrypoint /busybox "$image_id" find /workspace -type f | sort)
expected=$(printf '%s' "$expected" | sort)
if [ "$actual" != "$expected" ]; then
	echo 'restore-tools payload inventory differs from checked-in sources' >&2
	printf 'expected:\n%s\nactual:\n%s\n' "$expected" "$actual" >&2
	exit 1
fi

for file in db/migrations/*.sql scripts/db-migrate-url.sh scripts/db-configure-runtime-roles.sh scripts/db-verify-runtime-roles.sh scripts/db-bootstrap-roles.sh scripts/db-verify-backup-chain.sh scripts/db-bootstrap-backup-connection.sh scripts/db-configure-backup-schedule.sh scripts/db-verify-backup-roles.sh; do
	source_hash=$(sha256sum "$file" | cut -d ' ' -f 1)
	image_hash=$(docker run --rm --entrypoint /busybox "$image_id" sha256sum "/workspace/$file" | cut -d ' ' -f 1)
	if [ "$source_hash" != "$image_hash" ]; then
		echo "restore-tools payload hash differs: $file" >&2
		exit 1
	fi
done
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh db-verify-backup-chain.sh db-bootstrap-backup-connection.sh db-configure-backup-schedule.sh db-verify-backup-roles.sh; do
	docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test -x "/workspace/scripts/$file"
done
docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test ! -w /workspace/db/migrations
docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test ! -w /workspace/scripts
docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test ! -w /workspace/bin
test "$(docker image inspect --format '{{.Config.User}}' "$image_id")" = 65532:65532 || {
	echo 'restore-tools image must configure non-root User 65532:65532' >&2
	exit 1
}
if docker run --rm --network none --user 65532:65532 --read-only --tmpfs /tmp \
	--env BACKUP_VERIFIED_RECORD_FILE=/tmp/verified.json \
	--env BACKUP_VERIFIER_DATABASE_URL=postgresql://fixture.invalid/fixture \
	--env BACKUP_METRICS_LISTEN_ADDR=127.0.0.1:8099 \
	--entrypoint /usr/bin/timeout "$image_id" 10s /workspace/bin/backup-tools run-verifier >/dev/null 2>&1; then
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
if docker run --rm --network none --user 65532:65532 --read-only \
	--mount "type=bind,src=$scratch_probe/record,dst=/var/run/jandibat-backup" \
	--mount "type=bind,src=$scratch_probe/fake-sql,dst=/workspace/bin/fake-sql,readonly" \
	--env BACKUP_VERIFIED_RECORD_FILE=/var/run/jandibat-backup/verified.json \
	--env BACKUP_VERIFIER_DATABASE_URL=postgresql://fixture.invalid/fixture \
	--env COCKROACH_SQL_BIN=/workspace/bin/fake-sql \
	--entrypoint /usr/bin/timeout "$image_id" 10s /workspace/bin/backup-tools check-once >/dev/null 2>&1; then
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
docker run --rm --user 65532:65532 --read-only --tmpfs /tmp --entrypoint /bin/sh "$image_id" -c 'test -w /tmp && /cockroach/cockroach version >/dev/null'
printf '%s\n' 'restore-tools payload, permissions, Cockroach CLI and non-root /tmp passed'
