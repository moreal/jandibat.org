#!/bin/sh
set -eu

for name in api worker maintenance web; do
	derivation=$(nix eval --raw ".#packages.x86_64-linux.${name}-payload" --apply 'payload: payload.drvPath')
	case "$derivation" in
		/nix/store/*.drv) ;;
		*) echo "unexpected ${name} payload derivation: $derivation" >&2; exit 1 ;;
	esac
done

nix flake check --all-systems --no-build

rg -q 'yarnBerryConfigHook' nix/images.nix
rg -q 'yarnOfflineCache' nix/images.nix
if rg -q 'fakeHash' nix/images.nix; then
	echo 'release payload contains a temporary Nix hash' >&2
	exit 1
fi
if rg 'docker[[:space:]]+build|corepack|yarn[[:space:]]+install' nix/images.nix | rg -v 'yarn install --immutable --immutable-cache'; then
	echo 'release payload invokes a forbidden build or networked install command' >&2
	exit 1
fi

system=$(nix eval --impure --raw --expr builtins.currentSystem)
if [ "$system" = x86_64-linux ]; then
	for specification in api:server worker:worker maintenance:maintenance; do
		name=${specification%%:*}
		executable=${specification#*:}
		payload=$(nix build --no-link --print-out-paths ".#${name}-payload")
		test -x "$payload/bin/$executable"
		test "$(find "$payload/bin" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" -eq 1
	done
	web_payload=$(nix build --no-link --print-out-paths .#web-payload)
	test -f "$web_payload/dist/client/index.html"
	find "$web_payload/dist/client/assets" -type f -name '*-[[:alnum:]]*.js' | rg -q .
else
	echo "payload builds skipped on $system (requires x86_64-linux builder)"
fi
