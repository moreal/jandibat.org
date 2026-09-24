#!/bin/sh
set -eu

sh scripts/test-openapi-edge-contract.sh

generated_file="apps/web/src/generated/api.ts"
temporary_file="$(mktemp)"
trap 'rm -f "$temporary_file"' EXIT HUP INT TERM

yarn workspace @jandibat/web exec openapi-typescript ../../openapi/jandibat.yaml -o "$temporary_file"

if ! cmp -s "$generated_file" "$temporary_file"; then
  echo "Generated OpenAPI types are stale. Run: yarn openapi:types" >&2
  diff -u "$generated_file" "$temporary_file" || true
  exit 1
fi
