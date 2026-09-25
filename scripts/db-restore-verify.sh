#!/bin/sh
set -eu

# The retired drill must never select or remove a target implicitly. The
# isolated restore workflow will require these explicit values when added.
target=${RESTORE_TARGET_DATABASE:-}
disposition=${RESTORE_TARGET_DISPOSITION:-preserve}

case "$target" in
	''|*[!A-Za-z0-9_]*)
		echo 'obsolete restore entry point: RESTORE_TARGET_DATABASE must name an explicit safe target' >&2
		exit 2
		;;
esac
case "$disposition" in
	preserve) ;;
	*)
		echo 'obsolete restore entry point: RESTORE_TARGET_DISPOSITION must be preserve until separately approved cleanup exists' >&2
		exit 2
		;;
esac

echo 'obsolete restore entry point: isolated restore policy is required; target preserved' >&2
exit 2
