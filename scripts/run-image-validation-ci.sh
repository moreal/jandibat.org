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

case "$(dd if="$marker" bs=64 count=1 2>/dev/null | od -An -tu1 | awk '
	{ for (i = 1; i <= NF; i++) {
		count++; byte = $i
		if (byte == 10) { if (newline++) invalid = 1 }
		else if (newline || (byte != 45 && (byte < 97 || byte > 122))) invalid = 1
		else value = value sprintf("%c", byte)
	} }
	END { if (!invalid && count > 0 && count < 64 && length(value) > 0) print value }
')" in
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
	restore-runtime-oci-unpack) printf '%s\n' '::error::Image validation failed during restore runtime OCI unpack.' >&2 ;;
	restore-runtime-final-inventory) printf '%s\n' '::error::Image validation failed during restore runtime final inventory.' >&2 ;;
	restore-runtime-cleanup) printf '%s\n' '::error::Image validation failed during restore runtime cleanup.' >&2 ;;
	restore-runtime-sidecar-write) printf '%s\n' '::error::Image validation failed during restore runtime sidecar write.' >&2 ;;
	restore-secure) printf '%s\n' '::error::Image validation failed during restore secure client proof.' >&2 ;;
	*) printf '%s\n' '::error::Image validation failed during setup.' >&2 ;;
esac
exit "$status"
