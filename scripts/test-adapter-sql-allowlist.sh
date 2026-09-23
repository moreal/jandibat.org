#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
root="$scratch/adapter root with spaces"
manifest="$scratch/allowlist.manifest"
mkdir -p "$root/integrations/cockroach" "$root/operations/cockroach"

cat >"$root/integrations/cockroach/revocations.go" <<'EOF'
package cockroach
// adapter-sql-allowlist: virtual-catalog
const query = `SELECT 1`
EOF
cat >"$root/operations/cockroach/audit_outbox.go" <<'EOF'
package cockroach
// adapter-sql-allowlist: virtual-catalog
const query = `SELECT 1`
EOF
cat >"$root/operations/cockroach/store.go" <<'EOF'
package cockroach
// adapter-sql-allowlist: virtual-catalog
const query = `SELECT 1`
EOF
cat >"$root/operations/cockroach/retention.go" <<'EOF'
package cockroach
// adapter-sql-allowlist: sealed-dataset-identifiers
const query = `DELETE FROM table`
EOF

check() {
 ADAPTER_SQL_ROOT="$root" ADAPTER_SQL_MANIFEST="$manifest" sh "$script_dir/check-adapter-sql-allowlist.sh" "$@"
}
reject() {
 name=$1
 if check >"$scratch/$name.log" 2>&1; then
  echo "$name mutation was accepted" >&2
  exit 1
 fi
}

check --write-manifest > /dev/null
check > /dev/null

for sql in 'select 1' 'SELECT
  1' 'SELECT	1' 'SELECT /* probe */ count(*) FROM users' 'SELECT -- probe
  count(*) FROM users' 'SELECT $1' 'WITH newer AS (SELECT 1) SELECT * FROM newer' 'CREATE TABLE rogue (id INT)' 'ALTER TABLE target ADD COLUMN id INT' 'DROP TABLE target'; do
 printf 'package cockroach\nconst rogue = `%b`\n' "$sql" >"$root/operations/cockroach/rogue.go"
 reject rogue
done
printf 'package cockroach\nconst rogue = "SELECT " + "count(*) FROM users"\n' >"$root/operations/cockroach/rogue.go"
reject static_concatenation
printf 'package cockroach\nconst prefix = "SELECT "\nconst rogue = prefix + "count(*) FROM users"\n' >"$root/operations/cockroach/rogue.go"
reject const_identifier_concatenation
cat >"$root/operations/cockroach/rogue.go" <<'EOF'
package cockroach
func rogue() string {
 const prefix = "SELECT "
 return prefix + "count(*) FROM users"
}
EOF
reject function_local_const_concatenation
rm "$root/operations/cockroach/rogue.go"

# A SQL replacement, or an added literal on the SAME source line, must not
# inherit the allowance from a prior line-count observation.
sed 's/SELECT 1/SELECT 2/' "$root/integrations/cockroach/revocations.go" >"$scratch/replaced.go"
cp "$scratch/replaced.go" "$root/integrations/cockroach/revocations.go"
reject replaced_sql
sed 's/SELECT 2/SELECT 1/' "$root/integrations/cockroach/revocations.go" >"$scratch/restored.go"
cp "$scratch/restored.go" "$root/integrations/cockroach/revocations.go"
sed 's/const query = `SELECT 1`/const query = `SELECT 1`; const added = `SELECT 2`/' "$root/integrations/cockroach/revocations.go" >"$scratch/same_line.go"
cp "$scratch/same_line.go" "$root/integrations/cockroach/revocations.go"
reject same_line_sql
sed 's/; const added = `SELECT 2`//' "$root/integrations/cockroach/revocations.go" >"$scratch/restored.go"
cp "$scratch/restored.go" "$root/integrations/cockroach/revocations.go"

sed '/adapter-sql-allowlist:/d' "$root/operations/cockroach/store.go" >"$scratch/no_marker.go"
cp "$scratch/no_marker.go" "$root/operations/cockroach/store.go"
reject missing_marker
printf '%s\n' '// adapter-sql-allowlist: virtual-catalog' >>"$root/operations/cockroach/store.go"

# A block comment, trailing comment and test file are not executable SQL.
cat >"$root/operations/cockroach/commented.go" <<'EOF'
package cockroach
/*
CREATE TABLE not_a_query (id INT);
*/
const explanation = "No database call" // DELETE FROM not_a_query
const prefix = "SELECT "
func shadowedPrefix() string {
 const prefix = "not SQL "
 return prefix + "count(*) FROM users"
}
EOF
printf 'package cockroach\nconst testQuery = `SELECT 1`\n' >"$root/operations/cockroach/commented_test.go"
check > /dev/null

echo 'adapter SQL AST allowlist rejects SQL additions/replacements and ignores comments/tests'
