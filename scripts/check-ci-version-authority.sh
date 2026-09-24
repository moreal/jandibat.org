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

pattern='GO_VERSION|NODE_VERSION|YARN_VERSION|setup-go|setup-node|corepack[[:space:]]+prepare|docker[[:space:]]+(build|buildx[[:space:]]+build)([[:space:]]|$)|apps/(api|web)/Dockerfile'
violations=0

normalize_continuations() {
  awk '
    function indentation_width(value) {
      match(value, /^[[:space:]]*/)
      return RLENGTH
    }

    function drop_indentation(value, width, position) {
      for (position = 0; position < width && substr(value, 1, 1) ~ /[[:space:]]/; position++) {
        value = substr(value, 2)
      }
      return value
    }

    function trailing_backslashes(value, count, position) {
      count = 0
      for (position = length(value); position > 0 && substr(value, position, 1) == "\\"; position--) {
        count++
      }
      return count
    }

    {
      line = $0
      if (continuing) {
        line = drop_indentation(line, base_indent)
      } else {
        base_indent = indentation_width(line)
      }

      logical_line = logical_line line
      if (trailing_backslashes(logical_line) % 2 == 1) {
        logical_line = substr(logical_line, 1, length(logical_line) - 1)
        continuing = 1
        next
      }

      print logical_line
      logical_line = ""
      continuing = 0
    }

    END {
      if (logical_line != "") {
        print logical_line
      }
    }
  ' "$1"
}

old_ifs=$IFS
IFS='
'
for workflow_file in $workflow_files; do
  normalized_workflow=$(normalize_continuations "$workflow_file")
  if printf '%s\n' "$normalized_workflow" | grep -En "$pattern"; then
    violations=1
  fi
done
IFS=$old_ifs

# Release helpers are part of the same policy boundary as workflow run blocks.
# The sole Docker build exception copies already-built Nix image closures into
# restore tooling; its Dockerfile may not RUN a compiler or installer.
release_scripts=${2:-scripts}
for file in "$release_scripts/build-release-images.sh" "$release_scripts/image-release.mjs"; do
  [ -f "$file" ] || continue
  normalized=$(normalize_continuations "$file")
  if printf '%s\n' "$normalized" | grep -Ev '^[[:space:]]*(#|//)' | grep -En 'corepack|yarn[[:space:]]+install|npm[[:space:]]+(ci|install)|go[[:space:]]+build|apps/(api|web)/Dockerfile'; then
    violations=1
  fi
  if printf '%s\n' "$normalized" | grep -E 'docker[[:space:]]+(build|buildx[[:space:]]+build)([[:space:]]|$)' | grep -Ev -- '--file deploy/restore-tools.Dockerfile'; then
    violations=1
  fi
done
restore_dockerfile=${3:-deploy/restore-tools.Dockerfile}
if grep -Ein '^[[:space:]]*(RUN|ADD)[[:space:]]' "$restore_dockerfile"; then
  violations=1
fi

if [ "$violations" -ne 0 ]; then
  echo "Workflows and release helpers must build application code with Nix only" >&2
  exit 1
fi

echo "GitHub workflows use flake.nix as the sole language-version authority"
