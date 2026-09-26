#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

invalid() { echo 'backup connection input or policy invalid' >&2; exit 2; }
failed() { echo 'backup connection operation failed' >&2; exit 1; }

for value in "${BACKUP_BOOTSTRAP_DATABASE_URL:-}" "${BACKUP_S3_ENDPOINT:-}" \
 "${BACKUP_S3_REGION:-}" "${BACKUP_S3_BUCKET:-}" "${BACKUP_S3_PREFIX:-}" \
 "${BACKUP_S3_PATH_STYLE:-}" "${BACKUP_S3_ACCESS_KEY_ID:-}" \
 "${BACKUP_S3_SECRET_ACCESS_KEY:-}"; do
 [ -n "$value" ] || invalid
 if ! printf '%s' "$value" | od -v -An -tu1 | awk '
  { for (i=1; i<=NF; i++) if ($i < 32 || $i == 127) bad=1 }
  END { exit bad }
 '; then invalid; fi
done

case "$BACKUP_S3_ENDPOINT" in https://*) authority=${BACKUP_S3_ENDPOINT#https://};; *) invalid;; esac
case "$authority" in ''|*/*|*\?*|*\#*|*@*|*%*|*:*:*|*[!A-Za-z0-9.:-]*) invalid;; esac
case "$authority" in
 *:*) host=${authority%:*}; port=${authority##*:}
      case "$port" in ''|*[!0-9]*) invalid;; esac
      [ "$port" -ge 1 ] 2>/dev/null && [ "$port" -le 65535 ] 2>/dev/null || invalid;;
 *) host=$authority;;
esac
case "$host" in ''|.*|*.|*..*) invalid;; esac
remaining=$host
while :; do
 label=${remaining%%.*}
 case "$label" in ''|-*|*-|*[!A-Za-z0-9-]*) invalid;; esac
 case "$remaining" in *.*) remaining=${remaining#*.};; *) break;; esac
done
case "$BACKUP_S3_REGION" in *[!A-Za-z0-9-]*|'') invalid;; esac
case "$BACKUP_S3_BUCKET" in *[!a-z0-9.-]*|''|.*|*.|*..*) invalid;; esac
case "$BACKUP_S3_PREFIX" in *[!A-Za-z0-9/_-]*|''|/*|*/|*//* ) invalid;; esac
case "/$BACKUP_S3_PREFIX/" in */./*|*/../*) invalid;; esac
case "$BACKUP_S3_PATH_STYLE" in true|false) :;; *) invalid;; esac

sql_bin=${COCKROACH_SQL_BIN:-cockroach}
command -v "$sql_bin" >/dev/null 2>&1 || { echo 'Cockroach SQL client unavailable' >&2; exit 127; }
command -v sha256sum >/dev/null 2>&1 || { echo 'backup hash utility unavailable' >&2; exit 127; }
umask 077
capture_dir=$(mktemp -d) || failed
trap 'rm -rf "$capture_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

# Encode UTF-8 bytes, not shell characters. The generated URI contains only
# unreserved bytes and uppercase escapes, so it is safe inside one SQL literal.
encode() {
 printf '%s' "$1" | od -v -An -tu1 | awk '
  { for (i=1; i<=NF; i++) {
    n=$i+0
    if ((n>=65&&n<=90)||(n>=97&&n<=122)||(n>=48&&n<=57)||n==45||n==46||n==95||n==126)
      printf "%c", n
    else printf "%%%02X", n
  }}'
}
uri="s3://$BACKUP_S3_BUCKET/$BACKUP_S3_PREFIX?AWS_ACCESS_KEY_ID=$(encode "$BACKUP_S3_ACCESS_KEY_ID")&AWS_SECRET_ACCESS_KEY=$(encode "$BACKUP_S3_SECRET_ACCESS_KEY")&AWS_ENDPOINT=$(encode "$BACKUP_S3_ENDPOINT")&AWS_REGION=$(encode "$BACKUP_S3_REGION")&AWS_USE_PATH_STYLE=$(encode "$BACKUP_S3_PATH_STYLE")"
input_digest=$(printf 'jandibat-backup-policy-v1\n%s' "$uri" | sha256sum | awk '{print $1}')
case "$input_digest" in *[!0-9a-f]*|'') failed;; esac
[ "${#input_digest}" -eq 64 ] || failed

sql() {
 # The SQL client sees only COCKROACH_URL in its environment and SQL on stdin.
 # Captures may contain credentials or server errors and are never printed.
 if ! printf '%s\n' "$1" | (ulimit -f 128 || exit 1
  COCKROACH_URL="$BACKUP_BOOTSTRAP_DATABASE_URL" \
  "$sql_bin" sql --set=errexit=true --format=tsv) >"$capture_dir/stdout" 2>"$capture_dir/stderr"; then failed; fi
 [ "$(wc -c <"$capture_dir/stdout")" -le 65536 ] || failed
 [ "$(wc -c <"$capture_dir/stderr")" -le 65536 ] || failed
}

expect_ack() {
 [ ! -s "$capture_dir/stderr" ] || failed
 awk -v phase="$1" '
  BEGIN {
   if (phase=="create") {
    n=6; expected[1]="SET"; expected[2]="SET"; expected[3]="BEGIN"
    expected[4]="CREATE EXTERNAL CONNECTION"; expected[5]="INSERT 0 1"; expected[6]="COMMIT"
   } else if (phase=="grant") {
    n=4; expected[1]="SET"; expected[2]="BEGIN"
    expected[3]="GRANT"; expected[4]="COMMIT"
   } else exit 1
  }
  { if (NR>n || $0!=expected[NR]) bad=1 }
  END { exit bad || NR!=n }
 ' "$capture_dir/stdout" || failed
}

read_state() {
 sql "SET allow_unsafe_internals = true;
BEGIN;
SELECT current_user() AS actor,
 (SELECT count(*) FROM defaultdb.jandibat_backup_admin.connection_live_digest WHERE connection_name = 'jandibat_backup_v1') AS live_count,
 (SELECT count(*) FROM defaultdb.jandibat_backup_admin.connection_policy WHERE connection_name = 'jandibat_backup_v1') AS policy_count,
 COALESCE((SELECT policy_version::STRING FROM defaultdb.jandibat_backup_admin.connection_policy WHERE connection_name = 'jandibat_backup_v1'), '') AS policy_version,
 COALESCE((SELECT input_digest FROM defaultdb.jandibat_backup_admin.connection_policy WHERE connection_name = 'jandibat_backup_v1'), '') AS input_digest,
 COALESCE((SELECT catalog_digest FROM defaultdb.jandibat_backup_admin.connection_policy WHERE connection_name = 'jandibat_backup_v1'), '') AS catalog_digest,
 COALESCE((SELECT catalog_digest FROM defaultdb.jandibat_backup_admin.connection_live_digest WHERE connection_name = 'jandibat_backup_v1'), '') AS live_digest;
COMMIT;"
 awk -F '\t' '
  NR==1 {if ($0!="SET") bad=1;next}
  NR==2 {if ($0!="BEGIN") bad=1;next}
  NR==3 {if ($0!="actor\tlive_count\tpolicy_count\tpolicy_version\tinput_digest\tcatalog_digest\tlive_digest") bad=1;next}
  NR==4 {if (NF!=7) bad=1;else print $1 "|" $2 "|" $3 "|" $4 "|" $5 "|" $6 "|" $7;next}
  NR==5 {if ($0!="COMMIT") bad=1;next}
  {bad=1}
  END {if (bad||NR!=5) exit 1}
 ' "$capture_dir/stdout" >"$capture_dir/parsed" || invalid
 IFS='|' read -r actor live_count policy_count policy_version stored_input stored_catalog live_digest <"$capture_dir/parsed" || invalid
 [ "$actor" = jandibat_backup_bootstrap ] || invalid
 case "$live_count:$policy_count" in
  0:0)
   [ -z "$policy_version" ] && [ -z "$stored_input" ] &&
    [ -z "$stored_catalog" ] && [ -z "$live_digest" ] || invalid
   state=absent;;
  1:1)
   [ "$policy_version" = 1 ] || invalid
   for digest in "$stored_input" "$stored_catalog" "$live_digest"; do
    case "$digest" in *[!0-9a-f]*|'') invalid;; esac
    [ "${#digest}" -eq 64 ] || invalid
   done
   [ "$stored_input" = "$input_digest" ] || invalid
   [ "$stored_catalog" = "$live_digest" ] || invalid
   state=matched;;
  *) invalid;;
 esac
}

read_grants() {
 sql 'SELECT grantee, privilege_type, is_grantable FROM [SHOW GRANTS ON EXTERNAL CONNECTION jandibat_backup_v1] ORDER BY grantee, privilege_type;'
 awk -F '\t' '
  NR==1 {if ($0!="grantee\tprivilege_type\tis_grantable") bad=1;next}
  NF!=3 {bad=1;next}
  $1=="root" && $2=="ALL" && ($3=="false"||$3=="f") {root++;next}
  $1=="jandibat_backup_bootstrap" && $2=="DROP" && ($3=="true"||$3=="t") {owner_drop++;next}
  $1=="jandibat_backup_bootstrap" && $2=="USAGE" && ($3=="true"||$3=="t") {owner_usage++;next}
  $1=="jandibat_backup_runner" && $2=="USAGE" && ($3=="false"||$3=="f") {runner++;next}
  $1=="jandibat_backup_verifier" && $2=="USAGE" && ($3=="false"||$3=="f") {verifier++;next}
  {bad=1}
  END {if (bad||root!=1||owner_drop!=1||owner_usage!=1||runner>1||verifier>1) exit 1;
       print (runner==1 ? "true" : "false") "|" (verifier==1 ? "true" : "false")}
 ' "$capture_dir/stdout" >"$capture_dir/grants" || invalid
 IFS='|' read -r runner_granted verifier_granted <"$capture_dir/grants" || invalid
}

read_state
if [ "$state" = absent ]; then
 sql "SET allow_unsafe_internals = true;
SET autocommit_before_ddl = false;
BEGIN;
CREATE EXTERNAL CONNECTION jandibat_backup_v1 AS '$uri';
INSERT INTO defaultdb.jandibat_backup_admin.connection_policy
 (connection_name, policy_version, input_digest, catalog_digest)
 VALUES ('jandibat_backup_v1', 1, '$input_digest',
  (SELECT catalog_digest FROM defaultdb.jandibat_backup_admin.connection_live_digest WHERE connection_name = 'jandibat_backup_v1'));
COMMIT;"
 expect_ack create
 # Never retry a failed or uncertain transaction in this invocation.
 read_state
 [ "$state" = matched ] || invalid
fi

sql "CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1' WITH transfer = '1MiB';"
awk -F '\t' '
 NR==1 {if ($0!="node\tlocality\tok\terror\ttransferred\tread_speed\twrite_speed\tcan_delete") bad=1;next}
 NF!=8 {bad=1;next}
 $1 !~ /^[1-9][0-9]*$/ {bad=1;next}
 seen[$1]++ {bad=1;next}
 ($3!="true"&&$3!="t") || ($4!=""&&$4!="NULL") || ($8!="true"&&$8!="t") {bad=1;next}
 {rows++}
 END {exit bad || rows<1}
' "$capture_dir/stdout" || failed

# CHECK performs remote I/O. Only after it succeeds can incomplete, otherwise
# exact connection privileges be granted. Recheck policy on both sides of GRANT.
read_state
[ "$state" = matched ] || invalid
read_grants
if [ "$runner_granted" = false ] || [ "$verifier_granted" = false ]; then
 sql 'SET autocommit_before_ddl = false;
BEGIN;
GRANT USAGE ON EXTERNAL CONNECTION jandibat_backup_v1 TO jandibat_backup_runner, jandibat_backup_verifier;
COMMIT;'
 expect_ack grant
fi
read_state
[ "$state" = matched ] || invalid
read_grants
[ "$runner_granted" = true ] && [ "$verifier_granted" = true ] || invalid
echo 'backup connection verified'
