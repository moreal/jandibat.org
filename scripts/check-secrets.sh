#!/bin/sh
set -eu

if rg -n -I --hidden \
	-g '!.git/**' \
	-g '!node_modules/**' \
	-g '!.yarn/**' \
	-g '!dist/**' \
	-e '-----BEGIN (RSA|OPENSSH|EC|DSA) PRIVATE KEY-----' \
	-e 'ghp_[[:alnum:]]{20,}' \
	-e 'github_pat_[[:alnum:]_]{20,}' \
	-e 'AKIA[0-9A-Z]{16}' \
	-e 'sk-[[:alnum:]]{20,}' \
	.; then
	echo "possible committed secret detected" >&2
	exit 1
fi

echo "secret pattern scan passed"
