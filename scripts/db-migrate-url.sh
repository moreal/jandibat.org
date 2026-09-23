#!/bin/sh
set -eu

database_url=${MIGRATION_DATABASE_URL:-${DATABASE_URL:-}}
if [ -z "$database_url" ]; then
	echo "MIGRATION_DATABASE_URL or DATABASE_URL is required" >&2
	exit 2
fi

database=${COCKROACH_DATABASE:-jandibat}
migrations_dir=${MIGRATIONS_DIR:-db/migrations}
sql_bin=${COCKROACH_SQL_BIN:-cockroach}

case "$database" in
	*[!A-Za-z0-9_]*|'')
		echo "COCKROACH_DATABASE must contain only letters, digits, and underscores" >&2
		exit 2
		;;
esac
if [ ! -d "$migrations_dir" ]; then
	echo "migration directory not found: $migrations_dir" >&2
	exit 2
fi
if ! command -v "$sql_bin" >/dev/null 2>&1; then
	echo "Cockroach SQL client not found: $sql_bin" >&2
	exit 127
fi

sql() {
	"$sql_bin" sql --url="$database_url" --set=errexit=true "$@"
}

transaction_file=
schema_unlocked=
relock_schema() {
	[ -n "$schema_unlocked" ] || return 0
	sql --execute="ALTER TABLE $schema_unlocked SET (schema_locked = true)"
	schema_unlocked=
}
cleanup() {
	if [ -n "$schema_unlocked" ]; then
		relock_schema || echo "failed to restore schema lock on $schema_unlocked" >&2
	fi
	if [ -n "$transaction_file" ] && [ -f "$transaction_file" ]; then
		rm -f "$transaction_file"
	fi
}
trap cleanup EXIT HUP INT TERM

checksum() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

connected_database=$(sql --format=tsv --execute="SELECT current_database()" | tail -n 1 | tr -d '\r')
if [ "$connected_database" != "$database" ]; then
	echo "migration database URL points to '$connected_database', expected '$database'" >&2
	exit 2
fi

sql --execute="
CREATE TABLE IF NOT EXISTS schema_migrations (
  version STRING PRIMARY KEY,
  checksum STRING NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)"

if [ -f "$migrations_dir/0001_baseline.sql" ]; then
	legacy=$(sql --format=tsv --execute="
SELECT CASE WHEN
  EXISTS (SELECT 1 FROM schema_migrations WHERE version <> '0001_baseline.sql')
  OR (
    NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '0001_baseline.sql')
    AND EXISTS (
      SELECT 1 FROM information_schema.tables
      WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
    )
  ) THEN 1 ELSE 0 END" | tail -n 1 | tr -d '\r')
	if [ "$legacy" != 0 ]; then
		echo "legacy or unmanaged schema detected; baseline requires a new database" >&2
		exit 1
	fi
fi

for migration in "$migrations_dir"/*.sql; do
	version=${migration##*/}
	case "$version" in
		*[!A-Za-z0-9_.-]*)
			echo "unsafe migration filename: $version" >&2
			exit 2
			;;
	esac
	want=$(checksum "$migration")
	schema_unlock_table=$(sed -n 's/^-- jandibat:schema-unlock \([A-Za-z0-9_]*\)$/\1/p' "$migration")
	case "$schema_unlock_table" in *[!A-Za-z0-9_]* ) echo "unsafe schema unlock directive: $version" >&2; exit 2 ;; esac
	[ "$(printf '%s\n' "$schema_unlock_table" | awk 'NF { count++ } END { print count + 0 }')" -le 1 ] || { echo "multiple schema unlock directives: $version" >&2; exit 2; }
	got=$(sql --format=tsv \
		--execute="SELECT COALESCE(max(checksum), '') AS checksum FROM schema_migrations WHERE version = '$version'" \
		| tail -n 1 | tr -d '\r')
	if [ -n "$got" ]; then
		if [ "$got" != "$want" ]; then
			echo "migration checksum mismatch: $version" >&2
			exit 1
		fi
		echo "already applied: $version"
		if [ -n "$schema_unlock_table" ]; then
			schema_unlocked=$schema_unlock_table
			relock_schema
		fi
		continue
	fi

	echo "applying: $version"
	if [ -n "$schema_unlock_table" ]; then
		sql --execute="ALTER TABLE $schema_unlock_table SET (schema_locked = false)"
		schema_unlocked=$schema_unlock_table
	fi
	transaction_file=$(mktemp "${TMPDIR:-/tmp}/jandibat-migration.XXXXXX")
	chmod 600 "$transaction_file"
	{
		printf '%s\n' 'SET autocommit_before_ddl = off;' 'BEGIN;'
		sed -n 'p' "$migration"
		printf '\nINSERT INTO schema_migrations (version, checksum) VALUES ('"'"'%s'"'"', '"'"'%s'"'"');\nCOMMIT;\n' "$version" "$want"
	} >"$transaction_file"
	if ! sql <"$transaction_file"; then
		relock_schema
		exit 1
	fi
	rm -f "$transaction_file"
	transaction_file=
	relock_schema
done
