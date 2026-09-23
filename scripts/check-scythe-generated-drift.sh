#!/bin/sh
set -eu

source_root=${SCYTHE_SOURCE_ROOT:-$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)}
source_root=$(CDPATH='' cd -- "$source_root" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

cp "$source_root/scythe.toml" "$scratch/scythe.toml"
awk '/^(schema|queries|output) = / {
  line = $0
  while (match(line, /"[^"]+"/)) {
    print substr(line, RSTART + 1, RLENGTH - 2)
    line = substr(line, RSTART + RLENGTH)
  }
}' "$source_root/scythe.toml" | sort -u > "$scratch/paths"
awk -F '"' '/^output = / { print $2 }' "$source_root/scythe.toml" | sort -u > "$scratch/outputs"

while IFS= read -r path; do
	if [ -z "$path" ] || [ ! -e "$source_root/$path" ]; then
		echo "missing Scythe input or output: $path" >&2
		exit 1
	fi
	mkdir -p "$scratch/$(dirname -- "$path")"
	cp -R "$source_root/$path" "$scratch/$path"
done < "$scratch/paths"

(CDPATH='' cd -- "$scratch" && scythe generate --config scythe.toml)

while IFS= read -r output; do
	if ! diff -rq "$source_root/$output" "$scratch/$output"; then
		echo "Scythe generated output drift: $output" >&2
		exit 1
	fi
done < "$scratch/outputs"

echo "Scythe generated outputs match checked-in files"
