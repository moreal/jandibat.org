#!/bin/sh
set -eu
exec node "$(dirname "$0")/probe-restore-runtime-candidate.mjs" "$@"
