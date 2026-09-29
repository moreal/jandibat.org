#!/bin/sh
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
ruby - "$repo_root/yarn.lock" <<'RUBY'
require "yaml"

lock = YAML.safe_load(File.read(ARGV.fetch(0)))
undici = lock.select { |descriptors, _| descriptors.split(", ").any? { |descriptor| descriptor.start_with?("undici@npm:") } }
abort "expected a locked transitive undici dependency" if undici.empty?
undici.each_value do |entry|
  version = entry.fetch("version")
  major, minor, patch = version.split(".").map { |part| Integer(part, 10) }
  unless major == 8 && ([minor, patch] <=> [10, 2]) >= 0 && entry.fetch("resolution") == "undici@npm:#{version}"
    abort "undici #{version} is outside the patched 8.x range (>=8.10.2): GHSA-rfgv-xxqx-mfg5, GHSA-w293-vg96-wgc3, GHSA-vp8m-p9jh-q5pm"
  end
end
puts "Locked undici resolutions are in the patched 8.x range"
RUBY
