#!/bin/sh
set -eu

command -v rg >/dev/null 2>&1 || {
	echo "missing required command: rg" >&2
	exit 1
}

repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

mkdir -p "$scratch/apps/api/internal" "$scratch/db"
cp "$repository_root/scythe.toml" "$scratch/scythe.toml"
cp -R "$repository_root/db/migrations" "$scratch/db/migrations"
cp -R "$repository_root/apps/api/internal/adapters" "$scratch/apps/api/internal/adapters"

if ! SCYTHE_SOURCE_ROOT="$scratch" sh "$repository_root/scripts/check-scythe-generated-drift.sh"; then
	echo "unchanged Scythe outputs were rejected" >&2
	exit 1
fi

generated="$scratch/apps/api/internal/adapters/operations/cockroach/generated/queries.go"
sed 's/^package queries$/package drifted/' "$generated" > "$generated.changed"
mv "$generated.changed" "$generated"
if SCYTHE_SOURCE_ROOT="$scratch" sh "$repository_root/scripts/check-scythe-generated-drift.sh" >/dev/null 2>&1; then
	echo "modified generated output was accepted" >&2
	exit 1
fi

queries="$scratch/apps/api/internal/adapters/operations/cockroach/queries/operations.sql"
sed 's/SELECT primary_email FROM users/SELECT missing_column FROM users/' "$queries" > "$queries.changed"
mv "$queries.changed" "$queries"
if (CDPATH='' cd -- "$scratch" && scythe check --config scythe.toml) >"$scratch/sql-check.log" 2>&1; then
	echo "invalid copied SQL column was accepted" >&2
	exit 1
fi
if ! rg -q 'missing_column' "$scratch/sql-check.log"; then
	echo "offline check failed without identifying the invalid column" >&2
	exit 1
fi

echo "Scythe generated drift detection passed"
