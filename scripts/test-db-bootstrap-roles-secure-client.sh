#!/bin/sh
set -eu

fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT
mkdir "$fixture_dir/bin"
export TEST_DOCKER_STATE="$fixture_dir/state"
export TEST_DOCKER_SCRIPTS="$fixture_dir/scripts"
cat >"$fixture_dir/bin/uname" <<'FAKE_UNAME'
#!/bin/sh
case "$1" in -s) echo Linux;; -m) echo x86_64;; *) exit 2;; esac
FAKE_UNAME
cat >"$fixture_dir/bin/docker" <<'FAKE_DOCKER'
#!/bin/sh
set -eu
case "$1" in
 container) exit 1 ;;
 stop) exit 0 ;;
 run) ;;
 *) exit 2 ;;
esac
interactive=false
client=false
bootstrap=false
script=false
for arg in "$@"; do
 case "$arg" in
  -i) interactive=true ;;
  /bin/sh) client=true ;;
  /bootstrap.sh) bootstrap=true ;;
  /workspace/scripts/*) script=true; printf '%s\n' "$arg" >>"$TEST_DOCKER_SCRIPTS" ;;
 esac
done
[ "$client" = true ] || exit 0
[ "$interactive" = true ] || exit 0
[ "$bootstrap" = true ] && exit 0
[ "$script" = true ] && exit 0
query=$(cat)
case "$query" in
 *'SHOW DATABASES'*) printf 'count\n1\n' ;;
 *'SHOW USERS'*) printf 'username\toptions\njandibat_migrator\tLOGIN\njandibat_api\tLOGIN\njandibat_worker\tLOGIN\njandibat_maintenance\tLOGIN\n' ;;
 *'SELECT 1'*)
  [ "${TEST_EMPTY_READY:-}" != 1 ] || exit 0
  calls=0
  [ ! -f "$TEST_DOCKER_STATE" ] || calls=$(cat "$TEST_DOCKER_STATE")
  calls=$((calls + 1))
  printf '%s\n' "$calls" >"$TEST_DOCKER_STATE"
  [ "$calls" -le 2 ] || exit 1
  printf '1\n'
  ;;
 *) exit 2 ;;
esac
FAKE_DOCKER
cat >"$fixture_dir/bin/sleep" <<'FAKE_SLEEP'
#!/bin/sh
exit 0
FAKE_SLEEP
chmod +x "$fixture_dir/bin/uname" "$fixture_dir/bin/docker" "$fixture_dir/bin/sleep"
PATH="$fixture_dir/bin:$PATH" sh scripts/test-db-bootstrap-roles-secure.sh >"$fixture_dir/output" 2>&1 || {
 echo 'FAIL: secure client discarded SQL stdin or missed a fixture result' >&2
 exit 1
}
grep -q 'secure bootstrap, migrator migration/grants, role verification and password rotation checks passed' "$fixture_dir/output" || {
 echo 'FAIL: secure fixture did not reach its verification gate' >&2
 exit 1
}
expected=$(printf '%s\n' /workspace/scripts/db-migrate-url.sh /workspace/scripts/db-migrate-url.sh /workspace/scripts/db-configure-runtime-roles.sh /workspace/scripts/db-verify-runtime-roles.sh)
actual=$(cat "$TEST_DOCKER_SCRIPTS" 2>/dev/null || :)
[ "$actual" = "$expected" ] || {
 echo 'FAIL: secure fixture did not exercise migrator migration, grants, and role verification' >&2
 exit 1
}
if TEST_EMPTY_READY=1 PATH="$fixture_dir/bin:$PATH" sh scripts/test-db-bootstrap-roles-secure.sh >"$fixture_dir/output" 2>&1; then
 echo 'FAIL: secure fixture accepted an empty readiness result' >&2
 exit 1
fi
grep -q 'secure fixture did not become ready' "$fixture_dir/output" || {
 echo 'FAIL: secure fixture failed outside readiness assertion' >&2
 exit 1
}
echo 'secure fixture client forwards SQL stdin and checks results'
