#!/bin/sh
set -eu

: "${API_IMAGE:?API_IMAGE is required}"
: "${RESTORE_TOOLS_IMAGE:?RESTORE_TOOLS_IMAGE is required}"
: "${WEB_IMAGE:?WEB_IMAGE is required}"
: "${BUILD_SHA:?BUILD_SHA is required}"
: "${REGION:?REGION is required}"

case "$BUILD_SHA" in
	*[!0-9a-f]*|'')
		echo "BUILD_SHA must be lowercase hexadecimal" >&2
		exit 2
		;;
esac
if [ "${#BUILD_SHA}" -ne 40 ]; then
	echo "BUILD_SHA must be a full 40-character Git commit SHA" >&2
	exit 2
fi
case "$REGION" in
	*[!A-Za-z0-9._-]*|'')
		echo "REGION contains unsupported characters" >&2
		exit 2
		;;
esac

for image in "$API_IMAGE" "$WEB_IMAGE" "$RESTORE_TOOLS_IMAGE"; do
	case "$image" in
		*[!A-Za-z0-9._:/@-]*|*@sha256:*@*)
			echo "staging image reference contains unsupported characters" >&2
			exit 2
			;;
	esac
	case "$image" in
		*@sha256:*) digest=${image##*@sha256:} ;;
		*)
			echo "staging images must use immutable sha256 digest references" >&2
			exit 2
			;;
	esac
	if [ "${#digest}" -ne 64 ]; then
		echo "staging image digest must contain 64 hexadecimal characters" >&2
		exit 2
	fi
	case "$digest" in
		*[!0-9a-f]*)
			echo "staging image digest must be lowercase hexadecimal" >&2
			exit 2
			;;
	esac
done

compose_file=${STAGING_COMPOSE_FILE:-deploy/staging/compose.yaml}
environment_file=${STAGING_ENV_FILE:-deploy/staging/.env.staging}
api_port=${API_PORT:-18081}
web_port=${WEB_PORT:-18080}
state_file=${STAGING_STATE_FILE:-deploy/staging/.current-images}
previous_file="${state_file}.previous"
rollback_file=${STAGING_ROLLBACK_STATE_FILE:-"${state_file}.rollback"}

for value in "$api_port" "$web_port"; do
	case "$value" in
		*[!0-9]*|'')
			echo "staging ports must be integers" >&2
			exit 2
			;;
	esac
done
if [ ! -f "$environment_file" ]; then
	echo "staging environment file not found: $environment_file" >&2
	exit 2
fi

compose() {
	docker compose --env-file "$environment_file" --env-file "$state_file" -f "$compose_file" "$@"
}

deploy_candidate() {
	compose --profile tools pull api worker maintenance web restore-verify || return
	compose --profile tools run --rm backup || return
	compose --profile tools run --rm backup-role-verify || return
	compose --profile tools run --rm backup-schedule || return
	if ! compose --profile tools run --rm migrate; then
		# SIGKILL cannot run the migration process trap. A separate short-lived
		# process repairs only the allowlisted schema-unlock directives before the
		# deployment aborts, so a failed rollout never leaves a table unlocked.
		compose --profile tools run --rm schema-lock-verify --repair || return
		return 1
	fi
	compose --profile tools run --rm schema-lock-verify || return
	compose --profile tools run --rm grant-runtime-roles || return
	compose up -d --remove-orphans api worker maintenance web || return
}

smoke() {
	attempt=1
	while [ "$attempt" -le 30 ]; do
		if curl --fail --silent --show-error --max-time 5 "http://127.0.0.1:$api_port/healthz" >/dev/null &&
			curl --fail --silent --show-error --max-time 5 "http://127.0.0.1:$web_port/healthz" >/dev/null &&
			compose exec -T worker /busybox wget --spider -q -T 3 http://127.0.0.1:8081/readyz &&
			compose exec -T maintenance /busybox wget --spider -q -T 3 http://127.0.0.1:8082/readyz; then
			return 0
		fi
		sleep 2
		attempt=$((attempt + 1))
	done
	return 1
}

rollback_application() {
	if [ -f "$previous_file" ]; then
		mv "$previous_file" "$state_file"
		compose pull api worker maintenance web || return 1
		compose up -d --remove-orphans api worker maintenance web || return 1
		smoke
		return
	fi
	compose stop api worker maintenance web || return 1
	rm -f "$state_file"
	return 0
}

if [ -f "$state_file" ]; then
	cp "$state_file" "$previous_file"
	cp "$state_file" "$rollback_file"
fi
umask 077
{
	printf 'API_IMAGE=%s\n' "$API_IMAGE"
	printf 'WEB_IMAGE=%s\n' "$WEB_IMAGE"
	printf 'RESTORE_TOOLS_IMAGE=%s\n' "$RESTORE_TOOLS_IMAGE"
	printf 'BUILD_SHA=%s\n' "$BUILD_SHA"
	printf 'REGION=%s\n' "$REGION"
	printf 'API_PORT=%s\n' "$api_port"
	printf 'WEB_PORT=%s\n' "$web_port"
} >"$state_file"

if ! deploy_candidate; then
	echo "staging deployment failed before smoke checks" >&2
	if rollback_application; then
		echo "previous application state was preserved" >&2
	else
		echo "automatic application rollback also failed" >&2
	fi
	exit 1
fi

if smoke; then
	echo "staging deployment passed smoke checks"
	rm -f "$previous_file"
	echo "rollback state retained at $rollback_file"
	exit 0
fi

echo "staging smoke check failed" >&2
if rollback_application; then
	if [ -f "$state_file" ]; then
		echo "application images rolled back; database remains on the forward-compatible schema" >&2
	else
		echo "no previous release was recorded; failed candidate containers were stopped" >&2
	fi
else
	echo "automatic application rollback also failed" >&2
fi
exit 1
