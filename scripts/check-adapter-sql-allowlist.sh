#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
exec go run "$script_dir/check-adapter-sql-allowlist.go" "$@"
