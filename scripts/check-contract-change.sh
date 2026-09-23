#!/bin/sh
set -eu

base_ref=${CONTRACT_BASE_REF:-}
if [ -z "$base_ref" ]; then
	if git rev-parse --verify HEAD^ >/dev/null 2>&1; then
		base_ref=HEAD^
	else
		echo "contract change check skipped: no base revision"
		exit 0
	fi
fi

case "$base_ref" in
	0000000000000000000000000000000000000000)
		changed=$(git diff-tree --root --no-commit-id --name-only -r HEAD)
		;;
	*)
		if ! git cat-file -e "$base_ref^{commit}" 2>/dev/null; then
			echo "contract change check failed: base revision '$base_ref' is unavailable" >&2
			exit 1
		fi
		changed=$(git diff --name-only "$base_ref" HEAD)
		;;
esac
missing=0
if printf '%s\n' "$changed" | grep -Eq '^graphql/schema/.+\.graphqls$'; then
	if ! printf '%s\n' "$changed" | grep -qx 'docs/interface-change-log.md'; then
		echo 'contract change check failed: GraphQL SDL changed without docs/interface-change-log.md' >&2
		missing=1
	fi
fi

if printf '%s\n' "$changed" | grep -qx 'openapi/jandibat.yaml'; then
	for required in apps/web/src/generated/api.ts docs/interface-change-log.md; do
		if ! printf '%s\n' "$changed" | grep -qx "$required"; then
			echo "contract change check failed: openapi/jandibat.yaml changed without $required" >&2
			missing=1
		fi
	done
fi

if [ "$missing" -ne 0 ]; then
	exit 1
fi

echo 'contract change check: GraphQL SDL/OpenAPI changes retain their required artifacts'
