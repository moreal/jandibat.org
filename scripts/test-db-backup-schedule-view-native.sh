#!/bin/sh
set -eu

# Disposable SQL semantics test. Native Linux amd64 CI is required; local
# ARM64 runs require an explicit supplementary-probe opt-in. This insecure
# nodelocal fixture is not the secure x86_64 S3 release gate.
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-schedule-view.XXXXXX")
container="jandibat-task3-view-populated-$$"
owned=false
cleanup() {
 if [ "$owned" = true ]; then docker rm -f "$container" >/dev/null 2>&1 || :; fi
 rm -rf "$test_dir"
}
trap cleanup EXIT HUP INT TERM
umask 077
fail() { echo "schedule-view fixture failed: $1" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || fail 'Docker unavailable'
host_os=$(uname -s)
host_arch=$(uname -m)
if [ "$host_os" != Linux ] || { [ "$host_arch" != x86_64 ] && [ "$host_arch" != amd64 ]; }; then
 if [ "${TASK3_ALLOW_ARM64_PROBE:-}" != 1 ]; then
  echo 'SKIP: native Linux x86_64 host required (exit 77)' >&2
  exit 77
 fi
fi
server_arch=$(docker info --format '{{.OSType}} {{.Architecture}}' 2>"$test_dir/error") || fail 'Docker daemon unavailable'
case "$server_arch" in
 'linux x86_64'|'linux amd64')
  if [ "$host_os" = Linux ] && { [ "$host_arch" = x86_64 ] || [ "$host_arch" = amd64 ]; }; then
   evidence_label=native-x86_64
  else
   evidence_label=supplementary-nonnative-host
  fi;;
 'linux aarch64'|'linux arm64')
  if [ "${TASK3_ALLOW_ARM64_PROBE:-}" != 1 ]; then
   echo 'SKIP: native x86_64 Docker daemon required (exit 77)' >&2
   exit 77
  fi
  evidence_label=supplementary-arm64;;
 *) echo 'SKIP: native Linux x86_64 Docker daemon required (exit 77)' >&2; exit 77;;
esac
[ -z "$(docker ps -a --filter "name=^/$container$" --format '{{.Names}}')" ] || fail 'probe name already exists'
docker run -d --name "$container" --network none --memory 2g \
 cockroachdb/cockroach:v26.2.5 start-single-node --insecure \
 --listen-addr=127.0.0.1:26257 --http-addr=127.0.0.1:8080 \
 --store=type=mem,size=1GiB --external-io-dir=/tmp/h2-probe-io \
 --cache=128MiB --max-sql-memory=128MiB >"$test_dir/container-id" 2>"$test_dir/error" || fail 'probe start'
owned=true

ready=false
for n in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
 if printf 'SELECT 1;\n' | docker exec -i "$container" /cockroach/cockroach sql \
  --insecure --host=127.0.0.1:26257 --format=tsv >"$test_dir/output" 2>"$test_dir/error"; then
  ready=true; break
 fi
 sleep 1
done
[ "$ready" = true ] || fail 'SQL readiness'

sql_as() {
 actor=$1; statement=$2
 if ! printf '%s\n' "$statement" | docker exec -i "$container" /cockroach/cockroach sql \
  --insecure --host=127.0.0.1:26257 --user="$actor" --format=tsv \
  --set=errexit=true >"$test_dir/output" 2>"$test_dir/error"; then
  fail 'SQL operation'
 fi
}
denied_as() {
 actor=$1
 if printf '%s\n' 'SET allow_unsafe_internals = true; SELECT id FROM system.scheduled_jobs WHERE false;' | \
  docker exec -i "$container" /cockroach/cockroach sql --insecure \
   --host=127.0.0.1:26257 --user="$actor" --format=tsv --set=errexit=true \
   >"$test_dir/output" 2>"$test_dir/error"; then
  fail 'raw scheduled-jobs access was allowed'
 fi
 grep -q 'SQLSTATE: 42501' "$test_dir/error" || fail 'raw access failed for a reason other than privilege'
}

# Execute the production view definition, not a simplified test-only view.
view_sql=$(awk '
 /^CREATE VIEW defaultdb.jandibat_backup_admin.schedule_policy_v1 AS$/ {copy=1}
 copy {
  if (/^ OR .*;"$/) {sub(/"$/, ""); print; done=1; exit}
  print
 }
 END {if (!done) exit 1}
' "$root/scripts/db-bootstrap-roles.sh") || fail 'view source extraction'
sql_as root "CREATE DATABASE jandibat;
CREATE SCHEMA defaultdb.jandibat_backup_admin AUTHORIZATION root;
SET CLUSTER SETTING jobs.scheduler.enabled = false;
CREATE USER jandibat_backup_runner;
CREATE USER jandibat_backup_verifier;
CREATE EXTERNAL CONNECTION jandibat_backup_v1 AS 'nodelocal://1/h2-view-probe';
GRANT BACKUP ON DATABASE jandibat TO jandibat_backup_runner;
GRANT SYSTEM EXTERNALIOIMPLICITACCESS TO jandibat_backup_runner;
GRANT USAGE ON EXTERNAL CONNECTION jandibat_backup_v1 TO jandibat_backup_runner;
SET allow_unsafe_internals = true;
$view_sql
GRANT USAGE ON SCHEMA defaultdb.jandibat_backup_admin TO jandibat_backup_runner, jandibat_backup_verifier;
GRANT SELECT ON TABLE defaultdb.jandibat_backup_admin.schedule_policy_v1 TO jandibat_backup_runner, jandibat_backup_verifier;"

cat >"$test_dir/cockroach-wrapper" <<'WRAPPER'
#!/bin/sh
exec docker exec -i "$TEST_CONTAINER" /cockroach/cockroach "$@" \
 --insecure --host=127.0.0.1:26257 --user=jandibat_backup_runner
WRAPPER
chmod 700 "$test_dir/cockroach-wrapper"
for run in 1 2; do
 if ! env TEST_CONTAINER="$container" COCKROACH_SQL_BIN="$test_dir/cockroach-wrapper" \
  BACKUP_RUNNER_DATABASE_URL='postgresql://jandibat_backup_runner@127.0.0.1:26257/jandibat?sslmode=disable' \
  sh "$root/scripts/db-configure-backup-schedule.sh" >"$test_dir/output" 2>"$test_dir/error"; then
  reason=$(sed -n 's/^backup schedule .* (\([a-z-]*\))$/\1/p' "$test_dir/error")
  [ -z "$reason" ] || fail "runner schedule $reason phase"
  fail 'runner schedule preflight'
 fi
done
sql_as root "CREATE SCHEDULE jandibat_backup_schedule_v1
 FOR BACKUP DATABASE jandibat INTO 'nodelocal://1/other-owner-label'
 WITH revision_history RECURRING '10 * * * *' FULL BACKUP '10 0 * * *';
CREATE SCHEDULE other_owner_destination
 FOR BACKUP DATABASE jandibat INTO 'external://jandibat_backup_v1'
 WITH revision_history RECURRING '10 * * * *' FULL BACKUP '10 0 * * *';"

read_view() {
 actor=$1; output=$2
 sql_as "$actor" 'SELECT * FROM defaultdb.jandibat_backup_admin.schedule_policy_v1 ORDER BY schedule_id;'
 cp "$test_dir/output" "$output"
}
read_view jandibat_backup_runner "$test_dir/runner"
read_view jandibat_backup_verifier "$test_dir/verifier"
cmp -s "$test_dir/runner" "$test_dir/verifier" || fail 'runner/verifier projections differ'
if grep -Eq 'BACKUP DATABASE|external://|nodelocal://|jandibat_backup_runner|Waiting for initial backup' "$test_dir/runner"; then
 fail 'projection contains raw command, URI, owner or state'
fi
awk -F '\t' '
 NR==1 {if (NF!=14 || $1!="schedule_id" || $14!="full_command_ok") bad=1; next}
 NF!=14 {bad=1; next}
 $1 !~ /^[0-9]+$/ || length($1)<16 {bad=1}
 ($4=="true" || $4=="t") && ($5=="true" || $5=="t") {
  runner++
  if ($13=="true" || $13=="t") {inc_id=$1 ""; dep_id=$2 ""; inc++}
  if ($14=="true" || $14=="t") {full_id=$1 ""; unpause_id=$3 ""; full++}
 }
 ($4=="true" || $4=="t") && ($5=="false" || $5=="f") {foreign_label++}
 ($4=="false" || $4=="f") && ($5=="false" || $5=="f") {foreign_destination++}
 ($12!="true" && $12!="t") {bad=1}
 END {exit bad || NR!=7 || runner!=2 || foreign_label!=2 || foreign_destination!=2 ||
  inc!=1 || full!=1 || inc_id==full_id || dep_id!=full_id || unpause_id!=inc_id}
' "$test_dir/runner" || fail 'populated candidate projection'

# A root-only fixture mutation must be visible as metric_disabled=false to
# both low-privilege identities. Its ID remains an exact decimal string.
full_id=$(awk -F '\t' 'NR>1 && ($5=="true"||$5=="t") && ($14=="true"||$14=="t") {print $1}' "$test_dir/runner")
case "$full_id" in ''|*[!0-9]*) fail 'full schedule ID invalid';; esac
sql_as root "ALTER BACKUP SCHEDULE $full_id SET SCHEDULE OPTION updates_cluster_last_backup_time_metric;"
read_view jandibat_backup_runner "$test_dir/runner-after"
read_view jandibat_backup_verifier "$test_dir/verifier-after"
cmp -s "$test_dir/runner-after" "$test_dir/verifier-after" || fail 'post-mutation projections differ'
awk -F '\t' 'NR>1 && ($5=="true"||$5=="t") {if ($12!="false" && $12!="f") bad=1; rows++} END {exit bad || rows!=2}' "$test_dir/runner-after" || fail 'metric option true was not exposed'
denied_as jandibat_backup_runner
denied_as jandibat_backup_verifier
echo "populated schedule view projection and raw-table denial passed ($evidence_label)"
