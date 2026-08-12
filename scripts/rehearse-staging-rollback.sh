#!/bin/sh
set -eu

: "${API_IMAGE:?candidate API_IMAGE is required}"
: "${WEB_IMAGE:?candidate WEB_IMAGE is required}"
: "${RESTORE_TOOLS_IMAGE:?candidate RESTORE_TOOLS_IMAGE is required}"
: "${BUILD_SHA:?candidate BUILD_SHA is required}"
: "${REGION:?candidate REGION is required}"

state_file=${STAGING_STATE_FILE:-deploy/staging/.current-images}
rollback_file=${STAGING_ROLLBACK_STATE_FILE:-"${state_file}.rollback"}
deploy_script=${STAGING_DEPLOY_SCRIPT:-scripts/deploy-staging.sh}

if [ ! -f "$rollback_file" ]; then
	echo "rollback state is missing: $rollback_file (a previous successful release is required)" >&2
	exit 2
fi

read_state_value() {
	key=$1
	file=$2
	line=$(grep "^${key}=" "$file" || true)
	value=${line#*=}
	if [ -z "$value" ] || [ "$line" = "$value" ]; then
		echo "missing $key in $file" >&2
		exit 2
	fi
	case "$value" in
		*[!A-Za-z0-9._:/@-]*|*@sha256:*@*)
			echo "unsafe $key in $file" >&2
			exit 2
			;;
	esac
	printf '%s\n' "$value"
}

previous_api=$(read_state_value API_IMAGE "$rollback_file")
previous_web=$(read_state_value WEB_IMAGE "$rollback_file")
previous_build_sha=$(read_state_value BUILD_SHA "$rollback_file")
previous_region=$(read_state_value REGION "$rollback_file")
if [ "$previous_api" = "$API_IMAGE" ] && [ "$previous_web" = "$WEB_IMAGE" ]; then
	echo "rollback state is identical to the candidate; rehearsal would prove nothing" >&2
	exit 2
fi

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "rehearsal: deploying recorded previous release"
API_IMAGE=$previous_api WEB_IMAGE=$previous_web RESTORE_TOOLS_IMAGE=$RESTORE_TOOLS_IMAGE BUILD_SHA=$previous_build_sha REGION=$previous_region sh "$deploy_script"
rolled_back_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "rehearsal: re-promoting candidate release"
API_IMAGE=$API_IMAGE WEB_IMAGE=$WEB_IMAGE RESTORE_TOOLS_IMAGE=$RESTORE_TOOLS_IMAGE BUILD_SHA=$BUILD_SHA REGION=$REGION sh "$deploy_script"
finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

printf '{"startedAt":"%s","rolledBackAt":"%s","finishedAt":"%s","previousApi":"%s","previousWeb":"%s","candidateApi":"%s","candidateWeb":"%s","result":"passed"}\n' \
	"$started_at" "$rolled_back_at" "$finished_at" "$previous_api" "$previous_web" "$API_IMAGE" "$WEB_IMAGE"
