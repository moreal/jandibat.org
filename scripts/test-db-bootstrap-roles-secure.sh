#!/bin/sh
set -eu

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
 echo 'SKIP: secure Docker bootstrap test requires x86_64 Linux' >&2
 exit 0
fi
command -v docker >/dev/null 2>&1 || { echo 'Docker is required for secure bootstrap test' >&2; exit 2; }
image='cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282'
fixture_dir=$(mktemp -d)
fixture_name="jandibat-bootstrap-$(basename "$fixture_dir")"
created=false
cleanup() {
 if [ "$created" = true ]; then docker stop "$fixture_name" >/dev/null 2>&1 || :; fi
 rm -rf "$fixture_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
mkdir "$fixture_dir/certs"
chmod 700 "$fixture_dir"
cp scripts/db-bootstrap-roles.sh "$fixture_dir/db-bootstrap-roles.sh"
docker container inspect "$fixture_name" >/dev/null 2>&1 && { echo 'fixture container name already exists' >&2; exit 2; }
docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" --entrypoint /cockroach/cockroach "$image" cert create-ca --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null
docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" --entrypoint /cockroach/cockroach "$image" cert create-node localhost 127.0.0.1 --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null
docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" --entrypoint /cockroach/cockroach "$image" cert create-client root --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null
docker run -d --name "$fixture_name" --network none \
 --mount "type=bind,src=$fixture_dir/certs,dst=/certs,readonly" \
 --entrypoint /cockroach/cockroach "$image" start-single-node \
 --certs-dir=/certs --listen-addr=127.0.0.1:26257 --http-addr=127.0.0.1:8080 >/dev/null
created=true

password_a=$(awk 'BEGIN { for (i=0;i<43;i++) printf "A" }')
password_b=$(awk 'BEGIN { for (i=0;i<43;i++) printf "B" }')
password_c=$(awk 'BEGIN { for (i=0;i<43;i++) printf "C" }')
password_d=$(awk 'BEGIN { for (i=0;i<43;i++) printf "D" }')
password_e=$(awk 'BEGIN { for (i=0;i<43;i++) printf "E" }')
root_url='postgresql://root@localhost:26257/defaultdb?sslmode=verify-full&sslrootcert=/certs/ca.crt&sslcert=/certs/client.root.crt&sslkey=/certs/client.root.key'
role_url() { printf 'postgresql://%s:%s@localhost:26257/jandibat?sslmode=verify-full&sslrootcert=/certs/ca.crt' "$1" "$2"; }
write_env() {
 api_password=$1
 umask 077
 {
  printf 'COCKROACH_ROOT_URL=%s\n' "$root_url"
  printf 'MIGRATION_DATABASE_URL=%s\n' "$(role_url jandibat_migrator "$password_a")"
  printf 'API_DATABASE_URL=%s\n' "$(role_url jandibat_api "$api_password")"
  printf 'WORKER_DATABASE_URL=%s\n' "$(role_url jandibat_worker "$password_c")"
  printf 'MAINTENANCE_DATABASE_URL=%s\n' "$(role_url jandibat_maintenance "$password_d")"
  printf 'JANDIBAT_MIGRATOR_PASSWORD=%s\n' "$password_a"
  printf 'JANDIBAT_API_PASSWORD=%s\n' "$api_password"
  printf 'JANDIBAT_WORKER_PASSWORD=%s\n' "$password_c"
  printf 'JANDIBAT_MAINTENANCE_PASSWORD=%s\n' "$password_d"
 } >"$fixture_dir/roles.env"
}
client() {
 docker run --rm --network "container:$fixture_name" \
  --env-file "$fixture_dir/roles.env" \
  --mount "type=bind,src=$fixture_dir/certs,dst=/certs,readonly" \
  --mount "type=bind,src=$fixture_dir/db-bootstrap-roles.sh,dst=/bootstrap.sh,readonly" \
  --entrypoint /bin/sh "$image" "$@"
}
write_env "$password_b"
ready=false
for attempt in 1 2 3 4 5 6 7 8 9 10; do
 if printf 'SELECT 1;\n' | client -c 'COCKROACH_URL="$COCKROACH_ROOT_URL" /cockroach/cockroach sql --set=errexit=true' >"$fixture_dir/probe" 2>&1; then ready=true; break; fi
 sleep 1
done
[ "$ready" = true ] || { echo 'secure fixture did not become ready' >&2; exit 1; }
for run in 1 2; do
 if ! client /bootstrap.sh >"$fixture_dir/bootstrap-output" 2>&1; then echo 'bootstrap failed in secure fixture (redacted)' >&2; exit 1; fi
done
printf 'SELECT count(*) FROM [SHOW DATABASES] WHERE database_name = '\''jandibat'\'';\n' | client -c 'COCKROACH_URL="$COCKROACH_ROOT_URL" /cockroach/cockroach sql --format=tsv --set=errexit=true' >"$fixture_dir/count" 2>&1
[ "$(tail -n 1 "$fixture_dir/count" | tr -d '\r')" = 1 ] || { echo 'expected one database' >&2; exit 1; }
printf 'SHOW USERS;\n' | client -c 'COCKROACH_URL="$COCKROACH_ROOT_URL" /cockroach/cockroach sql --format=tsv --set=errexit=true' >"$fixture_dir/users" 2>&1
for role in jandibat_migrator jandibat_api jandibat_worker jandibat_maintenance; do
 awk -F '\t' -v role="$role" '$1 == role && $2 !~ /NOLOGIN/ { found++ } END { exit found != 1 }' "$fixture_dir/users" || { echo 'expected one LOGIN account for each role' >&2; exit 1; }
done
write_env "$password_e"
if ! client /bootstrap.sh >"$fixture_dir/bootstrap-output" 2>&1; then echo 'password rotation failed in secure fixture (redacted)' >&2; exit 1; fi
printf 'SELECT 1;\n' | client -c 'COCKROACH_URL="$API_DATABASE_URL" /cockroach/cockroach sql --set=errexit=true' >"$fixture_dir/probe" 2>&1 || { echo 'rotated credential rejected' >&2; exit 1; }
write_env "$password_b"
if printf 'SELECT 1;\n' | client -c 'COCKROACH_URL="$API_DATABASE_URL" /cockroach/cockroach sql --set=errexit=true' >"$fixture_dir/probe" 2>&1; then echo 'old credential accepted after rotation' >&2; exit 1; fi
echo 'secure bootstrap idempotence and password rotation checks passed'
