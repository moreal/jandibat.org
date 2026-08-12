#!/bin/sh
set -eu

database_url=${MIGRATION_DATABASE_URL:-${DATABASE_URL:-}}
database=${COCKROACH_DATABASE:-jandibat}
migrations_dir=${MIGRATIONS_DIR:-db/migrations}
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
repair=false

if [ "${1:-}" = "--repair" ]; then
	repair=true
	shift
fi
if [ "$#" -ne 0 ]; then
	echo "usage: db-verify-schema-locks.sh [--repair]" >&2
	exit 2
fi
if [ -z "$database_url" ]; then
	echo "MIGRATION_DATABASE_URL or DATABASE_URL is required" >&2
	exit 2
fi
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

connected_database=$(sql --format=tsv --execute="SELECT current_database()" | tail -n 1 | tr -d '\r')
if [ "$connected_database" != "$database" ]; then
	echo "migration database URL points to '$connected_database', expected '$database'" >&2
	exit 2
fi

tables=$(
	for migration in "$migrations_dir"/*.sql; do
		sed -n 's/^-- jandibat:schema-unlock \([A-Za-z0-9_]*\)$/\1/p' "$migration"
	done | sort -u
)

for table in $tables; do
	case "$table" in
		*[!A-Za-z0-9_]*|'')
			echo "unsafe schema-lock table identifier" >&2
			exit 2
			;;
	esac
	if [ "$repair" = true ]; then
		sql --execute="ALTER TABLE $table SET (schema_locked = true)" >/dev/null
	fi
	locked=$(sql --format=tsv --execute="SELECT count(*) FROM [SHOW CREATE TABLE $table] WHERE create_statement LIKE '%schema_locked = true%'" | tail -n 1 | tr -d '\r')
	if [ "$locked" != "1" ]; then
		echo "schema lock is not enabled: $table" >&2
		exit 1
	fi
	echo "schema lock verified: $table"
done
