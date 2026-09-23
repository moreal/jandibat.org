#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_root"

dry_run=$(make -n test-api-integration \
	JANDIBAT_TEST_DATABASE_URL=fixture \
	JANDIBAT_TEST_API_DATABASE_URL=fixture \
	JANDIBAT_TEST_WORKER_DATABASE_URL=fixture \
	JANDIBAT_TEST_MAINTENANCE_DATABASE_URL=fixture)

if ! printf '%s\n' "$dry_run" | rg -q -- '-tags(=|[[:space:]])integration'; then
	echo "test-api-integration omits the integration build tag" >&2
	exit 1
fi

packages=$(rg -l '^//go:build integration' apps/api -g '*_test.go' |
	sed 's#^apps/api/##; s#/[^/]*$##' | sort -u)
while IFS= read -r package; do
	[ -n "$package" ] || continue
	case "$dry_run" in
		*"go test"*"./..."* | *"./$package"*) ;;
		*) echo "test-api-integration omits ./$package" >&2; exit 1 ;;
	esac
done <<EOF
$packages
EOF

echo "test-api-integration covers every tagged package"
