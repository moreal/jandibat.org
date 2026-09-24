#!/bin/sh
set -eu

mode=
as_of=
scope=
resume=false
while [ "$#" -gt 0 ]; do
	case "$1" in
		--as-of) [ "$#" -ge 2 ] || { echo "--as-of requires a value" >&2; exit 2; }; as_of=$2; shift 2 ;;
		--scope) [ "$#" -ge 2 ] || { echo "--scope requires a value" >&2; exit 2; }; scope=$2; shift 2 ;;
		--dry-run|--execute) [ -z "$mode" ] || { echo "choose exactly one mode" >&2; exit 2; }; mode=$1; shift ;;
		--resume) resume=true; shift ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
[ -n "$as_of" ] && [ -n "$mode" ] || { echo "usage: $0 --as-of RFC3339 (--dry-run|--execute) [--scope ID] [--resume]" >&2; exit 2; }
if [ "$mode" = --execute ] && [ -z "$scope" ]; then echo "--execute requires --scope" >&2; exit 2; fi

set -- retention --as-of "$as_of" "$mode"
[ -n "$scope" ] && set -- "$@" --scope "$scope"
[ "$resume" = true ] && set -- "$@" --resume
exec "${MAINTENANCE_BIN:-/bin/maintenance}" "$@"
