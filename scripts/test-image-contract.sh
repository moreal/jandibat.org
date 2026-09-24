#!/bin/sh
set -eu

# Catches mixed process payloads, build/source/secret leakage, mutable timestamps,
# and web startup that depends on a writable root filesystem.
check_archive() {
	node - "$1" "$2" <<'NODE'
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const [name, archive] = process.argv.slice(2);
const member = path => execFileSync('tar', ['-xOf', archive, path], { maxBuffer: 64 * 1024 * 1024 });
const manifest = JSON.parse(member('manifest.json'));
assert.equal(manifest.length, 1);
assert.deepEqual(manifest[0].RepoTags, [`jandibat-${name}:nix`]);
const image = JSON.parse(member(manifest[0].Config));
assert.equal(image.architecture, 'amd64');
assert.equal(image.os, 'linux');
assert.equal(Date.parse(image.created), 1000, 'creation must be fixed at the DockerTools epoch');
assert.match(image.config.User, /^[1-9][0-9]*:[1-9][0-9]*$/);
const program = { api: 'server', worker: 'worker', maintenance: 'maintenance', web: 'web-start' }[name];
assert.deepEqual(image.config.Entrypoint, [`/bin/${program}`]);
assert.ok(!image.config.Cmd || image.config.Cmd.length === 0);
for (const env of image.config.Env || []) {
  assert.match(env, /^(PATH=\/bin|SSL_CERT_FILE=\/etc\/ssl\/certs\/ca-certificates\.crt)$/,
    'only non-secret, environment-independent runtime defaults belong in images');
}
const paths = manifest[0].Layers.flatMap(layer => execFileSync('tar', ['-tf', '-'], {
  input: member(layer), maxBuffer: 64 * 1024 * 1024,
}).toString().split('\n').map(path => path.replace(/^\.\//, '')));
assert.ok(paths.includes('busybox'), '/busybox supports exec health checks');
assert.ok(paths.includes(`bin/${program}`), 'entrypoint must exist in archive');
assert.ok(paths.some(path => path.endsWith('/etc/ssl/certs/ca-certificates.crt')));
for (const path of paths) {
  assert.doesNotMatch(path, /(^|\/)(\.git|node_modules|go\.mod|go\.sum|package\.json|yarn\.lock|src)(\/|$)/,
    `source or dependency tree leaked: ${path}`);
  assert.doesNotMatch(path, /(^|\/)(\.env(?:\.[^/]*)?|id_rsa|id_ed25519)$/);
  assert.doesNotMatch(path, /(^|\/)bin\/(go|node|npm|yarn|gcc|cc|rustc)$/);
  for (const other of ['server', 'worker', 'maintenance']) {
    if (other !== program) assert.doesNotMatch(path, new RegExp(`(^|/)bin/${other}$`), `unrelated ${other} payload leaked`);
  }
}
console.log(`${name}: archive contract passed`);
NODE
}

if [ "${1:-}" = --archive ]; then
	check_archive "$2" "$3"
	exit
fi

for name in api worker maintenance web; do
	nix eval --raw ".#packages.x86_64-linux.${name}-image.drvPath" >/dev/null
done
nix flake check --all-systems --no-build

system=$(nix eval --impure --raw --expr builtins.currentSystem)
if [ "$system" != x86_64-linux ]; then
	echo "SKIP: archive builds, rebuild hashes, and container smoke require x86_64-linux; evaluated on $system"
	exit
fi

scratch=$(mktemp -d)
containers=
network=
cleanup() {
	for container in $containers; do docker rm -f "$container" >/dev/null 2>&1 || :; done
	if [ -n "$network" ]; then docker network rm "$network" >/dev/null 2>&1 || :; fi
	rm -r "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

for name in api worker maintenance web; do
	archive=$(nix build --no-link --print-out-paths ".#${name}-image")
	check_archive "$name" "$archive"
	before=$(sha256sum "$archive" | cut -d ' ' -f 1)
	# --rebuild really reruns the archive derivation instead of reusing its output.
	nix build --offline --rebuild --option sandbox true --no-link ".#${name}-image"
	after=$(sha256sum "$archive" | cut -d ' ' -f 1)
	test "$before" = "$after"
	printf '%s %s\n' "$name" "$after"
	docker load -i "$archive"
done

wait_http() {
	container=$1 port=$2 route=$3
	for attempt in $(seq 1 60); do
		if docker exec "$container" /busybox wget -T 5 -q -O - "http://127.0.0.1:$port$route" >"$scratch/body"; then return; fi
		sleep 1
	done
	docker logs "$container" >&2
	echo "timed out waiting for $route" >&2
	exit 1
}

web=$(docker run -d --read-only --tmpfs /tmp:rw,nosuid,nodev --user 101:101 \
	-e JANDIBAT_API_BASE_URL=https://api.example.test jandibat-web:nix)
containers="$containers $web"
wait_http "$web" 8080 /healthz
test "$(cat "$scratch/body")" = ok
wait_http "$web" 8080 /config.json
node -e 'require("node:assert/strict").deepEqual(JSON.parse(require("node:fs").readFileSync(process.argv[1])), {apiBaseUrl:"https://api.example.test"})' "$scratch/body"
docker exec "$web" /busybox wget -T 5 -S -O /dev/null http://127.0.0.1:8080/ 2>"$scratch/headers"
node - "$scratch/headers" <<'NODE'
const assert = require('node:assert/strict');
const headers = require('node:fs').readFileSync(process.argv[2], 'utf8');
const policies = [...headers.matchAll(/content-security-policy:\s*([^\r\n]+)/gi)];
assert.equal(policies.length, 1);
assert.equal(policies[0][1].match(/(?:^|;)\s*connect-src\s+([^;]+)/)[1], "'self' https://api.example.test");
NODE

# Bounded wait also catches a regression that silently starts without /tmp.
readonly=$(docker run -d --read-only --user 101:101 \
	-e JANDIBAT_API_BASE_URL=https://api.example.test jandibat-web:nix)
containers="$containers $readonly"
for attempt in $(seq 1 15); do
	[ "$(docker inspect -f '{{.State.Running}}' "$readonly")" = true ] || break
	sleep 1
done
test "$(docker inspect -f '{{.State.Running}}' "$readonly")" = false
test "$(docker inspect -f '{{.State.ExitCode}}' "$readonly")" -ne 0
docker logs "$readonly" 2>&1 | grep -q 'runtime output directory must exist and be writable'

# Use only an isolated, disposable in-memory DB: no existing service or volume.
# Read the already pinned fixture image rather than introduce a version authority.
db_image=$(docker compose -f docker-compose.yml config --format json | node -e \
	'let data="";process.stdin.on("data", x=>data+=x);process.stdin.on("end",()=>console.log(JSON.parse(data).services.cockroach.image))')
network=$(docker network create "jandibat-images-$(basename "$scratch")")
db=$(docker run -d --network "$network" --network-alias database \
	--mount "type=bind,src=$PWD/db/migrations,dst=/migrations,readonly" \
	--mount "type=bind,src=$PWD/scripts/db-migrate-url.sh,dst=/migrate.sh,readonly" \
	"$db_image" start-single-node --insecure --store=type=mem,size=0.25 \
	--cache=64MiB --max-sql-memory=64MiB)
containers="$containers $db"
for attempt in $(seq 1 60); do
	if docker exec "$db" cockroach sql --insecure -e 'SELECT 1' >/dev/null 2>&1; then break; fi
	sleep 1
done
docker exec "$db" cockroach sql --insecure -e 'CREATE DATABASE image_smoke'
docker exec -e MIGRATION_DATABASE_URL=postgresql://root@localhost:26257/image_smoke?sslmode=disable \
	-e COCKROACH_DATABASE=image_smoke -e MIGRATIONS_DIR=/migrations "$db" sh /migrate.sh

# Development config permits disposable test credentials; production secret
# validation is covered by the separate runtime-contract suite.
processes=
for spec in api:8080:DATABASE_URL worker:8081:WORKER_DATABASE_URL maintenance:8082:MAINTENANCE_DATABASE_URL; do
	name=${spec%%:*} rest=${spec#*:} port=${rest%%:*} variable=${rest#*:}
	container=$(docker run -d --network "$network" --read-only --tmpfs /tmp:rw,nosuid,nodev \
		-e APP_ENV=development -e "$variable=postgresql://root@database:26257/image_smoke?sslmode=disable" \
		"jandibat-$name:nix")
	containers="$containers $container"
	processes="$processes $container:$port"
	wait_http "$container" "$port" /livez
	wait_http "$container" "$port" /readyz
done

# Loss of the dependency must fail readiness without failing liveness.
docker stop -t 5 "$db" >/dev/null
for spec in $processes; do
	container=${spec%:*} port=${spec#*:}
	wait_http "$container" "$port" /livez
	if docker exec "$container" /busybox wget -T 5 -S -O /dev/null "http://127.0.0.1:$port/readyz" 2>"$scratch/readiness"; then
		echo 'readiness unexpectedly succeeded without a database' >&2
		exit 1
	fi
	grep -q '503 Service Unavailable' "$scratch/readiness"
done
echo 'image archive and runtime contracts passed'
