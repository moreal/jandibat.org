#!/bin/sh
set -eu

assert_version() {
	command_name=$1
	expected=$2
	shift 2
	actual=$("$command_name" "$@")
	if [ "$actual" != "$expected" ]; then
		echo "expected $command_name version '$expected', got '$actual'" >&2
		exit 1
	fi
}

go_version=$(go version)
go_version=${go_version#go version }
go_version=${go_version%% *}
if [ "$go_version" != 'go1.27.1' ]; then
	echo "expected go version 'go1.27.1', got '$go_version'" >&2
	exit 1
fi

assert_version node 'v24.21.0' --version
assert_version yarn '4.18.0' --version

yarn_path=$(command -v yarn)
yarn_node=$(sed -n '1s/^#!//p' "$yarn_path")
yarn_node_version=$("$yarn_node" --version)
if [ "$yarn_node_version" != 'v24.21.0' ]; then
	echo "expected Yarn subprocess Node version 'v24.21.0', got '$yarn_node_version'" >&2
	exit 1
fi

for command_name in scythe staticcheck exhaustive go-check-sumtype; do
	command -v "$command_name" >/dev/null 2>&1 || {
		echo "missing required command: $command_name" >&2
		exit 1
	}
done

for analyzer in staticcheck exhaustive go-check-sumtype; do
	analyzer_path=$(command -v "$analyzer")
	analyzer_go_version=$(go version -m "$analyzer_path" | sed -n '1s/.*: //p')
	if [ "$analyzer_go_version" != 'go1.27.1' ]; then
		echo "expected $analyzer to be built with go1.27.1, got '$analyzer_go_version'" >&2
		exit 1
	fi
done

fixture_dir=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-go-analyzers.XXXXXX")
trap 'rm -rf "$fixture_dir"' EXIT HUP INT TERM
mkdir -p "$fixture_dir/home"

cat >"$fixture_dir/go.mod" <<'EOF'
module example.com/jandibat/analyzercompat

go 1.27
EOF

cat >"$fixture_dir/compat.go" <<'EOF'
package analyzercompat

import "math/rand/v2"

func BoundedInt() int {
	return rand.IntN(2)
}
EOF

(
	cd "$fixture_dir"
	export HOME="$fixture_dir/home"
	export GOCACHE="$fixture_dir/go-cache"
	export GOMODCACHE="$fixture_dir/go-mod-cache"
	staticcheck ./...
	exhaustive -check=switch,map ./...
	go-check-sumtype ./...
)
