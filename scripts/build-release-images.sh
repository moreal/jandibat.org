#!/bin/sh
set -eu

evidence=${1:?evidence directory is required}
stage=setup
record_failure() {
	status=$1
	if [ "$status" -ne 0 ] && [ -n "${IMAGE_VALIDATION_STAGE_FILE:-}" ]; then
		printf '%s\n' "$stage" 2>/dev/null >"$IMAGE_VALIDATION_STAGE_FILE" || :
	fi
}
trap 'record_failure "$?"' EXIT
test "$(nix eval --impure --raw --expr builtins.currentSystem)" = x86_64-linux || {
	echo 'release image validation requires an actual x86_64-linux builder and Docker runtime' >&2
	exit 2
}
mkdir -p "$evidence"
evidence=$(CDPATH='' cd "$evidence" && pwd)

# Rebuild payload derivations (not cache lookups) in the sandbox.
# Keep substituters available to realize build inputs missing from the first closure.
for name in api worker maintenance web backup-tools; do
	stage=payload-build
	first=$(nix build --no-link --print-out-paths ".#${name}-payload")
	stage=payload-rebuild
	second=$(nix build --rebuild --option sandbox true --no-link --print-out-paths ".#${name}-payload")
	stage=payload-compare
	test "$first" = "$second"
	printf '%s %s %s\n' "$name" "$first" "$second" >>"$evidence/payload-rebuilds.txt"
done

# Includes archive contract, actual second archive build/hash comparison,
# non-root read-only runtime, /tmp fail-closed, and DB-independent liveness.
stage=image-smoke
if make images-smoke >"$evidence/.image-smoke.log" 2>&1; then :; else
	smoke_status=$?
	exit "$smoke_status"
fi
stage=application-import
for name in api worker maintenance web; do
	archive=$(nix build --no-link --print-out-paths ".#${name}-image")
	node scripts/image-release.mjs import "$name" "$archive" "$evidence"
done

# Packaging only: extract static executables from exact imported Nix image IDs.
# The pinned Cockroach tool image must not inherit either Nix store closure.
stage=restore-context
api_image_id=$(node -e 'process.stdout.write(require(process.argv[1]).imageId)' "$evidence/api.json")
maintenance_image_id=$(node -e 'process.stdout.write(require(process.argv[1]).imageId)' "$evidence/maintenance.json")
restore_context=$(mktemp -d)
restore_builder="jandibat-restore-$(basename "$restore_context")"
builder_created=false
api_extract_container=
maintenance_extract_container=
base_inspect_container=
base_inspect_dir=
cleanup() {
	status=$1
	record_failure "$status"
	if [ -n "$api_extract_container" ]; then docker rm -f "$api_extract_container" >/dev/null || :; fi
	if [ -n "$maintenance_extract_container" ]; then docker rm -f "$maintenance_extract_container" >/dev/null || :; fi
	if [ -n "$base_inspect_container" ]; then docker rm -f "$base_inspect_container" >/dev/null 2>&1 || :; fi
	if [ "$builder_created" = true ]; then
		docker buildx rm "$restore_builder" >/dev/null || :
	fi
	if [ -n "$base_inspect_dir" ]; then rm -r "$base_inspect_dir"; fi
	for directory in "$restore_context/db/migrations" "$restore_context/scripts" "$restore_context/bin" "$restore_context/applets"; do
		if [ -d "$directory" ]; then chmod u+w "$directory"; fi
	done
	rm -r "$restore_context"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
# The default docker driver cannot export this archive. Select our own
# docker-container builder explicitly without changing the user's active one.
# Pin the server; record the client and enforce the release baseline of Buildx
# >= 0.23 for OCI contexts and reproducible export.
docker buildx version >"$evidence/buildx-version.txt"
if ! awk '$2 ~ /^v[0-9]+\.[0-9]+\.[0-9]+/ { split(substr($2, 2), v, "."); ok = v[1] > 0 || v[2] >= 23 } END { exit !ok }' "$evidence/buildx-version.txt"; then
	echo 'release packaging requires Docker Buildx >= 0.23' >&2
	exit 2
fi
if docker buildx inspect "$restore_builder" >/dev/null 2>&1; then
	echo 'task builder name already exists; refusing to modify it' >&2
	exit 2
fi
# An inspect failure is not proof of absence or ownership. A failed create
# might be partial, or it might have collided with another owner's builder.
# Only a successful create returning our exact name authorizes cleanup.
if created_builder=$(docker buildx create --name "$restore_builder" --driver docker-container \
	--driver-opt image=moby/buildkit:v0.23.2@sha256:ddd1ca44b21eda906e81ab14a3d467fa6c39cd73b9a39df1196210edcb8db59e); then
	if [ "$created_builder" != "$restore_builder" ]; then
		echo 'unexpected builder name; preserving uncertain builder state' >&2
		exit 2
	fi
	builder_created=true
else
	create_status=$?
	echo "builder creation failed; preserving any uncertain state for $restore_builder" >&2
	exit "$create_status"
fi
builder_inspection=$(docker buildx inspect "$restore_builder" --bootstrap)
printf '%s\n' "$builder_inspection" | awk '
	/^Driver:[[:space:]]+/ {
		if (found++ || NF != 2) invalid = 1
		driver = $2
	}
	END {
		if (invalid || found != 1) exit 1
		print driver
	}
' >"$evidence/buildx-driver.txt"
test "$(cat "$evidence/buildx-driver.txt")" = docker-container
api_extract_container=$(docker create --entrypoint /busybox "$api_image_id")
maintenance_extract_container=$(docker create --entrypoint /busybox "$maintenance_image_id")
docker cp -L "$api_extract_container:/bin/server" "$restore_context/jandibat-api"
docker cp -L "$maintenance_extract_container:/bin/maintenance" "$restore_context/jandibat-maintenance"
static_busybox=$(nix build --no-link --print-out-paths .#restore-tools-busybox)
cp "$static_busybox/bin/busybox" "$restore_context/busybox"
backup_tools_payload=$(nix build --no-link --print-out-paths .#backup-tools-payload)
mkdir -p "$restore_context/bin"
cp "$backup_tools_payload/bin/backup-tools" "$restore_context/bin/backup-tools"
for binary in jandibat-api jandibat-maintenance busybox bin/backup-tools; do
	test -s "$restore_context/$binary" && test -f "$restore_context/$binary" && test ! -L "$restore_context/$binary"
	chmod 0555 "$restore_context/$binary"
done
mkdir "$restore_context/applets"
applets='awk chmod cp mktemp rm sed sha256sum sh tail tr'
for applet in $applets; do
	ln -s /busybox "$restore_context/applets/$applet"
	test -L "$restore_context/applets/$applet"
	test "$(readlink "$restore_context/applets/$applet")" = /busybox
done
chmod 0555 "$restore_context/applets"
mkdir -p "$restore_context/db/migrations" "$restore_context/scripts"
cp db/migrations/*.sql "$restore_context/db/migrations/"
chmod 444 "$restore_context"/db/migrations/*.sql
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh db-bootstrap-roles.sh db-verify-backup-chain.sh db-bootstrap-backup-connection.sh db-configure-backup-schedule.sh db-verify-backup-roles.sh db-observe-backup-schedule.sh; do
	cp "scripts/$file" "$restore_context/scripts/$file"
	chmod 555 "$restore_context/scripts/$file"
done
chmod 555 "$restore_context/db/migrations" "$restore_context/scripts" "$restore_context/bin"

# Inspect the exact final-stage image without executing it. Docker export
# exposes the merged rootfs, including the package-owned /bin symlink. Listing
# the archive avoids extracting base symlinks that Docker cp cannot copy.
base_image='cgr.dev/chainguard/glibc-dynamic:latest@sha256:6acf5a19a988abdaf0f3d30247561431a206034e702871442bed66a2c68cc1a2'
if ! awk -v image="$base_image" 'toupper($1) == "FROM" { final = $0 } END { exit final != "FROM " image }' deploy/restore-tools.Dockerfile; then
	echo 'restore Dockerfile final base differs from the reviewed pinned image' >&2
	exit 2
fi
base_inspect_dir=$(mktemp -d)
if created_base=$(docker create --platform linux/amd64 --entrypoint /busybox "$base_image" 2>/dev/null); then
	case "$created_base" in *[!0-9a-f]*|'') echo 'base inspection returned an invalid container ID; preserving uncertain state' >&2; exit 2;; esac
	if [ "${#created_base}" -ne 64 ]; then
		echo 'base inspection returned an invalid container ID; preserving uncertain state' >&2
		exit 2
	fi
	base_inspect_container=$created_base
else
	echo 'could not create a container for pinned base inspection; preserving uncertain state' >&2
	exit 2
fi
if ! docker export -o "$base_inspect_dir/rootfs.tar" "$base_inspect_container" >/dev/null 2>&1 ||
	! tar -tvf "$base_inspect_dir/rootfs.tar" >"$base_inspect_dir/inventory" 2>/dev/null ||
	! tar -tf "$base_inspect_dir/rootfs.tar" >"$base_inspect_dir/members" 2>/dev/null; then
	echo 'could not inventory pinned base rootfs' >&2
	exit 2
fi
awk '
	{
		type = substr($1, 1, 1)
		if (type == "l" && $(NF - 1) == "->") { path = $(NF - 2); target = $NF }
		else if (type == "d") { path = $NF; target = "" }
		else next
		sub(/^\.\//, "", path)
		if (path == "bin") { bin_count++; if (type != "l" || target != "usr/bin") invalid = 1 }
		if (path == "usr/bin/" || path == "usr/bin") { usr_bin_count++; if (type != "d") invalid = 1 }
	}
	END {
		if (invalid || bin_count != 1 || usr_bin_count != 1) {
			print "pinned base /bin or /usr/bin layout differs from reviewed APK-owned layout" > "/dev/stderr"
			exit 2
		}
	}
' "$base_inspect_dir/inventory"
awk -v applet_names="$applets" '
	BEGIN { split(applet_names, names, " "); for (i in names) applet[names[i]] = 1 }
	{
		path = $0
		sub(/^\.\//, "", path)
		if (substr(path, 1, 8) == "usr/bin/") {
			name = substr(path, 9)
			sub(/\/$/, "", name)
			if (name in applet) collision = name
		}
	}
	END {
		if (collision != "") {
			print "pinned base destination collision: /usr/bin/" collision > "/dev/stderr"
			exit 2
		}
	}
' "$base_inspect_dir/members"
stage=restore-build
docker buildx build --file deploy/restore-tools.Dockerfile --platform linux/amd64 \
	--builder "$restore_builder" \
	--build-arg SOURCE_DATE_EPOCH=1 --provenance=false \
	--output "type=docker,dest=$restore_context/restore-tools.tar,rewrite-timestamp=true" \
	"$restore_context"
stage=restore-import
node scripts/image-release.mjs import restore-tools "$restore_context/restore-tools.tar" "$evidence"
image_id=$(node -e 'process.stdout.write(require(process.argv[1]).imageId)' "$evidence/restore-tools.json")
stage=restore-payload
sh scripts/test-restore-tools-payload.sh "$image_id" "$evidence"
