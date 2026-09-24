#!/bin/sh
set -eu

actual_paths=$(awk '/^paths:/ { in_paths = 1; next } /^components:/ { in_paths = 0 } in_paths && /^  \// { sub(/:$/, "", $1); print $1 }' openapi/jandibat.yaml | LC_ALL=C sort)
expected_paths='/healthz
/v1/auth/magic-link/consume
/v1/custom-providers/{customProviderId}/activities:ingest
/v1/integrations/{provider}/callback
/v1/render/{subject}.svg'
if [ "$actual_paths" != "$expected_paths" ]; then
  echo 'OpenAPI must expose exactly the five approved HTTP edge paths' >&2
  printf 'Expected:\n%s\nActual:\n%s\n' "$expected_paths" "$actual_paths" >&2
  exit 1
fi

actual_methods=$(awk '/^paths:/ { in_paths = 1; next } /^components:/ { in_paths = 0 } in_paths && /^  \// { path = $1; sub(/:$/, "", path) } in_paths && /^    (get|post|put|patch|delete|options|head|trace):/ { method = $1; sub(/:$/, "", method); print toupper(method), path }' openapi/jandibat.yaml | LC_ALL=C sort)
expected_methods='GET /healthz
GET /v1/integrations/{provider}/callback
GET /v1/render/{subject}.svg
POST /v1/auth/magic-link/consume
POST /v1/custom-providers/{customProviderId}/activities:ingest'
if [ "$actual_methods" != "$expected_methods" ]; then
  echo 'OpenAPI must expose exactly the five approved HTTP edge methods' >&2
  printf 'Expected:\n%s\nActual:\n%s\n' "$expected_methods" "$actual_methods" >&2
  exit 1
fi

if ! awk '
  /^  \/v1\/integrations\/\{provider\}\/callback:/ { in_callback = 1; next }
  in_callback && /^  \// { exit !found }
  in_callback && /^        '\''403'\'':/ {
    getline
    if ($0 == "          $ref: '\''#/components/responses/Forbidden'\''") found = 1
  }
  END { if (!found) exit 1 }
' openapi/jandibat.yaml; then
  echo 'OAuth callback must document its forbidden response' >&2
  exit 1
fi

actual_schemas=$(awk '/^  schemas:/ { in_schemas = 1; next } in_schemas && /^    [A-Za-z]/ { sub(/:$/, "", $1); print $1 }' openapi/jandibat.yaml | LC_ALL=C sort)
expected_schemas='ActivityMetric
CustomActivityEvent
CustomActivityIngestRequest
CustomActivityIngestResponse
Date
DateTime
FieldError
HealthResponse
HeatmapTheme
IngestRejection
MagicLinkConsumeResponse
MagicLinkVerifyRequest
Problem
ProviderId
SubjectIdentifier
Uuid
WeekStart'
if [ "$actual_schemas" != "$expected_schemas" ]; then
  echo 'OpenAPI schemas must describe only the HTTP edge payloads' >&2
  printf 'Expected:\n%s\nActual:\n%s\n' "$expected_schemas" "$actual_schemas" >&2
  exit 1
fi

for field in \
  'providerCatalog: [ProviderCatalogItem!]!' \
  'activitySnapshot(' \
  'requestMagicLink(input:' \
  'finishPasskeySignIn(input:' \
  'revokeSession(input:' \
  'createSubject(input:' \
  'connectProvider(input:' \
  'enqueueManualSync(input:' \
  'createCustomProvider(input:'; do
  if ! grep -FRq "$field" graphql/schema; then
    echo "GraphQL domain replacement is missing: $field" >&2
    exit 1
  fi
done

echo 'OpenAPI HTTP edge and GraphQL domain split contract passes'
