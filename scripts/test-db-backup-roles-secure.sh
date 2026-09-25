#!/bin/sh
set -eu

# This is deliberately a RED Linux gate until the Task 2 roles and connection
# bootstrap are implemented. It never accepts an existing DB or S3 endpoint.
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
 echo 'SKIP 77: backup-role fixture requires native x86_64 Linux' >&2
 exit 77
fi
command -v docker >/dev/null 2>&1 || { echo 'Docker is required for backup-role fixture' >&2; exit 2; }
command -v s3proxy >/dev/null 2>&1 || { echo 'Nix-pinned S3Proxy is required for backup-role fixture' >&2; exit 2; }
command -v mc >/dev/null 2>&1 || { echo 'Nix-pinned S3 client is required for backup-role fixture' >&2; exit 2; }
command -v openssl >/dev/null 2>&1 || { echo 'OpenSSL is required for backup-role fixture' >&2; exit 2; }

cockroach_image='cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282'
fixture_dir=$(mktemp -d)
fixture_id=$(basename "$fixture_dir")
db="jandibat-backup-db-$fixture_id"
db_created=false
s3proxy_pid=
has_sentinel() {
 for sentinel in "${synthetic_secret:-}" "${synthetic_access:-}" "${rotated_secret:-}" "${keystore_password:-}" \
  "${password_a:-}" "${password_b:-}" "${password_c:-}" "${password_d:-}" \
  "${password_e:-}" "${password_f:-}" "${password_g:-}"; do
  [ -n "$sentinel" ] || continue
  if [ -d "$1" ]; then
   grep -R -Fq "$sentinel" "$1" && return 0
  else
   grep -Fq "$sentinel" "$1" && return 0
  fi
 done
 return 1
}
cleanup() {
 status=$?
 if [ "$db_created" = true ]; then
  if docker cp "$db:/cockroach/cockroach-data/logs/." "$fixture_dir/db-logs/" >/dev/null 2>&1 &&
   docker logs "$db" >"$fixture_dir/db-logs/container.log" 2>&1; then
   if has_sentinel "$fixture_dir/db-logs"; then
    echo 'RED: server-log sentinel exposed (details redacted)' >&2
    status=1
   fi
  else
   echo 'RED: server log inspection unavailable (details redacted)' >&2
   status=1
  fi
 fi
 if [ "$db_created" = true ] && ! fixture_stop_container "$db"; then status=1; fi
 if [ -n "$s3proxy_pid" ]; then kill "$s3proxy_pid" >/dev/null 2>&1 || :; wait "$s3proxy_pid" >/dev/null 2>&1 || :; fi
 if [ -f "$fixture_dir/s3proxy-output" ] && has_sentinel "$fixture_dir/s3proxy-output"; then
  echo 'RED: synthetic storage log exposed credential (details redacted)' >&2
  status=1
 fi
 if [ -n "${synthetic_secret:-}" ]; then
  for output in "$fixture_dir"/mc-output "$fixture_dir"/probe "$fixture_dir"/users \
   "$fixture_dir"/roles "$fixture_dir"/system-grants "$fixture_dir"/db-grants \
   "$fixture_dir"/bootstrap-output "$fixture_dir"/privilege-probe "$fixture_dir"/privilege-check \
   "$fixture_dir"/privilege-drop "$fixture_dir"/first-run "$fixture_dir"/second-run \
   "$fixture_dir"/check "$fixture_dir"/verify-output "$fixture_dir"/connection-grants \
   "$fixture_dir"/count "$fixture_dir"/identity-* "$fixture_dir"/negative-* \
   "$fixture_dir"/mutation-* "$fixture_dir"/fingerprint-*; do
   if [ -f "$output" ] && has_sentinel "$output"; then
    echo 'RED: client output exposed synthetic credential (details redacted)' >&2
    status=1
   fi
  done
 fi
 rm -rf "$fixture_dir"
 exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
umask 077
mkdir -p "$fixture_dir/certs" "$fixture_dir/ca-only" "$fixture_dir/s3-data" "$fixture_dir/workspace/scripts" "$fixture_dir/db-logs"
for script in db-bootstrap-roles.sh db-bootstrap-backup-connection.sh db-verify-backup-roles.sh; do
 [ ! -f "scripts/$script" ] || cp "scripts/$script" "$fixture_dir/workspace/scripts/$script"
done

# The only container image is the repository-pinned secure Cockroach release.
docker image inspect "$cockroach_image" >/dev/null 2>&1 || docker pull "$cockroach_image" >/dev/null

docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" \
 --entrypoint /cockroach/cockroach "$cockroach_image" cert create-ca --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null
docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" \
 --entrypoint /cockroach/cockroach "$cockroach_image" cert create-node localhost 127.0.0.1 \
 --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null
docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" \
 --entrypoint /cockroach/cockroach "$cockroach_image" cert create-client root \
 --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null
cp "$fixture_dir/certs/ca.crt" "$fixture_dir/ca-only/ca.crt"
keystore_password=$(awk 'BEGIN { for (i=0;i<44;i++) printf "P" }')
export KEYSTORE_PASSWORD="$keystore_password"
openssl pkcs12 -export -in "$fixture_dir/certs/node.crt" -inkey "$fixture_dir/certs/node.key" \
 -out "$fixture_dir/s3proxy.p12" -passout env:KEYSTORE_PASSWORD >/dev/null 2>&1

synthetic_access=$(awk 'BEGIN { for (i=0;i<44;i++) printf "K" }')
synthetic_secret=$(awk 'BEGIN { for (i=0;i<44;i++) printf "S" }')
password_a=$(awk 'BEGIN { for (i=0;i<43;i++) printf "A" }')
password_b=$(awk 'BEGIN { for (i=0;i<43;i++) printf "B" }')
password_c=$(awk 'BEGIN { for (i=0;i<43;i++) printf "C" }')
password_d=$(awk 'BEGIN { for (i=0;i<43;i++) printf "D" }')
password_e=$(awk 'BEGIN { for (i=0;i<43;i++) printf "E" }')
password_f=$(awk 'BEGIN { for (i=0;i<43;i++) printf "F" }')
password_g=$(awk 'BEGIN { for (i=0;i<43;i++) printf "G" }')
root_url='postgresql://root@localhost:26259/defaultdb?sslmode=verify-full&sslrootcert=/certs/ca.crt&sslcert=/certs/client.root.crt&sslkey=/certs/client.root.key'
role_url() { printf 'postgresql://%s:%s@localhost:26259/jandibat?sslmode=verify-full&sslrootcert=/certs/ca.crt' "$1" "$2"; }
FIXTURE_DIR=$fixture_dir
FIXTURE_IMAGE=$cockroach_image
FIXTURE_ROOT_URL=$root_url
FIXTURE_MIGRATION_URL=$(role_url jandibat_migrator "$password_a")
FIXTURE_API_URL=$(role_url jandibat_api "$password_b")
FIXTURE_WORKER_URL=$(role_url jandibat_worker "$password_c")
FIXTURE_MAINTENANCE_URL=$(role_url jandibat_maintenance "$password_d")
FIXTURE_BOOTSTRAP_URL=$(role_url jandibat_backup_bootstrap "$password_e")
FIXTURE_RUNNER_URL=$(role_url jandibat_backup_runner "$password_f")
FIXTURE_VERIFIER_URL=$(role_url jandibat_backup_verifier "$password_g")
FIXTURE_PASS_A=$password_a FIXTURE_PASS_B=$password_b FIXTURE_PASS_C=$password_c FIXTURE_PASS_D=$password_d
FIXTURE_PASS_E=$password_e FIXTURE_PASS_F=$password_f FIXTURE_PASS_G=$password_g
FIXTURE_ACCESS=$synthetic_access FIXTURE_SECRET=$synthetic_secret
. scripts/backup-fixture-client.sh
fixture_write_env

{
 printf 's3proxy.authorization=aws-v2-or-v4\n'
 printf 's3proxy.secure-endpoint=https://127.0.0.1:9009\n'
 printf 's3proxy.identity=%s\ns3proxy.credential=%s\n' "$synthetic_access" "$synthetic_secret"
 printf 's3proxy.keystore-path=%s\ns3proxy.keystore-password=%s\n' "$fixture_dir/s3proxy.p12" "$keystore_password"
 printf 'jclouds.provider=filesystem\njclouds.filesystem.basedir=%s\n' "$fixture_dir/s3-data"
} >"$fixture_dir/s3proxy.properties"
s3proxy --properties "$fixture_dir/s3proxy.properties" >"$fixture_dir/s3proxy-output" 2>&1 &
s3proxy_pid=$!
docker run -d --rm --name "$db" --network host \
 --env SSL_CERT_FILE=/certs/ca.crt \
 --mount "type=bind,src=$fixture_dir/certs,dst=/certs,readonly" \
 --entrypoint /cockroach/cockroach "$cockroach_image" start-single-node \
 --certs-dir=/certs --listen-addr=127.0.0.1:26259 --http-addr=127.0.0.1:8089 >/dev/null
db_created=true

client() { fixture_client "$@"; }
sql_as() {
 identity=$1
 statement=$2
 case "$identity" in
  root) url_var=COCKROACH_ROOT_URL ;;
  bootstrap) url_var=BACKUP_BOOTSTRAP_DATABASE_URL ;;
  runner) url_var=BACKUP_RUNNER_DATABASE_URL ;;
  verifier) url_var=BACKUP_VERIFIER_DATABASE_URL ;;
  *) echo 'unknown fixture identity' >&2; exit 2 ;;
 esac
 printf '%s\n' "$statement" | client "$identity" -c "COCKROACH_URL=\"\$$url_var\" /cockroach/cockroach sql --set=errexit=true --format=tsv"
}
fail() { echo "RED: $1 (details redacted)" >&2; exit 1; }
assert_denied() {
 role=$1
 label=$2
 statement=$3
 if sql_as "$role" "$statement" >"$fixture_dir/negative-$role-$label" 2>&1; then
  fail "$role $label unexpectedly succeeded"
 fi
 grep -Eq '42501|insufficient privilege|permission denied' "$fixture_dir/negative-$role-$label" ||
  fail "$role $label lacked privilege-denial SQLSTATE"
}
connection_fingerprint() {
 output=$1
 sql_as root "SELECT connection_type, sha256(connection_uri) FROM [SHOW EXTERNAL CONNECTIONS] WHERE connection_name = 'jandibat_backup_v1';" \
  >"$output" 2>&1 || fail 'connection policy fingerprint query'
 awk -F '\t' 'NR==2 && $1=="STORAGE" && length($2)>0 { n++ } END { exit n!=1 }' "$output" ||
  fail 'connection policy fingerprint missing'
}
ready=false
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
 if sql_as root 'SELECT 1;' >"$fixture_dir/probe" 2>&1 &&
  [ "$(tail -n 1 "$fixture_dir/probe" | tr -d '\r')" = 1 ]; then ready=true; break; fi
 sleep 1
done
[ "$ready" = true ] || fail 'secure Cockroach node did not become ready'
bucket_ready=false
export MC_HOST_fixture="https://$synthetic_access:$synthetic_secret@127.0.0.1:9009"
export SSL_CERT_FILE="$fixture_dir/certs/ca.crt"
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
 if mc mb fixture/disposable-backup >"$fixture_dir/mc-output" 2>&1; then bucket_ready=true; break; fi
 sleep 1
done
[ "$bucket_ready" = true ] || fail 'synthetic S3 bucket did not become ready'

# The production account bootstrap is the only root-cert SQL actor.
client root /workspace/scripts/db-bootstrap-roles.sh >"$fixture_dir/bootstrap-output" 2>&1 || fail 'account bootstrap'
sql_as root 'SHOW USERS;' >"$fixture_dir/users" 2>&1 || fail 'role listing'
for role in bootstrap runner verifier; do
 awk -F '\t' -v name="jandibat_backup_$role" '$1 == name && $2 !~ /NOLOGIN/ { n++ } END { exit n != 1 }' \
  "$fixture_dir/users" || fail "missing $role LOGIN role"
 sql_as "$role" 'SELECT current_user();' >"$fixture_dir/identity-$role" 2>&1 || fail "$role login identity"
 [ "$(tail -n 1 "$fixture_dir/identity-$role" | tr -d '\r')" = "jandibat_backup_$role" ] || fail "$role identity mismatch"
done
sql_as root "SELECT grantee, privilege_type, is_grantable FROM [SHOW SYSTEM GRANTS] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') ORDER BY grantee, privilege_type;" \
 >"$fixture_dir/system-grants" 2>&1 || fail 'system grants'
fixture_check_system_grants "$fixture_dir/system-grants" || fail 'backup system grant boundary'
sql_as root 'SHOW ROLES;' >"$fixture_dir/roles" 2>&1 || fail 'role membership listing'
awk -F '\t' '$1 ~ /^jandibat_backup_/ { n++; if ($3 != "{}") bad=1 } END { exit bad || n!=3 }' \
 "$fixture_dir/roles" || fail 'backup role has admin membership'
sql_as root "SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON DATABASE jandibat] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') ORDER BY grantee, privilege_type;" \
 >"$fixture_dir/db-grants" 2>&1 || fail 'database grants'
fixture_check_database_grants "$fixture_dir/db-grants" || fail 'exact backup database grant boundary'
sql_as root 'CREATE TABLE jandibat.public.backup_privilege_probe (id INT PRIMARY KEY);' \
 >"$fixture_dir/mutation-table-setup" 2>&1 || fail 'privilege probe table setup'
for role in bootstrap runner verifier; do
 assert_denied "$role" create-user "CREATE USER jandibat_forbidden_$role;"
 assert_denied "$role" create-table "CREATE TABLE jandibat.public.forbidden_$role (id INT PRIMARY KEY);"
 assert_denied "$role" drop-table 'DROP TABLE jandibat.public.backup_privilege_probe;'
done

# missing-permission: creation must fail without EXTERNALCONNECTION, leaving
# the fixed connection name absent. Restore the grant only in this fixture.
sql_as root 'REVOKE SYSTEM EXTERNALCONNECTION FROM jandibat_backup_bootstrap;' \
 >"$fixture_dir/mutation-revoke" 2>&1 || fail 'missing-permission setup'
if client bootstrap /workspace/scripts/db-bootstrap-backup-connection.sh \
 >"$fixture_dir/mutation-missing-permission" 2>&1; then fail 'missing-permission accepted'; fi
sql_as root 'GRANT SYSTEM EXTERNALCONNECTION TO jandibat_backup_bootstrap;' \
 >"$fixture_dir/mutation-regrant" 2>&1 || fail 'missing-permission restore'
sql_as root "SELECT count(*) FROM [SHOW EXTERNAL CONNECTIONS] WHERE connection_name = 'jandibat_backup_v1';" \
 >"$fixture_dir/mutation-count" 2>&1 || fail 'missing-permission count'
[ "$(tail -n 1 "$fixture_dir/mutation-count" | tr -d '\r')" = 0 ] || fail 'missing-permission created connection'

# Probe custom-endpoint CREATE under the non-root identity independently of
# the application wrapper. SQL and errors stay in private fixture files; the
# only diagnostic emitted to CI is a five-character SQLSTATE.
probe_uri="s3://disposable-backup/fixture-only?AWS_ACCESS_KEY_ID=$synthetic_access&AWS_SECRET_ACCESS_KEY=$synthetic_secret&AWS_ENDPOINT=https%3A%2F%2F127.0.0.1%3A9009&AWS_REGION=us-east-1&AWS_USE_PATH_STYLE=true"
if ! sql_as bootstrap "CREATE EXTERNAL CONNECTION jandibat_privilege_probe AS '$probe_uri';" \
 >"$fixture_dir/privilege-probe" 2>&1; then
 code=$(sed -n 's/.*SQLSTATE: \([0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z]\).*/\1/p' "$fixture_dir/privilege-probe" | head -n 1)
 fail "custom S3 CREATE privilege SQLSTATE ${code:-unavailable}"
fi
sql_as bootstrap "CHECK EXTERNAL CONNECTION 'external://jandibat_privilege_probe' WITH transfer = '1MiB';" \
 >"$fixture_dir/privilege-check" 2>&1 || fail 'custom S3 CHECK privilege'
sql_as bootstrap 'DROP EXTERNAL CONNECTION jandibat_privilege_probe;' \
 >"$fixture_dir/privilege-drop" 2>&1 || fail 'fixture-only probe disposal'
unset probe_uri

# malformed-uri: reject invalid endpoint input before any CREATE.
cp "$fixture_dir/bootstrap.env" "$fixture_dir/bootstrap-original.env"
sed 's#BACKUP_S3_ENDPOINT=https://127.0.0.1:9009#BACKUP_S3_ENDPOINT=https://127.0.0.1:9009/%bad#' \
 "$fixture_dir/bootstrap-original.env" >"$fixture_dir/bootstrap.env"
if client bootstrap /workspace/scripts/db-bootstrap-backup-connection.sh \
 >"$fixture_dir/mutation-malformed-uri" 2>&1; then fail 'malformed-uri accepted'; fi
cp "$fixture_dir/bootstrap-original.env" "$fixture_dir/bootstrap.env"

# The production wrapper must reproduce this success and be idempotent.
client bootstrap /workspace/scripts/db-bootstrap-backup-connection.sh >"$fixture_dir/first-run" 2>&1 || fail 'first connection run'
sql_as bootstrap "CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1' WITH transfer = '1MiB';" \
 >"$fixture_dir/check" 2>&1 || fail 'connection check'
client bootstrap /workspace/scripts/db-bootstrap-backup-connection.sh >"$fixture_dir/second-run" 2>&1 || fail 'second run idempotency'
connection_fingerprint "$fixture_dir/fingerprint-before"
sql_as root "SELECT count(*) FROM [SHOW EXTERNAL CONNECTIONS] WHERE connection_name = 'jandibat_backup_v1';" \
 >"$fixture_dir/count" 2>&1 || fail 'connection count'
[ "$(tail -n 1 "$fixture_dir/count" | tr -d '\r')" = 1 ] || fail 'duplicate connection after second run'
sql_as root 'SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON EXTERNAL CONNECTION jandibat_backup_v1] ORDER BY grantee, privilege_type;' \
 >"$fixture_dir/connection-grants" 2>&1 || fail 'connection grants'
fixture_check_connection_grants "$fixture_dir/connection-grants" || fail 'exact connection grants'
for role in runner verifier; do
 assert_denied "$role" external-create "CREATE EXTERNAL CONNECTION jandibat_forbidden_$role AS 'nodelocal://1/forbidden';"
done
for role in bootstrap runner verifier; do
 assert_denied "$role" restore "RESTORE DATABASE jandibat FROM LATEST IN 'external://jandibat_backup_v1';"
done
client verifier /workspace/scripts/db-verify-backup-roles.sh >"$fixture_dir/verify-output" 2>&1 || fail 'backup role verifier'

# credential-rotation: changed Secret must be refused rather than silently
# using the stored connection or altering it in place.
rotated_secret=$(awk 'BEGIN { for (i=0;i<44;i++) printf "R" }')
sed "s/^BACKUP_S3_SECRET_ACCESS_KEY=.*/BACKUP_S3_SECRET_ACCESS_KEY=$rotated_secret/" \
 "$fixture_dir/bootstrap-original.env" >"$fixture_dir/bootstrap.env"
if client bootstrap /workspace/scripts/db-bootstrap-backup-connection.sh \
 >"$fixture_dir/mutation-credential-rotation" 2>&1; then fail 'credential-rotation accepted'; fi
cp "$fixture_dir/bootstrap-original.env" "$fixture_dir/bootstrap.env"
connection_fingerprint "$fixture_dir/fingerprint-after-rotation"
cmp -s "$fixture_dir/fingerprint-before" "$fixture_dir/fingerprint-after-rotation" || fail 'credential-rotation changed connection policy'
sql_as bootstrap "CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1' WITH transfer = '1MiB';" \
 >"$fixture_dir/mutation-rotation-check" 2>&1 || fail 'post-rejection CHECK after credential-rotation'
sql_as root 'SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON EXTERNAL CONNECTION jandibat_backup_v1] ORDER BY grantee, privilege_type;' \
 >"$fixture_dir/mutation-rotation-grants" 2>&1 || fail 'post-rotation grant query'
fixture_check_connection_grants "$fixture_dir/mutation-rotation-grants" || fail 'post-rotation grant policy'
cmp -s "$fixture_dir/connection-grants" "$fixture_dir/mutation-rotation-grants" || fail 'credential-rotation changed grants'

# conflicting-connection: a changed URI at the fixed name must be refused.
# Only this disposable database is mutated; no production connection is dropped.
conflict_uri="s3://disposable-backup/fixture-conflict?AWS_ACCESS_KEY_ID=$synthetic_access&AWS_SECRET_ACCESS_KEY=$synthetic_secret&AWS_ENDPOINT=https%3A%2F%2F127.0.0.1%3A9009&AWS_REGION=us-east-1&AWS_USE_PATH_STYLE=true"
sql_as root "ALTER EXTERNAL CONNECTION jandibat_backup_v1 AS '$conflict_uri';" \
 >"$fixture_dir/mutation-alter" 2>&1 || fail 'conflicting-connection setup'
unset conflict_uri
connection_fingerprint "$fixture_dir/fingerprint-conflict-before"
if cmp -s "$fixture_dir/fingerprint-before" "$fixture_dir/fingerprint-conflict-before"; then fail 'conflicting-connection setup did not change policy'; fi
if client bootstrap /workspace/scripts/db-bootstrap-backup-connection.sh \
 >"$fixture_dir/mutation-conflicting-connection" 2>&1; then fail 'conflicting-connection accepted'; fi
connection_fingerprint "$fixture_dir/fingerprint-conflict-after"
cmp -s "$fixture_dir/fingerprint-conflict-before" "$fixture_dir/fingerprint-conflict-after" || fail 'conflicting-connection changed policy'
sql_as bootstrap "CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1' WITH transfer = '1MiB';" \
 >"$fixture_dir/mutation-conflict-check" 2>&1 || fail 'post-rejection CHECK after conflicting-connection'
sql_as root 'SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON EXTERNAL CONNECTION jandibat_backup_v1] ORDER BY grantee, privilege_type;' \
 >"$fixture_dir/mutation-conflict-grants" 2>&1 || fail 'post-conflict grant query'
fixture_check_connection_grants "$fixture_dir/mutation-conflict-grants" || fail 'post-conflict grant policy'
cmp -s "$fixture_dir/connection-grants" "$fixture_dir/mutation-conflict-grants" || fail 'conflicting-connection changed grants'
sql_as root "SELECT count(*) FROM [SHOW EXTERNAL CONNECTIONS] WHERE connection_name = 'jandibat_backup_v1';" \
 >"$fixture_dir/mutation-final-count" 2>&1 || fail 'conflicting-connection count'
[ "$(tail -n 1 "$fixture_dir/mutation-final-count" | tr -d '\r')" = 1 ] || fail 'conflicting-connection was dropped'

# Never stream raw SQL errors into CI. The EXIT trap checks all output even
# when an earlier RED assertion fails.
echo 'secure backup roles, synthetic S3 connection, rerun and redaction checks passed'
