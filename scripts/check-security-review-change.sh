#!/bin/sh
set -eu

base=${SECURITY_REVIEW_BASE:-}
if [ -z "$base" ]; then
	echo "SECURITY_REVIEW_BASE is required" >&2
	exit 2
fi
git cat-file -e "$base^{commit}" 2>/dev/null || { echo "security review base commit is unavailable: $base" >&2; exit 2; }

changed=$(git diff --name-only "$base"...HEAD)
sensitive=$(printf '%s\n' "$changed" | awk '
  /^(apps\/api\/|apps\/web\/|packages\/contracts\/|openapi\/|db\/|deploy\/|scripts\/|\.github\/workflows\/|\.env|docker-compose\.yml|Makefile$)/ { print }
')
if [ -z "$sensitive" ]; then
	echo "no security-sensitive paths changed"
	exit 0
fi

review=${SECURITY_REVIEW:-}
if [ -z "$review" ]; then
	reviews=$(printf '%s\n' "$changed" | awk '/^docs\/evidence\/security\/[^/]+\.md$/ && $0 !~ /\/README\.md$/')
	count=$(printf '%s\n' "$reviews" | awk 'NF { count++ } END { print count + 0 }')
	if [ "$count" -ne 1 ]; then
		echo "security-sensitive change requires exactly one changed security review artifact; found $count" >&2
		exit 1
	fi
	review=$reviews
fi
case "$review" in docs/evidence/security/*.md) ;; *) echo "security review must be under docs/evidence/security" >&2; exit 2 ;; esac
[ -f "$review" ] || { echo "security review file does not exist: $review" >&2; exit 2; }

reviewed_sha=$(awk -F '|' '$2 ~ /^[[:space:]]*Commit SHA[[:space:]]*$/ { gsub(/[[:space:]]/, "", $3); print $3; exit }' "$review")
node scripts/validate-security-review.mjs "$review" "$reviewed_sha"
git merge-base --is-ancestor "$reviewed_sha" HEAD || { echo "reviewed commit is not an ancestor of HEAD" >&2; exit 1; }
post_review_changes=$(git diff --name-only "$reviewed_sha"..HEAD | awk -v review="$review" '$0 != review { print }')
if [ -n "$post_review_changes" ]; then
	echo "code changed after the reviewed commit; obtain a new security review:" >&2
	printf '%s\n' "$post_review_changes" >&2
	exit 1
fi
echo "security review is bound to reviewed code $reviewed_sha; HEAD adds only $review"
