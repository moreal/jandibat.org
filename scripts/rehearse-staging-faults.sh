#!/bin/sh
set -eu

if [ "${ALLOW_STAGING_FAULTS:-false}" != true ]; then
	echo "set ALLOW_STAGING_FAULTS=true after confirming this is an isolated non-production environment" >&2
	exit 2
fi
if [ "${APP_ENVIRONMENT:-}" != staging ]; then
	echo "APP_ENVIRONMENT must be exactly staging" >&2
	exit 2
fi

compose_file=${STAGING_COMPOSE_FILE:-deploy/staging/compose.yaml}
environment_file=${STAGING_ENV_FILE:-deploy/staging/.env.staging}
state_file=${STAGING_STATE_FILE:-deploy/staging/.current-images}
api_port=${API_PORT:-18081}

compose() {
	docker compose --env-file "$environment_file" --env-file "$state_file" -f "$compose_file" "$@"
}

wait_http() {
	url=$1
	attempt=1
	while [ "$attempt" -le 60 ]; do
		if curl --fail --silent --show-error --max-time 3 "$url" >/dev/null; then return 0; fi
		sleep 1
		attempt=$((attempt + 1))
	done
	return 1
}

wait_container() {
	service=$1
	port=$2
	attempt=1
	while [ "$attempt" -le 60 ]; do
		if compose exec -T "$service" /busybox wget --spider -q -T 3 "http://127.0.0.1:$port/readyz"; then return 0; fi
		sleep 1
		attempt=$((attempt + 1))
	done
	return 1
}

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
api_container=$(compose ps -q api)
worker_container=$(compose ps -q worker)
test -n "$api_container" && test -n "$worker_container"

# docker kill (rather than compose stop/kill) exercises the configured restart
# policy. The script never targets a database container.
docker kill --signal TERM "$api_container" >/dev/null
wait_http "http://127.0.0.1:$api_port/readyz"
api_recovered_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

docker kill --signal TERM "$worker_container" >/dev/null
wait_container worker 8081
worker_recovered_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

printf '{"startedAt":"%s","apiRecoveredAt":"%s","workerRecoveredAt":"%s","result":"passed","scope":["api-sigterm","worker-sigterm"]}\n' \
	"$started_at" "$api_recovered_at" "$worker_recovered_at"
