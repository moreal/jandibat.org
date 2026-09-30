#!/bin/sh
set -eu

evidence=${1:-}
if [ -z "$evidence" ]; then
	printf '%s\n' '::error::Image validation failed during setup.' >&2
	exit 2
fi
stage_parser_module=$(dirname -- "$0")/image-validation-stage.sh
if [ ! -r "$stage_parser_module" ]; then
	printf '%s\n' '::error::Image validation failed during setup.' >&2
	exit 2
fi
# shellcheck source=scripts/image-validation-stage.sh
. "$stage_parser_module" 2>/dev/null
umask 077
if mkdir -p "$evidence" >/dev/null 2>&1; then :; else
	status=$?
	printf '%s\n' '::error::Image validation failed during setup.' >&2
	exit "$status"
fi
log="$evidence/.image-validation-output"
marker="$evidence/.image-validation-stage"
if : 2>/dev/null >"$log" && : 2>/dev/null >"$marker"; then :; else
	status=$?
	printf '%s\n' '::error::Image validation failed during setup.' >&2
	exit "$status"
fi

if IMAGE_VALIDATION_STAGE_FILE="$marker" nix develop .#images --command sh scripts/build-release-images.sh "$evidence" >"$log" 2>&1; then
	exit 0
else
	status=$?
fi

case "$(read_image_validation_stage "$marker")" in
	payload-build) printf '%s\n' '::error::Image validation failed during payload build.' >&2 ;;
	payload-rebuild) printf '%s\n' '::error::Image validation failed during payload rebuild.' >&2 ;;
	payload-compare) printf '%s\n' '::error::Image validation failed during payload comparison.' >&2 ;;
	image-smoke) printf '%s\n' '::error::Image validation failed during image smoke.' >&2 ;;
	application-import) printf '%s\n' '::error::Image validation failed during application archive import.' >&2 ;;
	restore-context) printf '%s\n' '::error::Image validation failed during restore builder/context.' >&2 ;;
	restore-build) printf '%s\n' '::error::Image validation failed during restore build.' >&2 ;;
	restore-import) printf '%s\n' '::error::Image validation failed during restore image import and scan.' >&2 ;;
	restore-payload) printf '%s\n' '::error::Image validation failed during restore payload contract.' >&2 ;;
	restore-runtime) printf '%s\n' '::error::Image validation failed during restore runtime evidence.' >&2 ;;
	restore-runtime-preflight) printf '%s\n' '::error::Image validation failed during restore runtime preflight.' >&2 ;;
	restore-runtime-scan) printf '%s\n' '::error::Image validation failed during restore runtime scan.' >&2 ;;
	restore-runtime-imported-config) printf '%s\n' '::error::Image validation failed during restore runtime imported config.' >&2 ;;
	restore-runtime-daemon-oci-copy) printf '%s\n' '::error::Image validation failed during restore runtime daemon OCI copy.' >&2 ;;
	restore-runtime-oci-scratch) printf '%s\n' '::error::Image validation failed during restore runtime OCI scratch creation.' >&2 ;;
	restore-runtime-skopeo-copy) printf '%s\n' '::error::Image validation failed during restore runtime Skopeo copy.' >&2 ;;
	restore-runtime-oci-index) printf '%s\n' '::error::Image validation failed during restore runtime OCI index.' >&2 ;;
	restore-runtime-oci-manifest) printf '%s\n' '::error::Image validation failed during restore runtime OCI manifest.' >&2 ;;
	restore-runtime-oci-config-digest) printf '%s\n' '::error::Image validation failed during restore runtime OCI config digest.' >&2 ;;
	restore-runtime-oci-config-blob) printf '%s\n' '::error::Image validation failed during restore runtime OCI config blob.' >&2 ;;
	restore-runtime-oci-unpack) printf '%s\n' '::error::Image validation failed during restore runtime OCI unpack.' >&2 ;;
	restore-runtime-final-inventory) printf '%s\n' '::error::Image validation failed during restore runtime final inventory.' >&2 ;;
	restore-runtime-cleanup) printf '%s\n' '::error::Image validation failed during restore runtime cleanup.' >&2 ;;
	restore-runtime-sidecar-write) printf '%s\n' '::error::Image validation failed during restore runtime sidecar write.' >&2 ;;
	restore-secure) printf '%s\n' '::error::Image validation failed during restore secure client proof.' >&2 ;;
	*) printf '%s\n' '::error::Image validation failed during setup.' >&2 ;;
esac
exit "$status"
