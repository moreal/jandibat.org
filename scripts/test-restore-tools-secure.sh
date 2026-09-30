#!/bin/sh
# shellcheck disable=SC2016 # Quoted programs expand inside the packaged image.
set -eu

invalid() { echo 'restore-tools secure proof requires an immutable image ID and evidence directory' >&2; exit 2; }
[ "$#" -eq 2 ] || invalid
image_id=$1
case "$image_id" in sha256:*) hex=${image_id#sha256:};; *) invalid;; esac
case "$hex" in ''|*[!0-9a-f]*) invalid;; esac
[ "${#hex}" -eq 64 ] || invalid
[ -n "$2" ] || invalid
umask 077
evidence=$2
# Keep incidental host/Docker diagnostics private too. FD 3 is reserved for
# our fixed redacted failure marker; no captured subprocess output uses it.
exec 3>&2 2>/dev/null
mkdir -p "$evidence" || { echo 'restore-tools secure proof failed: setup (details redacted)' >&3; exit 1; }
rm -f "$evidence/restore-tools-secure.json"
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
 echo 'SKIP 77: restore-tools secure proof requires native x86_64 Linux' >&3
 exit 77
fi
source_sha=$(git -c core.fsmonitor=false rev-parse HEAD 2>/dev/null) || invalid
case "$source_sha" in ''|*[!0-9a-f]*) invalid;; esac
[ "${#source_sha}" -eq 40 ] || invalid
sbom_file="restore-tools-sha256-$hex.syft.json"
sbom_sha=$(node --input-type=module -e '
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
const bytes = readFileSync(process.argv[1]);
assert.equal(JSON.parse(bytes).source?.metadata?.imageID, process.argv[2]);
process.stdout.write(createHash("sha256").update(bytes).digest("hex"));
' "$evidence/$sbom_file" "$image_id") || { echo 'restore-tools secure proof failed: local SBOM (details redacted)' >&3; exit 1; }
donor='cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282'
phase=setup
default_user=false; verified_connection=false; dns=false
bootstrap_reruns=false; migration_reruns=false; grants_roles=false
rotation=false; wrong_ca=false; wrong_hostname=false; wrong_password=false; cleaned=false
network_id=; server_id=; private=
fail() { exit 1; }
bounded() { timeout --kill-after=5 "$@"; }
valid_id() { case "$1" in ''|*[!0-9a-f]*) return 1;; esac; [ "${#1}" -eq 64 ]; }
cleanup_container_file() {
 cid_file=$1
 [ -f "$cid_file" ] || return 0
 client_id=$(cat "$cid_file")
 valid_id "$client_id" || return 1
 # --rm may already have removed it. Verify absence with a successful listing,
 # rather than treating an inspect/daemon failure as proof of removal.
 bounded 20 docker rm -f "$client_id" >/dev/null 2>&1 || :
 remaining=$(bounded 20 docker ps -a --no-trunc --filter "id=$client_id" --format '{{.ID}}' 2>/dev/null) || return 1
 [ -z "$remaining" ] || return 1
 rm -f "$cid_file"
}
cleanup_client() { cleanup_container_file "$private/client.cid"; }
cleanup() {
 status=$?
 trap - EXIT
 cleanup_ok=true
 if [ -n "$private" ]; then
  cleanup_client || cleanup_ok=false
  cleanup_container_file "$private/helper.cid" || cleanup_ok=false
 fi
 if [ -n "$server_id" ]; then bounded 20 docker rm -f "$server_id" >/dev/null 2>&1 || cleanup_ok=false; fi
 if [ -n "$network_id" ]; then bounded 20 docker network rm "$network_id" >/dev/null 2>&1 || cleanup_ok=false; fi
 if [ -n "$private" ]; then rm -rf "$private" >/dev/null 2>&1 || cleanup_ok=false; fi
 if [ "$cleanup_ok" = true ]; then cleaned=true; else status=1; phase=cleanup; fi
 if [ "$status" -eq 0 ]; then phase=passed; else echo "restore-tools secure proof failed: $phase (details redacted)" >&3; fi
 # Only validated identifiers, fixed phase names and literal booleans enter
 # this sidecar. Credentials, addresses and raw diagnostics never leave /tmp.
 if ! printf '{"schema":2,"sourceSha":"%s","imageId":"%s","sbom":{"file":"%s","sha256":"%s"},"serverDonorDigest":"%s","phase":"%s","checks":{"defaultUser":%s,"verifiedConnection":%s,"dns":%s,"bootstrapReruns":%s,"migrationReruns":%s,"grantsRoles":%s,"rotation":%s,"wrongCa":%s,"wrongHostname":%s,"wrongPassword":%s,"cleanup":%s}}\n' \
  "$source_sha" "$image_id" "$sbom_file" "$sbom_sha" "${donor##*@}" "$phase" "$default_user" "$verified_connection" "$dns" \
  "$bootstrap_reruns" "$migration_reruns" "$grants_roles" "$rotation" "$wrong_ca" "$wrong_hostname" "$wrong_password" "$cleaned" \
  >"$evidence/.restore-tools-secure.json"; then exit 1; fi
 mv "$evidence/.restore-tools-secure.json" "$evidence/restore-tools-secure.json" || exit 1
 exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
command -v docker >/dev/null 2>&1 || fail
command -v timeout >/dev/null 2>&1 || fail
private=$(mktemp -d) || fail
mkdir "$private/certs" "$private/wrong-certs" "$private/credentials"

phase=default-user
user=$(bounded 20 docker image inspect --format '{{.Config.User}}' "$image_id" 2>/dev/null) || fail
[ "$user" = 65532:65532 ] || fail
phase=donor
bounded 30 docker image inspect "$donor" >/dev/null 2>&1 || bounded 180 docker pull "$donor" >/dev/null 2>&1 || fail
phase=certificates
cert() {
 directory=$1; shift
 cleanup_container_file "$private/helper.cid" || return 1
 bounded 30 docker run --rm --cidfile "$private/helper.cid" --network none --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$directory,dst=/certs" --entrypoint /cockroach/cockroach \
  "$donor" cert "$@" --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null 2>&1
}
cert "$private/certs" create-ca || fail
cert "$private/certs" create-node secure-db || fail
cert "$private/certs" create-client root || fail
cert "$private/wrong-certs" create-ca || fail
cp "$private/certs/ca.crt" "$private/credentials/ca.crt"
cp "$private/certs/client.root.crt" "$private/credentials/client.root.crt"
cp "$private/certs/client.root.key" "$private/credentials/client.root.key"
cp "$private/wrong-certs/ca.crt" "$private/credentials/wrong-ca.crt"
# The host parent remains 0700. Only these four files enter a read-only bind
# mount; numeric UID 65532 can traverse/read it and has no writable credential.
chmod 0755 "$private/credentials"
chmod 0444 "$private/credentials/"*
cleanup_container_file "$private/helper.cid" || fail
# Only the private client key needs numeric ownership. The setup helper is the
# pinned donor; all packaged client invocations retain their default UID/GID.
bounded 30 docker run --rm --cidfile "$private/helper.cid" --network none --user 0:0 \
 --mount "type=bind,src=$private/credentials,dst=/certs" --entrypoint /bin/sh "$donor" \
 -ec 'chown 65532:65532 /certs/client.root.key; chmod 0400 /certs/client.root.key' >/dev/null 2>&1 || fail
cleanup_container_file "$private/helper.cid" || fail

phase=network
network_name="jandibat-restore-secure-$(basename "$private")"
created=$(bounded 20 docker network create --internal "$network_name" 2>/dev/null) || fail
valid_id "$created" || fail
network_id=$created
phase=server
created=$(bounded 20 docker create --name "$network_name-db" --hostname secure-db \
 --network "$network_id" --network-alias secure-db --network-alias wrong-host \
 --user "$(id -u):$(id -g)" --mount "type=bind,src=$private/certs,dst=/certs,readonly" \
 "$donor" start-single-node --certs-dir=/certs --listen-addr=0.0.0.0:26257 \
 --http-addr=0.0.0.0:8080 --store=type=mem,size=0.25 --cache=64MiB --max-sql-memory=256MiB 2>/dev/null) || fail
valid_id "$created" || fail
server_id=$created
bounded 20 docker start "$server_id" >/dev/null 2>&1 || fail

# Deliberately recognizable synthetic credentials; they are never operational
# secrets. All DSNs/passwords stay in mode-0600 env files and SQL stdin.
migrator_password=MIGRATOR_SYNTHETIC_012345678901234567890123456789012345678901234567890
api_password=API_SYNTHETIC_012345678901234567890123456789012345678901234567890123456
worker_password=WORKER_SYNTHETIC_012345678901234567890123456789012345678901234567890123
maintenance_password=MAINTENANCE_SYNTHETIC_0123456789012345678901234567890123456789012345678
rotated_password=ROTATED_SYNTHETIC_01234567890123456789012345678901234567890123456789012
root_url='postgresql://root@secure-db:26257/defaultdb?sslmode=verify-full&sslrootcert=/credentials/ca.crt&sslcert=/credentials/client.root.crt&sslkey=/credentials/client.root.key'
url() { printf 'postgresql://%s:%s@secure-db:26257/jandibat?sslmode=verify-full&sslrootcert=/credentials/ca.crt' "$1" "$2"; }
migration_url=$(url jandibat_migrator "$migrator_password")
api_url=$(url jandibat_api "$api_password")
worker_url=$(url jandibat_worker "$worker_password")
maintenance_url=$(url jandibat_maintenance "$maintenance_password")
write_env() {
 printf 'COCKROACH_URL=%s\n' "$2" >"$private/$1.env"
 printf 'COCKROACH_ROOT_URL=%s\nMIGRATION_DATABASE_URL=%s\nAPI_DATABASE_URL=%s\nWORKER_DATABASE_URL=%s\nMAINTENANCE_DATABASE_URL=%s\n' \
  "$root_url" "$migration_url" "$api_url" "$worker_url" "$maintenance_url" >>"$private/$1.env"
 printf 'JANDIBAT_MIGRATOR_PASSWORD=%s\nJANDIBAT_API_PASSWORD=%s\nJANDIBAT_WORKER_PASSWORD=%s\nJANDIBAT_MAINTENANCE_PASSWORD=%s\n' \
  "$migrator_password" "$api_password" "$worker_password" "$maintenance_password" >>"$private/$1.env"
 printf 'COCKROACH_DATABASE=jandibat\nMIGRATIONS_DIR=/workspace/db/migrations\n' >>"$private/$1.env"
}
write_env root "$root_url"
write_env api "$api_url"
write_env rotated "$(url jandibat_api "$rotated_password")"
write_env wrong-ca "$(printf '%s' "$root_url" | sed 's|sslrootcert=/credentials/ca.crt|sslrootcert=/credentials/wrong-ca.crt|')"
write_env wrong-host "$(printf '%s' "$root_url" | sed 's|@secure-db:|@wrong-host:|')"
client_timeout=90
run_client() {
 env_name=$1; program=$2
 cleanup_client || fail
 if bounded "$client_timeout" docker run --rm -i --cidfile "$private/client.cid" \
  --network "$network_id" --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --tmpfs /tmp:rw,nosuid,nodev,mode=1777 --mount "type=bind,src=$private/credentials,dst=/credentials,readonly" \
  --env-file "$private/$env_name.env" --entrypoint /bin/sh "$image_id" -ec "$program" \
  >"$private/stdout" 2>"$private/stderr"; then client_status=0; else client_status=$?; fi
 for password in "$migrator_password" "$api_password" "$worker_password" "$maintenance_password" "$rotated_password"; do
  if grep -Fq "$password" "$private/stdout" "$private/stderr"; then fail; fi
 done
 return "$client_status"
}
sql_program='exec cockroach sql --format=tsv --set=errexit=true'
marker() {
 printf '%s\n' "$2" | run_client "$1" "$sql_program" || return 1
 [ "$(tail -n 1 "$private/stdout" | tr -d '\r')" = "$3" ]
}
phase=default-user
run_client root 'test "$(/busybox id -u):$(/busybox id -g)" = 65532:65532; printf "%s\n" "65532:65532" # default-user' </dev/null || fail
[ "$(cat "$private/stdout")" = 65532:65532 ] || fail
default_user=true
phase=readiness
client_timeout=5
attempt=0
while ! marker root "SELECT 'restore-tools-ready';" restore-tools-ready; do
 attempt=$((attempt + 1)); [ "$attempt" -lt 20 ] || fail
 sleep 1
done
client_timeout=90
verified_connection=true; dns=true
phase=bootstrap
for iteration in 1 2; do run_client root 'exec /bin/sh /workspace/scripts/db-bootstrap-roles.sh' </dev/null || fail; done
bootstrap_reruns=true
phase=migrations
for iteration in 1 2; do
 run_client root 'exec /bin/sh /workspace/scripts/db-migrate-url.sh' </dev/null || fail
 # This is the packaged migration connection, not root/defaultdb.
 printf '%s\n' "SELECT IF(count(*) > 0, 'restore-tools-migrations', 'invalid') FROM schema_migrations;" | \
  run_client root 'COCKROACH_URL="$MIGRATION_DATABASE_URL"; export COCKROACH_URL; exec cockroach sql --format=tsv --set=errexit=true' || fail
 [ "$(tail -n 1 "$private/stdout" | tr -d '\r')" = restore-tools-migrations ] || fail
done
migration_reruns=true
phase=grants-roles
run_client root 'exec /bin/sh /workspace/scripts/db-configure-runtime-roles.sh' </dev/null || fail
run_client root 'exec /bin/sh /workspace/scripts/db-verify-runtime-roles.sh' </dev/null || fail
marker api 'SELECT current_user;' jandibat_api || fail
grants_roles=true
phase=wrong-ca
if printf '%s\n' "SELECT 'restore-tools-ready';" | run_client wrong-ca "$sql_program"; then fail; fi
grep -Eq 'x509: certificate signed by unknown authority' "$private/stderr" || fail
marker root "SELECT 'restore-tools-ready';" restore-tools-ready || fail
wrong_ca=true
phase=wrong-hostname
if printf '%s\n' "SELECT 'restore-tools-ready';" | run_client wrong-host "$sql_program"; then fail; fi
grep -Eq 'x509: certificate is valid for .*not wrong-host|x509: certificate is not valid for any names, but wanted to match wrong-host' "$private/stderr" || fail
marker root "SELECT 'restore-tools-ready';" restore-tools-ready || fail
wrong_hostname=true
phase=rotation
printf "ALTER USER jandibat_api WITH PASSWORD '%s';\n" "$rotated_password" | run_client root "$sql_program" || fail
marker rotated 'SELECT current_user;' jandibat_api || fail
rotation=true
phase=wrong-password
if printf '%s\n' "SELECT 'restore-tools-ready';" | run_client api "$sql_program"; then fail; fi
grep -Eq 'password authentication failed for user' "$private/stderr" || fail
marker rotated 'SELECT current_user;' jandibat_api || fail
wrong_password=true
