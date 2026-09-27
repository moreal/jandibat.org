#!/bin/sh
set -eu

# This is deliberately a RED Linux gate until the Task 2 roles and connection
# bootstrap are implemented. It never accepts an existing DB or S3 endpoint.
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
 echo 'SKIP 77: backup-role fixture requires native x86_64 Linux' >&2
 exit 77
fi
# CI annotations use only these fixed categories. Never pass failure labels or
# captured tool output to the workflow command.
ci_phase=setup
annotate_failure() {
 [ "${GITHUB_ACTIONS:-}" = true ] || return 0
 case "$ci_phase" in
  setup|cockroach-image|certificate-generation|host-node-key|ca-certificate-copy|pkcs12-creation|log-config-validation|s3proxy-startup|db-start|role-and-connection-checks|cleanup) ;;
  *) ci_phase=setup ;;
 esac
 printf '::error title=Secure backup fixture failure::phase=%s\n' "$ci_phase" >&2
}
trap 'exit_status=$?; [ "$exit_status" -eq 0 ] || annotate_failure' EXIT
. scripts/backup-fixture-client.sh
command -v docker >/dev/null 2>&1 || { echo 'Docker is required for backup-role fixture' >&2; exit 2; }
command -v s3proxy >/dev/null 2>&1 || { echo 'Nix-pinned S3Proxy is required for backup-role fixture' >&2; exit 2; }
command -v mc >/dev/null 2>&1 || { echo 'Nix-pinned S3 client is required for backup-role fixture' >&2; exit 2; }
command -v openssl >/dev/null 2>&1 || { echo 'OpenSSL is required for backup-role fixture' >&2; exit 2; }

cockroach_image='cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282'
fixture_dir=$(mktemp -d)
fixture_id=$(basename "$fixture_dir")
db="jandibat-backup-db-$fixture_id"
db_creation_attempted=false
s3proxy_pid=
audit_expected=false
fail() { echo "RED: $1 (details redacted)" >&2; exit 1; }
diagnose_custom_s3_create() {
 capture=$1
 fixture_has_sentinel "$capture" 2>/dev/null && scan_result=0 || scan_result=$?
 if [ "$scan_result" -eq 0 ]; then
  fail 'custom S3 CREATE diagnostic exposed synthetic credential'
 elif [ "$scan_result" -ne 1 ]; then
  fail 'custom S3 CREATE diagnostic inspection unavailable'
 fi
 code=$(awk '
  {
   remaining = $0
   while (match(remaining, /SQLSTATE: [0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z]/)) {
    candidate = substr(remaining, RSTART + 10, 5)
    following = substr(remaining, RSTART + 15, 1)
    if (following !~ /[0-9A-Za-z]/) { print candidate; exit }
    remaining = substr(remaining, RSTART + RLENGTH)
   }
  }
 ' "$capture" 2>/dev/null) || fail 'custom S3 CREATE diagnostic inspection unavailable'
 if [ -n "$code" ]; then
  category=sql
 else
  code=unavailable
  grep -Eiq 'dial tcp|connection refused|connection reset|timed out|i/o timeout|tls handshake|x509:|certificate verify|network is unreachable' "$capture" 2>/dev/null && match_result=0 || match_result=$?
  if [ "$match_result" -eq 0 ]; then
   category=transport-client
  elif [ "$match_result" -eq 1 ]; then
   category=unknown
  else
   fail 'custom S3 CREATE diagnostic inspection unavailable'
  fi
 fi
 fail "custom S3 CREATE probe category=$category SQLSTATE=$code"
}
scan_capture() {
 target=$1
 label=$2
 fixture_has_sentinel "$target" && scan_result=0 || scan_result=$?
 if [ "$scan_result" -eq 0 ]; then
  echo "RED: $label exposed synthetic credential (details redacted)" >&2
  status=1
 elif [ "$scan_result" -ne 1 ]; then
  echo "RED: $label inspection unavailable (details redacted)" >&2
  status=1
 fi
}
cleanup() {
 status=$?
 initial_status=$status
 if [ "$db_creation_attempted" = true ]; then
  fixture_quiesce_collect_logs "$db" "$fixture_dir/db-logs" || status=1
  scan_capture "$fixture_dir/db-logs" 'server log'
  if ! fixture_check_log_sinks "$fixture_dir/log-config-check" "$fixture_dir/db-logs"; then
   echo 'RED: configured server log sink unavailable (details redacted)' >&2
   status=1
  fi
  if [ "$audit_expected" = true ] && ! fixture_audit_markers "$fixture_dir/db-logs"; then
   echo 'RED: positive SQL/security/sensitive audit marker missing (details redacted)' >&2
   status=1
  fi
 fi
 if [ -n "$s3proxy_pid" ]; then kill "$s3proxy_pid" >/dev/null 2>&1 || :; wait "$s3proxy_pid" >/dev/null 2>&1 || :; fi
 if [ -n "$s3proxy_pid" ] && [ ! -f "$fixture_dir/s3proxy-output" ]; then
  echo 'RED: synthetic storage log unavailable (details redacted)' >&2
  status=1
 fi
 if [ -f "$fixture_dir/s3proxy-output" ]; then scan_capture "$fixture_dir/s3proxy-output" 'synthetic storage log'; fi
 if [ -n "${synthetic_secret:-}" ]; then
  for output in "$fixture_dir"/mc-output "$fixture_dir"/probe "$fixture_dir"/users \
   "$fixture_dir"/roles "$fixture_dir"/system-grants "$fixture_dir"/db-grants \
   "$fixture_dir"/bootstrap-output "$fixture_dir"/privilege-probe "$fixture_dir"/privilege-check \
   "$fixture_dir"/privilege-drop "$fixture_dir"/first-run "$fixture_dir"/second-run \
   "$fixture_dir"/check "$fixture_dir"/verify-output "$fixture_dir"/connection-grants \
   "$fixture_dir"/count "$fixture_dir"/identity-* "$fixture_dir"/negative-* \
   "$fixture_dir"/mutation-* "$fixture_dir"/fingerprint-* \
   "$fixture_dir"/metadata-* "$fixture_dir"/defaultdb-grants \
   "$fixture_dir"/log-config-check "$fixture_dir"/sql-audit-setting; do
   if [ -f "$output" ]; then scan_capture "$output" 'client output'; fi
  done
 fi
 if [ "$db_creation_attempted" = true ] && ! docker rm "$db" >/dev/null 2>&1; then
  echo 'RED: secure fixture container removal incomplete (details redacted)' >&2
  status=1
 fi
 if ! rm -rf "$fixture_dir"; then
  echo 'RED: secure fixture private cleanup incomplete (details redacted)' >&2
  status=1
 fi
 if [ "$status" -eq 0 ] && [ "$audit_expected" = true ]; then
  echo 'secure backup roles, synthetic S3 connection, rerun and redaction checks passed'
 fi
 if [ "$status" -ne 0 ]; then
  [ "$initial_status" -ne 0 ] || ci_phase=cleanup
  annotate_failure
 fi
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
ci_phase=cockroach-image
echo 'PHASE: Cockroach image availability' >&2
docker image inspect "$cockroach_image" >/dev/null 2>&1 ||
 docker pull "$cockroach_image" >/dev/null 2>&1 || fail 'Cockroach image availability'

ci_phase=certificate-generation
echo 'PHASE: certificate generation' >&2
docker run --rm --user "$(id -u):$(id -g)" --mount "type=bind,src=$fixture_dir/certs,dst=/certs" \
 --entrypoint /cockroach/cockroach "$cockroach_image" cert create-ca --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null 2>&1 || fail 'certificate generation'
docker run --rm --user "$(id -u):$(id -g)" --mount "type=bind,src=$fixture_dir/certs,dst=/certs" \
 --entrypoint /cockroach/cockroach "$cockroach_image" cert create-node localhost 127.0.0.1 \
 --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null 2>&1 || fail 'certificate generation'
docker run --rm --user "$(id -u):$(id -g)" --mount "type=bind,src=$fixture_dir/certs,dst=/certs" \
 --entrypoint /cockroach/cockroach "$cockroach_image" cert create-client root \
 --certs-dir=/certs --ca-key=/certs/ca.key >/dev/null 2>&1 || fail 'certificate generation'
ci_phase=host-node-key
echo 'PHASE: host node key readability' >&2
[ -r "$fixture_dir/certs/node.key" ] || fail 'host node key unreadable'
ci_phase=ca-certificate-copy
echo 'PHASE: CA certificate copy' >&2
cp "$fixture_dir/certs/ca.crt" "$fixture_dir/ca-only/ca.crt" >/dev/null 2>&1 || fail 'CA certificate copy'
keystore_password=$(awk 'BEGIN { for (i=0;i<44;i++) printf "P" }')
export KEYSTORE_PASSWORD="$keystore_password"
ci_phase=pkcs12-creation
echo 'PHASE: PKCS#12 creation' >&2
openssl pkcs12 -export -in "$fixture_dir/certs/node.crt" -inkey "$fixture_dir/certs/node.key" \
 -out "$fixture_dir/s3proxy.p12" -passout env:KEYSTORE_PASSWORD >/dev/null 2>&1 || fail 'PKCS#12 creation'
ci_phase=log-config-validation
echo 'PHASE: log config validation' >&2
fixture_log_config_dir=$(pwd)/scripts/fixtures
[ -r "$fixture_log_config_dir/cockroach-backup-logging.yaml" ] || fail 'log config validation'
docker run --rm --mount "type=bind,src=$fixture_log_config_dir,dst=/fixture-logging,readonly" \
 --entrypoint /cockroach/cockroach "$cockroach_image" debug check-log-config \
 --log-config-file=/fixture-logging/cockroach-backup-logging.yaml \
 >"$fixture_dir/log-config-check" 2>&1 || fail 'log config validation'
fixture_effective_log_groups "$fixture_dir/log-config-check" >/dev/null || fail 'log config validation'

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
fixture_write_env

{
 printf 's3proxy.authorization=aws-v2-or-v4\n'
 printf 's3proxy.secure-endpoint=https://127.0.0.1:9009\n'
 printf 's3proxy.identity=%s\ns3proxy.credential=%s\n' "$synthetic_access" "$synthetic_secret"
 printf 's3proxy.keystore-path=%s\ns3proxy.keystore-password=%s\n' "$fixture_dir/s3proxy.p12" "$keystore_password"
 printf 'jclouds.provider=filesystem\njclouds.filesystem.basedir=%s\n' "$fixture_dir/s3-data"
} >"$fixture_dir/s3proxy.properties"
ci_phase=s3proxy-startup
echo 'PHASE: S3Proxy startup' >&2
s3proxy --properties "$fixture_dir/s3proxy.properties" >"$fixture_dir/s3proxy-output" 2>&1 &
s3proxy_pid=$!
ci_phase=db-start
echo 'PHASE: DB start' >&2
db_creation_attempted=true
docker run -d --name "$db" --network host \
 --env SSL_CERT_FILE=/certs/ca.crt \
 --mount "type=bind,src=$fixture_dir/certs,dst=/certs,readonly" \
 --mount "type=bind,src=$fixture_log_config_dir,dst=/fixture-logging,readonly" \
 --entrypoint /cockroach/cockroach "$cockroach_image" start-single-node \
 --certs-dir=/certs --log-config-file=/fixture-logging/cockroach-backup-logging.yaml \
 --listen-addr=127.0.0.1:26259 --http-addr=127.0.0.1:8089 >/dev/null 2>&1 || fail 'DB start'
ci_phase=role-and-connection-checks

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
sql_as root 'SET CLUSTER SETTING sql.log.all_statements.enabled = true;' \
 >"$fixture_dir/sql-audit-setting" 2>&1 || fail 'SQL statement audit setting'
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
sql_as root "SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON DATABASE defaultdb] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier') ORDER BY grantee, privilege_type;" \
 >"$fixture_dir/defaultdb-grants" 2>&1 || fail 'private database grants'
fixture_check_defaultdb_grants "$fixture_dir/defaultdb-grants" || fail 'private database grant boundary'
sql_as root "SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON SCHEMA defaultdb.jandibat_backup_admin] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier', 'public') ORDER BY grantee, privilege_type;" \
 >"$fixture_dir/metadata-schema-grants" 2>&1 || fail 'metadata schema grants'
fixture_check_metadata_grants schema "$fixture_dir/metadata-schema-grants" || fail 'metadata schema grant boundary'
sql_as root "SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.connection_policy] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier', 'public') ORDER BY grantee, privilege_type;" \
 >"$fixture_dir/metadata-table-grants" 2>&1 || fail 'metadata table grants'
fixture_check_metadata_grants table "$fixture_dir/metadata-table-grants" || fail 'metadata table grant boundary'
sql_as root "SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON TABLE defaultdb.jandibat_backup_admin.connection_live_digest] WHERE grantee IN ('jandibat_backup_bootstrap', 'jandibat_backup_runner', 'jandibat_backup_verifier', 'public') ORDER BY grantee, privilege_type;" \
 >"$fixture_dir/metadata-view-grants" 2>&1 || fail 'metadata view grants'
fixture_check_metadata_grants view "$fixture_dir/metadata-view-grants" || fail 'metadata view grant boundary'
sql_as bootstrap "SET allow_unsafe_internals = true; SELECT connection_name, catalog_digest FROM defaultdb.jandibat_backup_admin.connection_live_digest;" \
 >"$fixture_dir/metadata-view-read" 2>&1 || fail 'bootstrap digest view read'
sql_as bootstrap "BEGIN; INSERT INTO defaultdb.jandibat_backup_admin.connection_policy (connection_name, policy_version, input_digest, catalog_digest) VALUES ('jandibat_backup_v1', 1, repeat('a',64), repeat('b',64)); ROLLBACK;" \
 >"$fixture_dir/metadata-insert-probe" 2>&1 || fail 'bootstrap metadata INSERT capability'
assert_denied bootstrap raw-catalog 'SET allow_unsafe_internals = true; SELECT connection_details FROM system.external_connections;'
assert_denied bootstrap metadata-update 'UPDATE defaultdb.jandibat_backup_admin.connection_policy SET policy_version = 1 WHERE false;'
assert_denied bootstrap metadata-delete 'DELETE FROM defaultdb.jandibat_backup_admin.connection_policy WHERE false;'
assert_denied bootstrap metadata-create 'CREATE TABLE defaultdb.jandibat_backup_admin.forbidden (id INT PRIMARY KEY);'
assert_denied verifier metadata-read 'SELECT * FROM defaultdb.jandibat_backup_admin.connection_policy;'
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
# Only a validated SQLSTATE and fixed category may leave the private capture.
probe_uri="s3://disposable-backup/fixture-only?AWS_ACCESS_KEY_ID=$synthetic_access&AWS_SECRET_ACCESS_KEY=$synthetic_secret&AWS_ENDPOINT=https%3A%2F%2F127.0.0.1%3A9009&AWS_REGION=us-east-1&AWS_USE_PATH_STYLE=true"
if ! sql_as bootstrap "CREATE EXTERNAL CONNECTION jandibat_privilege_probe AS '$probe_uri';" \
 >"$fixture_dir/privilege-probe" 2>&1; then
 diagnose_custom_s3_create "$fixture_dir/privilege-probe"
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
audit_expected=true
