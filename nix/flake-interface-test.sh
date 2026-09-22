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

for command_name in scythe staticcheck exhaustive go-check-sumtype; do
	command -v "$command_name" >/dev/null 2>&1 || {
		echo "missing required command: $command_name" >&2
		exit 1
	}
done
