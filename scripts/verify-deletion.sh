#!/bin/sh
set -eu

request_id=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--request-id) [ "$#" -ge 2 ] || { echo "--request-id requires a value" >&2; exit 2; }; request_id=$2; shift 2 ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
[ -n "$request_id" ] || { echo "usage: $0 --request-id ID" >&2; exit 2; }
exec "${MAINTENANCE_BIN:-/bin/maintenance}" verify-deletion --request-id "$request_id"
