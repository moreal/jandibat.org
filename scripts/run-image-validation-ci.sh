#!/bin/sh
set -eu

evidence=${1:-}
if [ -z "$evidence" ]; then
	printf '%s\n' '::error::Image validation failed during setup.' >&2
	exit 2
fi
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

case "$(cat "$marker" 2>/dev/null)" in
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
	restore-secure) printf '%s\n' '::error::Image validation failed during restore secure client proof.' >&2 ;;
	*) printf '%s\n' '::error::Image validation failed during setup.' >&2 ;;
esac
exit "$status"
