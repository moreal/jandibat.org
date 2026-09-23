#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

mkdir -p "$scratch/apps"
cp -R "$repo_root/apps/api" "$scratch/apps/api"
cp -R "$repo_root/graphql" "$scratch/graphql"

GRAPHQL_SOURCE_ROOT="$scratch" sh "$script_dir/check-graphql-generated.sh" >/dev/null
mkdir -p "$scratch/graphql/schema/nested"
printf '%s\n' 'extend type Query { nestedProbe: String }' > "$scratch/graphql/schema/nested/probe.graphqls"
if GRAPHQL_SOURCE_ROOT="$scratch" sh "$script_dir/check-graphql-generated.sh" >"$scratch/nested.log" 2>&1; then
  echo 'nested GraphQL SDL source was silently ignored' >&2
  exit 1
fi
rm "$scratch/graphql/schema/nested/probe.graphqls"
printf '\n// drift probe\n' >> "$scratch/apps/api/internal/graphql/generated/generated.go"
if GRAPHQL_SOURCE_ROOT="$scratch" sh "$script_dir/check-graphql-generated.sh" >"$scratch/drift.log" 2>&1; then
  echo 'gqlgen drift mutation was accepted' >&2
  exit 1
fi
if ! grep -q 'gqlgen generated artifact drift' "$scratch/drift.log"; then
  echo 'gqlgen drift check failed without naming generated artifact drift' >&2
  exit 1
fi

echo 'GraphQL generated artifact drift mutation was rejected'
