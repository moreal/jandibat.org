#!/bin/sh
set -eu

for file in \
  graphql/schema/scalars.graphqls \
  graphql/schema/node.graphqls \
  graphql/schema/query.graphqls \
  graphql/schema/mutation.graphqls \
  apps/api/gqlgen.yml \
  scripts/check-graphql-generated.sh; do
  if [ ! -s "$file" ]; then
    echo "GraphQL contract artifact is missing or empty: $file" >&2
    exit 1
  fi
done

if ! grep -q 'GraphQL SDL.*도메인' AGENTS.md; then
  echo 'working agreement must make GraphQL SDL the domain contract' >&2
  exit 1
fi
if ! grep -q 'GraphQL SDL' docs/interface-change-log.md; then
  echo 'interface change log must record the split contract' >&2
  exit 1
fi
if ! grep -q '^graphql-generate:' Makefile || ! grep -q '^graphql-check:' Makefile; then
  echo 'GraphQL generation and verification Make targets are required' >&2
  exit 1
fi

echo 'GraphQL SDL contract and generation entry points are present'
