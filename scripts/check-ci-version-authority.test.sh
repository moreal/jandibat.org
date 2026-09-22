#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
guard=$script_dir/check-ci-version-authority.sh
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-ci-version-authority.XXXXXX")
trap 'rm -rf "$tmp_dir"' 0 1 2 3 15

expect_status() {
  expected=$1
  label=$2
  shift 2
  output=$tmp_dir/$label.out

  set +e
  "$@" >"$output" 2>&1
  actual=$?
  set -e

  if [ "$actual" -ne "$expected" ]; then
    echo "$label: expected exit $expected, got $actual" >&2
    cat "$output" >&2
    exit 1
  fi
}

split_corepack=$tmp_dir/split-corepack
mkdir -p "$split_corepack"
cat >"$split_corepack/ci.yml" <<'YAML'
name: split Corepack
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - run: |
          corepack \
            prepare yarn@4.6.0 --activate
YAML
expect_status 1 split-corepack sh "$guard" "$split_corepack"
grep -q 'corepack.*prepare' "$tmp_dir/split-corepack.out"

split_version=$tmp_dir/split-version
mkdir -p "$split_version"
cat >"$split_version/ci.yaml" <<'YAML'
name: split version authority
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - run: |
          export NODE_\
          VERSION=24.18.0
YAML
expect_status 1 split-version sh "$guard" "$split_version"
grep -q 'NODE_VERSION' "$tmp_dir/split-version.out"

ordinary=$tmp_dir/ordinary
mkdir -p "$ordinary"
cat >"$ordinary/first.yml" <<'YAML'
env:
  NODE_VERSION: 24.18.0
YAML
cat >"$ordinary/second.yaml" <<'YAML'
steps:
  - uses: actions/setup-go@0123456789abcdef
YAML
expect_status 1 ordinary-violations sh "$guard" "$ordinary"
grep -q 'NODE_VERSION' "$tmp_dir/ordinary-violations.out"
grep -q 'setup-go' "$tmp_dir/ordinary-violations.out"

empty=$tmp_dir/empty
mkdir -p "$empty"
expect_status 2 no-workflows sh "$guard" "$empty"
expect_status 2 missing-directory sh "$guard" "$tmp_dir/missing"

clean=$tmp_dir/clean
mkdir -p "$clean"
cat >"$clean/ci.yml" <<'YAML'
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - run: nix develop --command make ci
YAML
expect_status 0 clean-workflow sh "$guard" "$clean"

echo "CI version authority continuation and failure-mode tests passed"
