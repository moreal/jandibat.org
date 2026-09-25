#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

cat > "$scratch/git" <<'GIT'
#!/bin/sh
case "$1" in
  cat-file) exit 0 ;;
  diff) printf '%s\n' "$CHANGED_PATHS" ;;
  *) echo "unexpected git test call: $1" >&2; exit 2 ;;
esac
GIT
chmod +x "$scratch/git"

check() {
  CHANGED_PATHS="$1" CONTRACT_BASE_REF=fixture-base PATH="$scratch:$PATH" \
    sh "$script_dir/check-contract-change.sh"
}

if check 'graphql/schema/query.graphqls' >"$scratch/missing-graphql.log" 2>&1; then
  echo 'GraphQL SDL change without interface log was accepted' >&2
  exit 1
fi
if check 'graphql/schema/activity/fields.graphqls' >"$scratch/missing-nested-graphql.log" 2>&1; then
  echo 'nested GraphQL SDL change without interface log was accepted' >&2
  exit 1
fi
check 'graphql/schema/query.graphqls
docs/interface-change-log.md' >/dev/null

if check 'openapi/jandibat.yaml
docs/interface-change-log.md' >"$scratch/missing-openapi.log" 2>&1; then
  echo 'OpenAPI change without generated types was accepted' >&2
  exit 1
fi
check 'openapi/jandibat.yaml
apps/web/src/generated/api.ts
docs/interface-change-log.md' >/dev/null

interface_log="$script_dir/../docs/interface-change-log.md"
section() {
  awk -v heading="$1" '
    $0 == heading { found = 1; next }
    found && /^## / { exit }
    found { print }
    END { if (!found) exit 1 }
  ' "$interface_log"
}
require_section_terms() {
  heading=$1
  shift
  if ! section "$heading" >"$scratch/section"; then
    echo "missing operational interface section: $heading" >&2
    return 1
  fi
  for term do
    if ! grep -Fq -- "$term" "$scratch/section"; then
      echo "operational interface $heading lacks: $term" >&2
      return 1
    fi
  done
}

check_operational_sections() {
  require_section_terms '## 2026-09-25 — 내부 metrics-only scrape 계약' \
    'METRICS_SOURCE' 'METRICS_LISTEN_ADDR' 'METRICS_TOKEN_FILE' \
    'COCKROACH_METRICS_HOST' 'COCKROACH_METRICS_CA_FILE' \
    'COCKROACH_METRICS_SERVER_NAME' 'GET /metrics' 'Bearer' \
    '403' '404' '405' '502' \
    'GraphQL 필드도 공개 OpenAPI 경로도 아닙니다' || return 1
  require_section_terms '## 2026-09-25 — 백업 검증 기록·보존 계획 운영 계약' \
    'collectionId' 'linkedScheduleIds' 'chainId' 'checkedAt' \
    'verifiedRecoveryAt' 'outcome' 'catalogDigest' 'objectKey' \
    'versionId' 'coverage' 'expiresAt' 'approvalHash' \
    '403' '404' '405' '200' \
    'GraphQL 도메인 필드나 공개 OpenAPI HTTP edge endpoint가 아닙니다'
}

check_operational_sections

original_log=$interface_log
sed 's/GraphQL 필드도 공개 OpenAPI 경로도 아닙니다/GraphQL 필드이며 공개 OpenAPI 경로입니다/' \
  "$original_log" >"$scratch/public-proxy.md"
if cmp -s "$original_log" "$scratch/public-proxy.md"; then
  echo 'metrics-only public-contract mutation did not apply' >&2
  exit 1
fi
interface_log="$scratch/public-proxy.md"
if check_operational_sections >"$scratch/public-proxy.log" 2>&1; then
  echo 'public metrics-only OpenAPI/GraphQL contract was accepted' >&2
  exit 1
fi

sed 's/GraphQL 도메인 필드나 공개 OpenAPI HTTP edge endpoint가 아닙니다/GraphQL 도메인 필드이며 공개 OpenAPI HTTP edge endpoint입니다/' \
  "$original_log" >"$scratch/public-backup.md"
if cmp -s "$original_log" "$scratch/public-backup.md"; then
  echo 'backup public-contract mutation did not apply' >&2
  exit 1
fi
interface_log="$scratch/public-backup.md"
if check_operational_sections >"$scratch/public-backup.log" 2>&1; then
  echo 'public backup OpenAPI/GraphQL contract was accepted' >&2
  exit 1
fi

echo 'GraphQL and OpenAPI contract changes require their change log and generated artifacts'
