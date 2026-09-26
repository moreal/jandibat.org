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

fixture_percent_encode() {
 # Encode bytes without placing a credential in a process argument.
 printf '%s' "$1" | od -v -An -tx1 | awk '
  { for (i=1; i<=NF; i++) {
      h=tolower($i); n=(index("0123456789abcdef",substr(h,1,1))-1)*16+index("0123456789abcdef",substr(h,2,1))-1
      if ((n>=48 && n<=57) || (n>=65 && n<=90) || (n>=97 && n<=122) || n==45 || n==46 || n==95 || n==126)
        printf "%c", n
      else printf "%%%s", toupper(h)
    }
  }
 '
}

fixture_has_sentinel() {
 target=$1
 [ -e "$target" ] || return 2
 for sentinel in "${synthetic_secret:-}" "${synthetic_access:-}" "${rotated_secret:-}" "${keystore_password:-}" \
  "${password_a:-}" "${password_b:-}" "${password_c:-}" "${password_d:-}" \
  "${password_e:-}" "${password_f:-}" "${password_g:-}"; do
  [ -n "$sentinel" ] || continue
  encoded=$(fixture_percent_encode "$sentinel") || return 2
  for candidate in "$sentinel" "$encoded"; do
   if [ -d "$target" ]; then
    grep -R -Fq "$candidate" "$target"
   else
    grep -Fq "$candidate" "$target"
   fi
   result=$?
   [ "$result" -eq 0 ] && return 0
   [ "$result" -eq 1 ] || return 2
  done
 done
 return 1
}

fixture_quiesce_collect_logs() {
 name=$1
 log_dir=$2
 result=0
 docker stop "$name" >/dev/null 2>&1 || result=1
 docker wait "$name" >/dev/null 2>&1 || result=1
 docker cp "$name:/cockroach/cockroach-data/logs/." "$log_dir/" >/dev/null 2>&1 || result=1
 docker logs "$name" >"$log_dir/container.log" 2>&1 || result=1
 server_log=$(find "$log_dir" -type f -name 'cockroach*' -print -quit 2>/dev/null) || result=1
 [ -n "$server_log" ] || result=1
 if [ "$result" -ne 0 ]; then
  echo 'RED: secure fixture final log collection or cleanup incomplete (details redacted)' >&2
 fi
 return "$result"
}

fixture_effective_log_groups() {
 awk '
  BEGIN {
   expected["default"]=1; expected["ops"]=1; expected["sql-audit"]=1
   expected["security"]=1; expected["sql-auth"]=1; expected["sql-exec"]=1
  }
  {
   line=$0; sub(/\r$/, "", line)
   indent=match(line, /[^ ]/) - 1
   sub(/^[ ]+/, "", line); sub(/[ ]+$/, "", line)
   if (line=="" || line ~ /^#/) next
   if (in_groups && indent<=groups_indent) in_groups=0
   if (in_sinks && indent<=sinks_indent && line!="sinks:") in_sinks=0
   if (in_capture && indent<=capture_indent && line!="capture-stray-errors:") in_capture=0
   if (in_groups && indent==groups_indent+2 && line ~ /^[a-z][a-z0-9-]*:$/) {
    name=line; sub(/:$/, "", name)
    if (!(name in expected) || ++seen[name]!=1) bad=1
   }
   if (in_sinks && indent==sinks_indent+2) {
    if (line=="file-groups:") { in_groups=1; groups_indent=indent }
    if (line=="stderr:") stderr=1
    if (line ~ /^(fluent-servers|http-servers|otlp-servers):/) bad=1
   }
   if (in_capture && indent==capture_indent+2 && line=="enable: true") stray=1
   if (line=="sinks:") { in_sinks=1; sinks_indent=indent }
   if (line=="capture-stray-errors:") { in_capture=1; capture_indent=indent }
  }
  END {
   for (name in expected) if (seen[name]!=1) bad=1
   if (bad || !stderr || !stray) exit 1
   for (name in expected) print name
  }
 ' "$1"
}

fixture_check_log_sinks() {
 effective=$1
 log_dir=$2
 groups=$(fixture_effective_log_groups "$effective") || return 1
 for group in $groups; do
  if [ "$group" = default ]; then prefix='cockroach.'; else prefix="cockroach-$group."; fi
  found=false
  nonempty=false
  for file in "$log_dir"/"$prefix"*.log; do
   [ -e "$file" ] || continue
   [ -f "$file" ] && [ -r "$file" ] || return 1
   found=true
   [ -s "$file" ] && nonempty=true
  done
  [ "$found" = true ] || return 1
  # OPS WARNING is quiet on healthy runs; other configured channels are
  # deliberately exercised by the fixture and must contain events.
  if [ "$group" != ops ] && [ "$nonempty" != true ]; then return 1; fi
 done
 # Stray capture and container stderr may be empty, but both must be copied.
 for file in "$log_dir/cockroach-stderr.log" "$log_dir/container.log"; do
  [ -f "$file" ] && [ -r "$file" ] || return 1
 done
}

fixture_audit_markers() {
 log_dir=$1
 for specification in \
  'cockroach-sql-exec.*|CREATE EXTERNAL CONNECTION' \
  'cockroach-security.*|USER_ADMIN|PRIVILEGES' \
  'cockroach-sql-audit.*|SENSITIVE_ACCESS|42501'; do
  pattern=${specification%%|*}
  markers=${specification#*|}
  found=false
  for file in "$log_dir"/$pattern; do
   [ -f "$file" ] || continue
   if [ "$pattern" = 'cockroach-sql-audit.*' ]; then
    grep -Fq 'SENSITIVE_ACCESS' "$file" &&
     grep -Eq '42501|denied|insufficient privilege' "$file" && found=true
   elif [ "$pattern" = 'cockroach-security.*' ]; then
    grep -Eq 'USER_ADMIN|PRIVILEGES' "$file" && found=true
   else
    grep -Fq "$markers" "$file" && found=true
   fi
  done
  [ "$found" = true ] || return 1
 done
 return 0
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

fixture_check_defaultdb_grants() {
 awk -F '\t' '
  NR==1 { if ($1!="grantee" || $2!="privilege_type" || $3!="is_grantable") bad=1; next }
  $1=="jandibat_backup_bootstrap" && $2=="CONNECT" && ($3=="false" || $3=="f") { allowed++; next }
  { bad=1 }
  END { exit bad || allowed!=1 }
 ' "$1"
}

fixture_check_metadata_grants() {
 kind=$1
 file=$2
 awk -F '\t' -v kind="$kind" '
  NR==1 { if ($1!="grantee" || $2!="privilege_type" || $3!="is_grantable") bad=1; next }
  $1=="jandibat_backup_bootstrap" && ($3=="false" || $3=="f") && (
   (kind=="schema" && $2=="USAGE") ||
   (kind=="table" && ($2=="SELECT" || $2=="INSERT")) ||
   (kind=="view" && $2=="SELECT")) { seen[$2]++; next }
  { bad=1 }
  END {
   if (kind=="table") exit bad || seen["SELECT"]!=1 || seen["INSERT"]!=1
   if (kind=="schema") exit bad || seen["USAGE"]!=1
   exit bad || seen["SELECT"]!=1
  }
 ' "$file"
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
