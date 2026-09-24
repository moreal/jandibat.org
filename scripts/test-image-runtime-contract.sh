#!/bin/sh
set -eu

# This is a host-side contract check. Linux container smoke is in
# test-image-contract.sh; this check never labels a Darwin run as container QA.
test -f docs/IMAGE_RUNTIME_CONTRACT.ko.md || {
	echo 'missing image runtime contract document' >&2
	exit 1
}

# The table is consumed by deployment authors before manifests exist. Pin its
# exact role/port split and the parser's two deliberately different encodings.
node - <<'NODE'
const assert = require('node:assert/strict');
const fs = require('node:fs');
const runtime = fs.readFileSync('docs/IMAGE_RUNTIME_CONTRACT.ko.md', 'utf8');
const configuration = fs.readFileSync('docs/CONFIGURATION.ko.md', 'utf8');
const rows = runtime.split('\n').filter(line => line.startsWith('| '));
for (const [name, port, variable, role] of [
  ['API', 8080, 'DATABASE_URL', 'jandibat_api'],
  ['Sync worker', 8081, 'WORKER_DATABASE_URL', 'jandibat_worker'],
  ['Maintenance', 8082, 'MAINTENANCE_DATABASE_URL', 'jandibat_maintenance'],
]) {
  const row = rows.find(line => line.startsWith(`| ${name} `));
  assert.ok(row, `${name} runtime row is missing`);
  assert.match(row, new RegExp(`:${port}\\b`), `${name} port drifted`);
  assert.ok(row.includes(`\`${variable}\``) && row.includes(`\`${role}\``), `${name} DB role drifted`);
  assert.ok(!row.includes('MIGRATION_DATABASE_URL'), `${name} received a migrator DSN`);
}
const migration = rows.find(line => line.startsWith('| Migration Job '));
assert.ok(migration?.includes('MIGRATION_DATABASE_URL') && migration.includes('jandibat_migrator'));
assert.match(configuration, /DELETED_IDENTITY_HMAC_KEYS[^\n]*정확히 32바이트의 unpadded standard base64/);
assert.match(configuration, /DELETION_PSEUDONYM_KEY[^\n]*unpadded base64url/);
assert.match(runtime, /127\.0\.0\.1:8080\/metrics/);
console.log('documented runtime role, port and key encoding contract passed');
NODE

scratch=$(mktemp -d)
trap 'rm -r "$scratch"' EXIT HUP INT TERM

# Generate unrelated, otherwise-valid production keys in a protected scratch
# directory. Their values never appear in source, stdout, or failure messages.
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$scratch/key.pem" 2>/dev/null
openssl pkey -in "$scratch/key.pem" -pubout -outform DER 2>/dev/null \
	| openssl base64 -A | tr -d '=' >"$scratch/public.b64"
openssl pkcs8 -topk8 -nocrypt -in "$scratch/key.pem" -outform DER 2>/dev/null \
	| openssl base64 -A | tr -d '=' >"$scratch/private.b64"
openssl rand -base64 32 | tr -d '\n=' >"$scratch/session.b64"
openssl rand -base64 32 | tr -d '\n=' >"$scratch/hmac.b64"
openssl rand -base64 32 | tr -d '\n=' | tr '+/' '-_' >"$scratch/pseudonym.b64"
public_map=$(printf '{"current":"%s"}' "$(tr -d '\n' <"$scratch/public.b64")")
private_map=$(printf '{"current":"%s"}' "$(tr -d '\n' <"$scratch/private.b64")")
hmac_map=$(printf '{"identity-current":"%s"}' "$(tr -d '\n' <"$scratch/hmac.b64")")
session_key=$(tr -d '\n' <"$scratch/session.b64")
pseudonym_key=$(tr -d '\n' <"$scratch/pseudonym.b64")
printf '%s\n' 'secret-not-for-logs' 'oauth-not-for-logs' 'smtp-not-for-logs' \
	"$session_key" "$pseudonym_key" \
	"$(tr -d '\n' <"$scratch/public.b64")" \
	"$(tr -d '\n' <"$scratch/private.b64")" \
	"$(tr -d '\n' <"$scratch/hmac.b64")" >"$scratch/secret-patterns"

# A Go overlay keeps the exact error-reason probe out of Backend-owned source.
# Process logs correctly reveal only a bounded error type, so the probe checks
# the real config/revoker functions on the same environment as each binary.
node - "$scratch" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const scratch = process.argv[2];
const moduleRoot = path.resolve('apps/api');
const server = path.join(scratch, 'server_probe_test.go');
const worker = path.join(scratch, 'worker_probe_test.go');
fs.writeFileSync(server, `package main
import (
  "os"
  "strings"
  "testing"
  "github.com/moreal/jandibat.org/apps/api/internal/config"
)
func TestTask3RuntimeProbe(t *testing.T) {
  process := os.Getenv("TASK3_PROBE_PROCESS")
  settings, err := config.LoadForProcess(os.LookupEnv, process)
  if err == nil && process == config.ProcessAPI { err = validateRuntimeConfig(settings) }
  want := os.Getenv("TASK3_PROBE_REASON")
  if want == "valid" {
    if err != nil { t.Fatal("otherwise-valid production configuration was rejected") }
    return
  }
  if err == nil || !strings.Contains(err.Error(), want) {
    t.Fatalf("expected sanitized config failure for %s", want)
  }
}
`);
fs.writeFileSync(worker, `package main
import (
  "context"
  "net/http"
  "os"
  "strings"
  "testing"
  "time"
  "github.com/moreal/jandibat.org/apps/api/internal/config"
)
func TestTask3WorkerOAuthProbe(t *testing.T) {
  settings, err := config.LoadForProcess(os.LookupEnv, config.ProcessWorker)
  if err == nil && os.Getenv("TASK3_PROBE_LIVE_DB") == "1" {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    app, buildErr := buildWorker(ctx, settings, nil)
    err = buildErr
    if app != nil { defer app.Close() }
  } else if err == nil {
    _, err = buildWorkerOAuthRevoker(settings, &http.Client{})
  }
  want := os.Getenv("TASK3_PROBE_REASON")
  if want == "valid" {
    if err != nil { t.Fatal("otherwise-valid worker OAuth configuration was rejected") }
    return
  }
  if err == nil || !strings.Contains(err.Error(), want) {
    t.Fatalf("expected sanitized worker OAuth failure for %s", want)
  }
}
`);
fs.writeFileSync(path.join(scratch, 'overlay.json'), JSON.stringify({Replace: {
  [path.join(moduleRoot, 'cmd/server/task3_runtime_probe_test.go')]: server,
  [path.join(moduleRoot, 'cmd/worker/task3_runtime_probe_test.go')]: worker,
}}));
NODE

for spec in server worker maintenance; do
	(cd apps/api && go build -o "$scratch/$spec" "./cmd/$spec")
done

with_production_env() {
	process=$1 omitted=$2
	shift 2
	case "$process" in
		api)
			env -i PATH="$PATH" HOME="$HOME" APP_ENV=production API_ADDR=127.0.0.1:0 \
				PUBLIC_BASE_URL=https://api.example.test WEB_BASE_URL=https://web.example.test \
				'DATABASE_URL=postgresql://jandibat_api:secret-not-for-logs@127.0.0.1:1/jandibat?sslmode=require' \
				SESSION_SIGNING_KEY="$session_key" CREDENTIAL_ACTIVE_KEY_ID=current \
				CREDENTIAL_ENCRYPTION_PUBLIC_KEYS="$public_map" \
				DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID=identity-current DELETED_IDENTITY_HMAC_KEYS="$hmac_map" \
				GITHUB_CLIENT_ID=github-client GITHUB_CLIENT_SECRET=oauth-not-for-logs \
				GITLAB_CLIENT_ID=gitlab-client GITLAB_CLIENT_SECRET=oauth-not-for-logs-2 \
				CODEBERG_CLIENT_ID=codeberg-client CODEBERG_CLIENT_SECRET=oauth-not-for-logs-3 \
				env -u "$omitted" "$@"
			;;
		worker)
			env -i PATH="$PATH" HOME="$HOME" APP_ENV=production WORKER_HEALTH_ADDR=127.0.0.1:0 \
				PUBLIC_BASE_URL=https://api.example.test WEB_BASE_URL=https://web.example.test \
				"WORKER_DATABASE_URL=${TASK3_WORKER_OAUTH_TEST_DSN:-postgresql://jandibat_worker:secret-not-for-logs@127.0.0.1:1/jandibat?sslmode=require}" \
				CREDENTIAL_ACTIVE_KEY_ID=current CREDENTIAL_ENCRYPTION_PUBLIC_KEYS="$public_map" \
				CREDENTIAL_ENCRYPTION_PRIVATE_KEYS="$private_map" \
				SMTP_ADDR=smtp.example.test:587 SMTP_USERNAME=mailer SMTP_PASSWORD=smtp-not-for-logs \
				SMTP_FROM=mailer@example.test \
				GITHUB_CLIENT_ID=github-client GITHUB_CLIENT_SECRET=oauth-not-for-logs \
				GITLAB_CLIENT_ID=gitlab-client GITLAB_CLIENT_SECRET=oauth-not-for-logs-2 \
				env -u "$omitted" "$@"
			;;
		maintenance)
			env -i PATH="$PATH" HOME="$HOME" APP_ENV=production MAINTENANCE_HEALTH_ADDR=127.0.0.1:0 \
				PUBLIC_BASE_URL=https://api.example.test WEB_BASE_URL=https://web.example.test \
				'MAINTENANCE_DATABASE_URL=postgresql://jandibat_maintenance:secret-not-for-logs@127.0.0.1:1/jandibat?sslmode=require' \
				CREDENTIAL_ACTIVE_KEY_ID=current CREDENTIAL_ENCRYPTION_PUBLIC_KEYS="$public_map" \
				CREDENTIAL_ENCRYPTION_PRIVATE_KEYS="$private_map" \
				DELETION_PSEUDONYM_KEY="$pseudonym_key" \
				DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID=identity-current DELETED_IDENTITY_HMAC_KEYS="$hmac_map" \
				env -u "$omitted" "$@"
			;;
	esac
}

probe_config() {
	process=$1 omitted=$2 reason=$3
	with_production_env "$process" "$omitted" \
		env TASK3_PROBE_PROCESS="$process" TASK3_PROBE_REASON="$reason" \
		sh -c 'cd apps/api && go test -overlay="$1" ./cmd/server -run "^TestTask3RuntimeProbe$" -count=1' sh "$scratch/overlay.json" \
		>"$scratch/probe.log" 2>&1 || {
			echo "$process/$omitted config probe failed" >&2
			sed -n '1,30p' "$scratch/probe.log" >&2
			exit 1
		}
}

assert_binary_rejects() {
	process=$1 omitted=$2
	program=$process
	[ "$process" = api ] && program=server
	set +e
	with_production_env "$process" "$omitted" timeout 8s "$scratch/$program" >"$scratch/$process.log" 2>&1
	status=$?
	set -e
	if [ "$status" -ne 1 ]; then
		echo "$process/$omitted exited $status instead of failing closed" >&2
		exit 1
	fi
	case "$process" in maintenance) stopped=maintenance.stopped ;; *) stopped=$process.process_stopped ;; esac
	if ! grep -F "$stopped" "$scratch/$process.log" >/dev/null || \
		! grep -F 'failure_type' "$scratch/$process.log" >/dev/null || \
		grep -E '(^|[.])listening' "$scratch/$process.log" >/dev/null || \
		grep -F -f "$scratch/secret-patterns" "$scratch/$process.log" >/dev/null; then
		echo "$process/$omitted leaked a secret, opened a listener, or lacked a bounded failure" >&2
		exit 1
	fi
}

# First prove the fixture is valid; each omission must then fail for its own
# reason instead of being masked by the public-key or database check.
for process in api worker maintenance; do probe_config "$process" TASK3_UNUSED_ENV valid; done
for spec in \
	'api:SESSION_SIGNING_KEY:SESSION_SIGNING_KEY needs at least 32 bytes' \
	'api:CREDENTIAL_ENCRYPTION_PUBLIC_KEYS:CREDENTIAL_ACTIVE_KEY_ID must select a key' \
	'api:DELETED_IDENTITY_HMAC_KEYS:DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID must select a 32-byte key' \
	'api:GITHUB_CLIENT_SECRET:complete OAuth configuration is required' \
	'worker:CREDENTIAL_ENCRYPTION_PRIVATE_KEYS:CREDENTIAL_ACTIVE_KEY_ID must select a key' \
	'worker:SMTP_PASSWORD:worker requires the complete SMTP configuration' \
	'maintenance:CREDENTIAL_ENCRYPTION_PRIVATE_KEYS:CREDENTIAL_ACTIVE_KEY_ID must select a key' \
	'maintenance:DELETION_PSEUDONYM_KEY:DELETION_PSEUDONYM_KEY needs at least 32 bytes' \
	'maintenance:DELETED_IDENTITY_HMAC_KEYS:DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID must select a 32-byte key'; do
	process=${spec%%:*} rest=${spec#*:} omitted=${rest%%:*} reason=${rest#*:}
	probe_config "$process" "$omitted" "$reason"
	assert_binary_rejects "$process" "$omitted"
	printf '%s\n' "$process/$omitted: exact config reason and safe process exit"
done

# Worker OAuth validation currently follows DB opening. Its exact failure
# is exercised directly; process-level proof needs a live isolated DB.
with_production_env worker GITHUB_CLIENT_SECRET \
	env TASK3_PROBE_REASON='GitHub and GitLab OAuth credentials are required' \
	sh -c 'cd apps/api && go test -overlay="$1" ./cmd/worker -run "^TestTask3WorkerOAuthProbe$" -count=1' sh "$scratch/overlay.json" \
	>"$scratch/probe.log" 2>&1 || {
		echo 'worker OAuth revoker probe failed' >&2
		sed -n '1,30p' "$scratch/probe.log" >&2
		exit 1
	}
printf '%s\n' 'worker/GITHUB_CLIENT_SECRET: exact revoker error'
if [ -n "${TASK3_WORKER_OAUTH_TEST_DSN:-}" ]; then
	for reason in valid 'GitHub and GitLab OAuth credentials are required'; do
		omitted=TASK3_UNUSED_ENV
		[ "$reason" = valid ] || omitted=GITHUB_CLIENT_SECRET
		with_production_env worker "$omitted" \
			env TASK3_PROBE_REASON="$reason" TASK3_PROBE_LIVE_DB=1 \
			sh -c 'cd apps/api && go test -overlay="$1" ./cmd/worker -run "^TestTask3WorkerOAuthProbe$" -count=1' sh "$scratch/overlay.json" \
			>"$scratch/probe.log" 2>&1 || {
				echo "worker live-DB composition failed for $omitted" >&2
				sed -n '1,30p' "$scratch/probe.log" >&2
				exit 1
			}
	done
	assert_binary_rejects worker GITHUB_CLIENT_SECRET
	printf '%s\n' 'worker/GITHUB_CLIENT_SECRET: full live-DB composition and real process fail closed'
fi

# The migration Job must supply its own DSN, database name and mounted SQL.
# These checks exercise the actual runner's preflight, without connecting to
# or modifying any database.
assert_migration_failure() {
	if ! grep -F "$1" "$scratch/migrate.log" >/dev/null; then
		echo "migration preflight did not report $1" >&2
		exit 1
	fi
	if grep -F -f "$scratch/secret-patterns" "$scratch/migrate.log" >/dev/null || \
		grep -F 'secret-migrator-not-for-logs' "$scratch/migrate.log" >/dev/null; then
		echo 'migration preflight exposed a secret' >&2
		exit 1
	fi
}
if env -i PATH="$PATH" sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted a missing DSN' >&2
	exit 1
fi
assert_migration_failure 'MIGRATION_DATABASE_URL is required'
if env -i PATH="$PATH" 'DATABASE_URL=postgresql://root@127.0.0.1:1/jandibat?sslmode=disable' \
	sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted the API DATABASE_URL fallback' >&2
	exit 1
fi
assert_migration_failure 'MIGRATION_DATABASE_URL is required'
if env -i PATH="$PATH" MIGRATION_DATABASE_URL=secret-migrator-not-for-logs \
	sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted a missing database name' >&2
	exit 1
fi
assert_migration_failure 'COCKROACH_DATABASE is required'
if env -i PATH="$PATH" MIGRATION_DATABASE_URL=secret-migrator-not-for-logs \
	COCKROACH_DATABASE=jandibat sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted a missing migration mount path' >&2
	exit 1
fi
assert_migration_failure 'MIGRATIONS_DIR is required'
if env -i PATH="$PATH" MIGRATION_DATABASE_URL=secret-migrator-not-for-logs \
	COCKROACH_DATABASE=invalid-name MIGRATIONS_DIR=db/migrations \
	sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted an invalid database name' >&2
	exit 1
fi
assert_migration_failure 'COCKROACH_DATABASE must contain'
if env -i PATH="$PATH" MIGRATION_DATABASE_URL=secret-migrator-not-for-logs \
	COCKROACH_DATABASE=jandibat MIGRATIONS_DIR="$scratch/missing" \
	sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted an absent migration mount' >&2
	exit 1
fi
assert_migration_failure 'migration directory not found'
if env -i PATH="$PATH" MIGRATION_DATABASE_URL=secret-migrator-not-for-logs \
	COCKROACH_DATABASE=jandibat MIGRATIONS_DIR=db/migrations \
	TMPDIR="$scratch/missing-temporary-directory" \
	sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted an unwritable temporary directory' >&2
	exit 1
fi
assert_migration_failure 'writable temporary directory is required'
if env -i PATH="$PATH" MIGRATION_DATABASE_URL=secret-migrator-not-for-logs \
	COCKROACH_DATABASE=jandibat MIGRATIONS_DIR=db/migrations \
	COCKROACH_SQL_BIN=missing-cockroach-cli-for-test \
	sh scripts/db-migrate-url.sh >"$scratch/migrate.log" 2>&1; then
	echo 'migration runner accepted an absent Cockroach CLI' >&2
	exit 1
fi
assert_migration_failure 'Cockroach SQL client not found'

# Real Go boundary tests cover the process-scoped key parser, DB-role/port
# defaults, independent health probes, and physical-peer metrics policy.
(cd apps/api && go test ./internal/config ./internal/processruntime ./internal/http ./cmd/server ./cmd/worker ./cmd/maintenance)

echo 'host runtime contract checks passed; Linux container smoke is separate'
