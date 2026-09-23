#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
source_root=${GRAPHQL_SOURCE_ROOT:-$repo_root}
source_root=$(CDPATH='' cd -- "$source_root" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

(CDPATH='' cd -- "$source_root" && sh "$script_dir/render-graphql-schema.sh") |
  cmp - "$source_root/graphql/schema.graphql"

mkdir -p "$scratch/apps"
cp -R "$source_root/apps/api" "$scratch/apps/api"
cp -R "$source_root/graphql" "$scratch/graphql"
(CDPATH='' cd -- "$scratch/apps/api" && go tool gqlgen generate)

for path in \
  internal/graphql/generated \
  internal/graphql/model \
  internal/graphql/schema.resolvers.go; do
  if ! diff -rq "$source_root/apps/api/$path" "$scratch/apps/api/$path"; then
    echo "gqlgen generated artifact drift: $path" >&2
    exit 1
  fi
done

echo 'GraphQL SDL bundle and gqlgen generated artifacts match checked-in files'
