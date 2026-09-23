#!/bin/sh
set -eu

: "${SCYTHE_DATABASE_URL:?SCYTHE_DATABASE_URL is required}"
repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

mkdir -p "$scratch/apps/api/internal" "$scratch/db"
cp "$repository_root/scythe.toml" "$scratch/scythe.toml"
cp -R "$repository_root/apps/api/internal/adapters" "$scratch/apps/api/internal/adapters"
cp -R "$repository_root/db/migrations" "$scratch/db/migrations"
sed '/^  primary_email STRING NOT NULL UNIQUE,$/a\
  drift_missing STRING NULL,' "$repository_root/db/migrations/0001_baseline.sql" > "$scratch/db/migrations/0001_baseline.sql"

if ! (CDPATH='' cd -- "$scratch" && scythe generate --config scythe.toml) >/dev/null 2>&1; then
	echo "copied schema with an additional column failed generation" >&2
	exit 1
fi
if ! (CDPATH='' cd -- "$scratch" && scythe check --config scythe.toml) >/dev/null 2>&1; then
	echo "copied schema with an additional column failed offline validation" >&2
	exit 1
fi
if (CDPATH='' cd -- "$scratch" && scythe check --config scythe.toml --database-url "$SCYTHE_DATABASE_URL") >"$scratch/live-check.log" 2>&1; then
	echo "live check accepted a column absent from CockroachDB" >&2
	exit 1
fi
if ! rg -q 'drift_missing' "$scratch/live-check.log"; then
	echo "live check failed without identifying the missing column" >&2
	exit 1
fi

echo "Scythe live missing-column detection passed"
