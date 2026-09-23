#!/bin/sh
set -eu

schema=graphql/schema/integrations.graphqls
root=graphql/schema/mutation.graphqls

grep -Fq 'enum CustomProviderStatus {' "$schema"
grep -Fq 'input CreateCustomProviderInput {' "$schema"
grep -Fq 'subjectID: ID!' "$schema"
grep -Fq 'slug: String!' "$schema"
grep -Fq 'type CreateCustomProviderPayload implements MutationPayload {' "$schema"
grep -Fq 'ingestionKey: String' "$schema"
grep -Fq 'input UpdateCustomProviderInput {' "$schema"
grep -Fq 'input RotateCustomProviderKeyInput {' "$schema"
grep -Fq 'type RotateCustomProviderKeyPayload implements MutationPayload {' "$schema"
grep -Fq 'input DeleteCustomProviderInput {' "$schema"
grep -Fq 'deletedProviderID: ID' "$schema"
grep -Fq 'createCustomProvider(input: CreateCustomProviderInput!): CreateCustomProviderPayload!' "$root"
grep -Fq 'updateCustomProvider(input: UpdateCustomProviderInput!): UpdateCustomProviderPayload!' "$root"
grep -Fq 'rotateCustomProviderKey(input: RotateCustomProviderKeyInput!): RotateCustomProviderKeyPayload!' "$root"
grep -Fq 'deleteCustomProvider(input: DeleteCustomProviderInput!): DeleteCustomProviderPayload!' "$root"

if sed -n '/^type CustomProvider implements Node {/,/^}/p' graphql/schema/node.graphqls | grep -Eq 'ingestionKey|ingestTokenHash|secret|token'; then
  echo 'CustomProvider Node must not expose ingestion credentials' >&2
  exit 1
fi

echo 'GraphQL custom provider mutations expose one-time keys only in payloads'
