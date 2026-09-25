#!/bin/sh
# Sourced only by the disposable Linux fixture. Do not export root material to
# phase-specific SQL clients.

fixture_write_env() {
 umask 077
 {
  printf 'COCKROACH_SQL_BIN=/cockroach/cockroach\nCOCKROACH_ROOT_URL=%s\n' "$FIXTURE_ROOT_URL"
  printf 'MIGRATION_DATABASE_URL=%s\nAPI_DATABASE_URL=%s\n' "$FIXTURE_MIGRATION_URL" "$FIXTURE_API_URL"
  printf 'WORKER_DATABASE_URL=%s\nMAINTENANCE_DATABASE_URL=%s\n' "$FIXTURE_WORKER_URL" "$FIXTURE_MAINTENANCE_URL"
  printf 'BACKUP_BOOTSTRAP_DATABASE_URL=%s\nBACKUP_RUNNER_DATABASE_URL=%s\nBACKUP_VERIFIER_DATABASE_URL=%s\n' \
   "$FIXTURE_BOOTSTRAP_URL" "$FIXTURE_RUNNER_URL" "$FIXTURE_VERIFIER_URL"
  printf 'JANDIBAT_MIGRATOR_PASSWORD=%s\nJANDIBAT_API_PASSWORD=%s\n' "$FIXTURE_PASS_A" "$FIXTURE_PASS_B"
  printf 'JANDIBAT_WORKER_PASSWORD=%s\nJANDIBAT_MAINTENANCE_PASSWORD=%s\n' "$FIXTURE_PASS_C" "$FIXTURE_PASS_D"
  printf 'JANDIBAT_BACKUP_BOOTSTRAP_PASSWORD=%s\nJANDIBAT_BACKUP_RUNNER_PASSWORD=%s\nJANDIBAT_BACKUP_VERIFIER_PASSWORD=%s\n' \
   "$FIXTURE_PASS_E" "$FIXTURE_PASS_F" "$FIXTURE_PASS_G"
 } >"$FIXTURE_DIR/root.env"
 {
  printf 'COCKROACH_SQL_BIN=/cockroach/cockroach\nBACKUP_BOOTSTRAP_DATABASE_URL=%s\n' "$FIXTURE_BOOTSTRAP_URL"
  printf 'BACKUP_S3_ENDPOINT=https://127.0.0.1:9009\nBACKUP_S3_REGION=us-east-1\n'
  printf 'BACKUP_S3_BUCKET=disposable-backup\nBACKUP_S3_PREFIX=fixture-only\nBACKUP_S3_PATH_STYLE=true\n'
  printf 'BACKUP_S3_ACCESS_KEY_ID=%s\nBACKUP_S3_SECRET_ACCESS_KEY=%s\n' "$FIXTURE_ACCESS" "$FIXTURE_SECRET"
 } >"$FIXTURE_DIR/bootstrap.env"
 printf 'COCKROACH_SQL_BIN=/cockroach/cockroach\nBACKUP_RUNNER_DATABASE_URL=%s\n' "$FIXTURE_RUNNER_URL" >"$FIXTURE_DIR/runner.env"
 printf 'COCKROACH_SQL_BIN=/cockroach/cockroach\nBACKUP_VERIFIER_DATABASE_URL=%s\n' "$FIXTURE_VERIFIER_URL" >"$FIXTURE_DIR/verifier.env"
}

fixture_client() {
 identity=$1
 shift
 case "$identity" in
  root) env_file="$FIXTURE_DIR/root.env"; cert_dir="$FIXTURE_DIR/certs" ;;
  bootstrap|runner|verifier) env_file="$FIXTURE_DIR/$identity.env"; cert_dir="$FIXTURE_DIR/ca-only" ;;
  *) echo 'unknown fixture identity' >&2; return 2 ;;
 esac
 docker run --rm -i --network host --env-file "$env_file" \
  --mount "type=bind,src=$cert_dir,dst=/certs,readonly" \
  --mount "type=bind,src=$FIXTURE_DIR/workspace,dst=/workspace,readonly" \
  --entrypoint /bin/sh "$FIXTURE_IMAGE" "$@"
}

fixture_stop_container() {
 name=$1
 docker stop "$name" >/dev/null 2>&1 || :
 for _ in 1 2 3 4 5; do
  if ! docker container inspect "$name" >/dev/null 2>&1; then
   if docker info >/dev/null 2>&1; then return 0; fi
   echo 'RED: secure fixture container cleanup incomplete (Docker unavailable)' >&2
   return 1
  fi
  sleep 1
 done
 echo 'RED: secure fixture container cleanup incomplete (container remains)' >&2
 return 1
}

fixture_check_system_grants() {
 awk -F '\t' '
  NR==1 { if ($1!="grantee" || $2!="privilege_type" || $3!="is_grantable") bad=1; next }
  $1=="jandibat_backup_bootstrap" && $2=="EXTERNALCONNECTION" && ($3=="false" || $3=="f") { allowed++; next }
  { bad=1 }
  END { exit bad || allowed!=1 }
 ' "$1"
}

fixture_check_database_grants() {
 awk -F '\t' '
  NR==1 { if ($1!="grantee" || $2!="privilege_type" || $3!="is_grantable") bad=1; next }
  $1=="jandibat_backup_runner" && $2=="BACKUP" && ($3=="false" || $3=="f") { allowed++; next }
  { bad=1 }
  END { exit bad || allowed!=1 }
 ' "$1"
}

fixture_check_connection_grants() {
 # Unfiltered v26.2 SHOW GRANTS includes the built-in root ALL row even
 # when the external connection was created by the bootstrap identity.
 awk -F '\t' '
  NR==1 { if ($1!="grantee" || $2!="privilege_type" || $3!="is_grantable") bad=1; next }
  $1=="root" && $2=="ALL" && ($3=="false" || $3=="f") { root_all++; next }
  $1=="jandibat_backup_bootstrap" && $2=="DROP" && ($3=="true" || $3=="t") { owner_drop++; next }
  $1=="jandibat_backup_bootstrap" && $2=="USAGE" && ($3=="true" || $3=="t") { owner_usage++; next }
  $1=="jandibat_backup_runner" && $2=="USAGE" && ($3=="false" || $3=="f") { runner_usage++; next }
  $1=="jandibat_backup_verifier" && $2=="USAGE" && ($3=="false" || $3=="f") { verifier_usage++; next }
  { bad=1 }
  END { exit bad || root_all!=1 || owner_drop!=1 || owner_usage!=1 || runner_usage!=1 || verifier_usage!=1 }
 ' "$1"
}
