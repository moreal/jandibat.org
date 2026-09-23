#!/bin/sh
set -eu

grep -Fq 'providerConnections(first: Int = 25, after: Cursor): ProviderConnectionConnection' graphql/schema/node.graphqls
grep -Fq 'customProviders(first: Int = 25, after: Cursor): CustomProviderConnection' graphql/schema/node.graphqls
grep -Fq 'providerCatalog: [ProviderCatalogItem!]!' graphql/schema/query.graphqls
grep -Fq 'type ProviderConnectionEdge {' graphql/schema/integrations.graphqls
grep -Fq 'type CustomProviderEdge {' graphql/schema/integrations.graphqls
grep -Fq 'pageInfo: PageInfo!' graphql/schema/integrations.graphqls

if grep -Eq 'accessToken|refreshToken|ingestTokenHash|ingestSecret|externalAccountLogin' graphql/schema/node.graphqls graphql/schema/integrations.graphqls; then
  echo 'GraphQL integration Node contract must not expose credential fields' >&2
  exit 1
fi

echo 'GraphQL integration query contract has scoped Relay connections and no credentials'
