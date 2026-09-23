#!/bin/sh
set -eu

for file in \
  graphql/schema/scalars.graphqls \
  graphql/schema/node.graphqls \
  graphql/schema/query.graphqls \
  graphql/schema/activity.graphqls \
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

node_types=$(awk '$1 == "type" && $3 == "implements" && $4 == "Node" { print $2 }' graphql/schema.graphql | LC_ALL=C sort)
expected_node_types='CustomProvider
ProviderConnection
Session
Subject
SyncJob'
if [ "$node_types" != "$expected_node_types" ]; then
  echo 'Relay Node implementations must be exactly the five durable entity types' >&2
  exit 1
fi
if ! grep -q 'node(id: ID!): Node' graphql/schema.graphql; then
  echo 'Relay node root field is missing' >&2
  exit 1
fi
if grep -Eq '^type (ActivityDay|ActivityStatistics) implements Node' graphql/schema.graphql; then
  echo 'activity days and statistics must remain value objects' >&2
  exit 1
fi
if ! grep -q 'subject(handleOrID: String!): Subject' graphql/schema.graphql ||
   ! grep -q 'activitySnapshot(' graphql/schema.graphql ||
   ! grep -q 'range: DateRangeInput!' graphql/schema.graphql; then
  echo 'static ActivitySnapshot query is missing from the domain contract' >&2
  exit 1
fi
for field in 'generatedAt: DateTime!' 'dataUpdatedAt: DateTime' 'revision: String!'; do
  if ! grep -q "$field" graphql/schema.graphql; then
    echo "ActivitySnapshot provenance field is missing: $field" >&2
    exit 1
  fi
done
if ! grep -q '^scalar Long' graphql/schema.graphql ||
   [ "$(grep -c ': Long!' graphql/schema.graphql)" -ne 3 ]; then
  echo 'ActivitySnapshot count, metric and total must retain signed 64-bit range' >&2
  exit 1
fi

echo 'GraphQL SDL contract and generation entry points are present'
