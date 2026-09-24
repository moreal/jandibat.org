#!/bin/sh
set -eu

evidence=${1:?evidence directory is required}
test "$(nix eval --impure --raw --expr builtins.currentSystem)" = x86_64-linux || {
	echo 'release image validation requires an actual x86_64-linux builder and Docker runtime' >&2
	exit 2
}
mkdir -p "$evidence"
evidence=$(CDPATH='' cd "$evidence" && pwd)

# Rebuild payload derivations (not cache lookups) with sandbox networking denied.
# --offline also forbids fetching a dependency omitted from the first closure.
for name in api worker maintenance web; do
	first=$(nix build --no-link --print-out-paths ".#${name}-payload")
	second=$(nix build --offline --rebuild --option sandbox true --no-link --print-out-paths ".#${name}-payload")
	test "$first" = "$second"
	printf '%s %s %s\n' "$name" "$first" "$second" >>"$evidence/payload-rebuilds.txt"
done

# Includes archive contract, actual second archive build/hash comparison,
# non-root read-only runtime, /tmp fail-closed, and DB-independent liveness.
make images-smoke >"$evidence/image-smoke.log" 2>&1 || {
	cat "$evidence/image-smoke.log" >&2
	exit 1
}
for name in api worker maintenance web; do
	archive=$(nix build --no-link --print-out-paths ".#${name}-image")
	node scripts/image-release.mjs import "$name" "$archive" "$evidence"
done

# Packaging only: Nix-built API/maintenance and their runtime closure are copied
# into the pinned Cockroach tool image. No application source is in this context.
# BuildKit named contexts bind FROM to exact local archive contents, not tags.
api_archive=$(nix build --no-link --print-out-paths .#api-image)
maintenance_archive=$(nix build --no-link --print-out-paths .#maintenance-image)
restore_context=$(mktemp -d)
restore_builder="jandibat-restore-$(basename "$restore_context")"
builder_created=false
cleanup() {
	if [ "$builder_created" = true ]; then
		docker buildx rm "$restore_builder" >/dev/null || :
	fi
	for directory in "$restore_context/db/migrations" "$restore_context/scripts"; do
		if [ -d "$directory" ]; then chmod u+w "$directory"; fi
	done
	rm -r "$restore_context"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
# The default docker driver cannot export this archive. Select our own
# docker-container builder explicitly without changing the user's active one.
# Pin the server; record the client and enforce the release baseline of Buildx
# >= 0.23 for OCI contexts, formatted driver inspection and reproducible export.
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
docker buildx inspect "$restore_builder" --bootstrap --format '{{.Driver}}' >"$evidence/buildx-driver.txt"
test "$(cat "$evidence/buildx-driver.txt")" = docker-container
skopeo copy "docker-archive:$api_archive" "oci:$restore_context/api:release"
skopeo copy "docker-archive:$maintenance_archive" "oci:$restore_context/maintenance:release"
mkdir -p "$restore_context/db/migrations" "$restore_context/scripts"
cp db/migrations/*.sql "$restore_context/db/migrations/"
chmod 444 "$restore_context"/db/migrations/*.sql
for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh; do
	cp "scripts/$file" "$restore_context/scripts/$file"
	chmod 555 "$restore_context/scripts/$file"
done
chmod 555 "$restore_context/db/migrations" "$restore_context/scripts"
docker buildx build --file deploy/restore-tools.Dockerfile --platform linux/amd64 \
	--builder "$restore_builder" \
	--build-context "api=oci-layout://$restore_context/api:release" \
	--build-context "maintenance=oci-layout://$restore_context/maintenance:release" \
	--build-arg SOURCE_DATE_EPOCH=1 --provenance=false \
	--output "type=docker,dest=$restore_context/restore-tools.tar,rewrite-timestamp=true" \
	"$restore_context"
node scripts/image-release.mjs import restore-tools "$restore_context/restore-tools.tar" "$evidence"
image_id=$(node -e 'process.stdout.write(require(process.argv[1]).imageId)' "$evidence/restore-tools.json")
sh scripts/test-restore-tools-payload.sh "$image_id"
