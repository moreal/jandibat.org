#!/bin/sh
set -eu

if [ ! -d graphql/schema ]; then
  echo 'GraphQL SDL directory is missing' >&2
  exit 1
fi

find graphql/schema -type f -name '*.graphqls' | LC_ALL=C sort | while IFS= read -r file; do
  if [ ! -s "$file" ]; then
    echo "GraphQL SDL source is missing or empty: $file" >&2
    exit 1
  fi
  if [ "${previous:-}" = done ]; then
    printf '\n'
  fi
  sed -n '1,$p' "$file"
  previous=done
done
