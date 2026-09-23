#!/bin/sh
set -eu

schema=graphql/schema/integrations.graphqls
root=graphql/schema/mutation.graphqls

grep -Fq 'enum ProviderAuthMethod {' "$schema"
grep -Fq 'input ConnectProviderInput {' "$schema"
grep -Fq 'subjectID: ID!' "$schema"
grep -Fq 'authMethod: ProviderAuthMethod!' "$schema"
grep -Fq 'token: String' "$schema"
grep -Fq 'includePrivate: Boolean = false' "$schema"
grep -Fq 'redirectURI: String' "$schema"
grep -Fq 'type ConnectProviderPayload implements MutationPayload {' "$schema"
grep -Fq 'authorizationURL: String' "$schema"
grep -Fq 'input UpdateProviderConnectionInput {' "$schema"
grep -Fq 'type RevokeProviderConnectionPayload implements MutationPayload {' "$schema"
grep -Fq 'revokedConnectionID: ID' "$schema"
grep -Fq 'input EnqueueManualSyncInput {' "$schema"
grep -Fq 'idempotencyKey: String!' "$schema"
grep -Fq 'job: SyncJob' "$schema"
grep -Fq 'connectProvider(input: ConnectProviderInput!): ConnectProviderPayload!' "$root"
grep -Fq 'updateProviderConnection(input: UpdateProviderConnectionInput!): UpdateProviderConnectionPayload!' "$root"
grep -Fq 'revokeProviderConnection(input: RevokeProviderConnectionInput!): RevokeProviderConnectionPayload!' "$root"
grep -Fq 'enqueueManualSync(input: EnqueueManualSyncInput!): EnqueueManualSyncPayload!' "$root"

if grep -Eq 'accessToken|refreshToken|sessionToken|ingestTokenHash' "$schema"; then
  echo 'integration mutation contract must not expose persisted secrets' >&2
  exit 1
fi

for output in ConnectProviderPayload UpdateProviderConnectionPayload RevokeProviderConnectionPayload EnqueueManualSyncPayload; do
  if sed -n "/^type $output /,/^}/p" "$schema" | grep -Eq 'token:|idempotencyKey:|credential|secret:'; then
    echo "GraphQL $output must not echo credentials or idempotency keys" >&2
    exit 1
  fi
done

echo 'GraphQL integration mutation contract uses typed payloads without persisted secrets'
