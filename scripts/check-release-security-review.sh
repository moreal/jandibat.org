#!/bin/sh

set -eu

case "$SECURITY_REVIEW" in docs/evidence/security/*.md) ;; *) echo "invalid security review path" >&2; exit 2;; esac
test -f "$SECURITY_REVIEW"
reviewed_sha=$(awk -F '|' '$2 ~ /^[[:space:]]*Commit SHA[[:space:]]*$/ { gsub(/[[:space:]]/, "", $3); print $3; exit }' "$SECURITY_REVIEW")
SECURITY_REVIEW_SHA="$reviewed_sha" nix develop --command make security-review-check
git merge-base --is-ancestor "$reviewed_sha" "$GITHUB_SHA"
test -z "$(git diff --name-only "$reviewed_sha".."$GITHUB_SHA" | awk -v review="$SECURITY_REVIEW" '$0 != review { print }')"
