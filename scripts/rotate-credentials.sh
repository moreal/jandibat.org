#!/bin/sh
set -eu

mode=
scope=
resume=false
while [ "$#" -gt 0 ]; do
	case "$1" in
		--scope) [ "$#" -ge 2 ] || { echo "--scope requires a value" >&2; exit 2; }; scope=$2; shift 2 ;;
		--dry-run|--execute) [ -z "$mode" ] || { echo "choose exactly one mode" >&2; exit 2; }; mode=$1; shift ;;
		--resume) resume=true; shift ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
[ -n "$mode" ] || { echo "usage: $0 (--dry-run|--execute) [--scope ID] [--resume]" >&2; exit 2; }
if [ "$mode" = --execute ] && [ -z "$scope" ]; then echo "--execute requires --scope" >&2; exit 2; fi

set -- reencrypt "$mode"
[ -n "$scope" ] && set -- "$@" --scope "$scope"
[ "$resume" = true ] && set -- "$@" --resume
exec "${MAINTENANCE_BIN:-/jandibat-maintenance}" "$@"
