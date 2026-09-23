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

echo 'GraphQL and OpenAPI contract changes require their change log and generated artifacts'
