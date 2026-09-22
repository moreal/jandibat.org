#!/bin/sh

set -eu

workflow_dir=${1:-.github/workflows}

if [ ! -d "$workflow_dir" ]; then
  echo "workflow directory not found: $workflow_dir" >&2
  exit 2
fi

workflow_files=$(find "$workflow_dir" -type f \( -name '*.yml' -o -name '*.yaml' \) -print | LC_ALL=C sort)
if [ -z "$workflow_files" ]; then
  echo "no workflow files found under $workflow_dir" >&2
  exit 2
fi

pattern='GO_VERSION|NODE_VERSION|YARN_VERSION|setup-go|setup-node|corepack[[:space:]]+prepare'
violations=0
old_ifs=$IFS
IFS='
'
for workflow_file in $workflow_files; do
  if grep -En "$pattern" "$workflow_file"; then
    violations=1
  fi
done
IFS=$old_ifs

if [ "$violations" -ne 0 ]; then
  echo "GitHub workflows must take Go, Node.js, and Yarn versions from flake.nix only" >&2
  exit 1
fi

echo "GitHub workflows use flake.nix as the sole language-version authority"
