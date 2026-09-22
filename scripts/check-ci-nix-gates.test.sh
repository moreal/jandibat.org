#!/bin/sh
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
fixture_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-ci-nix-gates.XXXXXX")
trap 'rm -rf "$fixture_dir"' EXIT HUP INT TERM

cat >"$fixture_dir/nix" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$NIX_INVOCATIONS"
EOF
chmod +x "$fixture_dir/nix"

NIX_INVOCATIONS="$fixture_dir/invocations" \
  PATH="$fixture_dir:$PATH" \
  make --no-print-directory -C "$repo_root" nix-check

expected_invocations='flake check --all-systems --no-build
flake check'
actual_invocations=$(cat "$fixture_dir/invocations")
if [ "$actual_invocations" != "$expected_invocations" ]; then
  printf '%s\n' 'nix-check must evaluate every declared system before building host checks' >&2
  printf 'expected:\n%s\nactual:\n%s\n' "$expected_invocations" "$actual_invocations" >&2
  exit 1
fi

ci_commands=$(make --no-print-directory -C "$repo_root" -n ci)
for command in 'nix flake check --all-systems --no-build' 'nix flake check'; do
  if ! printf '%s\n' "$ci_commands" | grep -Fxq "$command"; then
    printf 'make ci does not execute the canonical Nix gate: %s\n' "$command" >&2
    exit 1
  fi
done

ruby - "$repo_root/.github/workflows/ci.yml" <<'RUBY'
require "yaml"

workflow = YAML.safe_load(File.read(ARGV.fetch(0)))
steps = workflow.fetch("jobs").values.flat_map { |job| job.fetch("steps", []) }
unless steps.any? { |step| step["run"] == "nix develop --command make nix-check" }
  warn "CI workflow does not invoke the canonical Make nix-check target"
  exit 1
end
RUBY

printf '%s\n' 'CI Nix gates verified'
