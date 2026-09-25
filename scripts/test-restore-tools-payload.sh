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

# Match the complete image inventory so a missing or extra file cannot hide
# behind a successful digest check of the files that happen to be present.
expected=''
for file in db/migrations/*.sql; do
	test -f "$file" || { echo 'migration source inventory is empty' >&2; exit 1; }
	expected="$expected/workspace/$file
"
done
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh; do
	test -f "scripts/$file" || { echo "missing source script: $file" >&2; exit 1; }
	expected="$expected/workspace/scripts/$file
"
done
actual=$(docker run --rm --entrypoint /busybox "$image_id" find /workspace/db/migrations /workspace/scripts -type f | sort)
expected=$(printf '%s' "$expected" | sort)
if [ "$actual" != "$expected" ]; then
	echo 'restore-tools payload inventory differs from checked-in sources' >&2
	printf 'expected:\n%s\nactual:\n%s\n' "$expected" "$actual" >&2
	exit 1
fi

for file in db/migrations/*.sql scripts/db-migrate-url.sh scripts/db-configure-runtime-roles.sh scripts/db-verify-runtime-roles.sh scripts/db-bootstrap-roles.sh; do
	source_hash=$(sha256sum "$file" | cut -d ' ' -f 1)
	image_hash=$(docker run --rm --entrypoint /busybox "$image_id" sha256sum "/workspace/$file" | cut -d ' ' -f 1)
	if [ "$source_hash" != "$image_hash" ]; then
		echo "restore-tools payload hash differs: $file" >&2
		exit 1
	fi
done
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh; do
	docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test -x "/workspace/scripts/$file"
done
docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test ! -w /workspace/db/migrations
docker run --rm --user 65532:65532 --read-only --entrypoint /busybox "$image_id" test ! -w /workspace/scripts
docker run --rm --user 65532:65532 --read-only --tmpfs /tmp --entrypoint /bin/sh "$image_id" -c 'test -w /tmp && /cockroach/cockroach version >/dev/null'
printf '%s\n' 'restore-tools payload, permissions, Cockroach CLI and non-root /tmp passed'
