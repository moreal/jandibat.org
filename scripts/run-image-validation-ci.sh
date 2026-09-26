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
	packaging) printf '%s\n' '::error::Image validation failed during packaging.' >&2 ;;
	*) printf '%s\n' '::error::Image validation failed during setup.' >&2 ;;
esac
exit "$status"
