#!/bin/sh
set -eu

: "${BACKUP_EXTERNAL_CONNECTION:?BACKUP_EXTERNAL_CONNECTION is required}"

database_url=${BACKUP_DATABASE_URL:-${DATABASE_URL:-}}
if [ -z "$database_url" ]; then
	echo "BACKUP_DATABASE_URL or DATABASE_URL is required" >&2
	exit 2
fi

database=${COCKROACH_DATABASE:-jandibat}
sql_bin=${COCKROACH_SQL_BIN:-cockroach}

case "$database" in
	*[!A-Za-z0-9_]*|'')
		echo "COCKROACH_DATABASE must contain only letters, digits, and underscores" >&2
		exit 2
		;;
esac
case "$BACKUP_EXTERNAL_CONNECTION" in
	*[!A-Za-z0-9_]*|'')
		echo "BACKUP_EXTERNAL_CONNECTION must be a Cockroach external connection name" >&2
		exit 2
		;;
esac
if ! command -v "$sql_bin" >/dev/null 2>&1; then
	echo "Cockroach SQL client not found: $sql_bin" >&2
	exit 127
fi

"$sql_bin" sql --url="$database_url" --set=errexit=true \
	--execute="CHECK EXTERNAL CONNECTION 'external://$BACKUP_EXTERNAL_CONNECTION'"
"$sql_bin" sql --url="$database_url" --set=errexit=true \
	--execute="BACKUP DATABASE $database INTO 'external://$BACKUP_EXTERNAL_CONNECTION' AS OF SYSTEM TIME '-10s' WITH revision_history"
"$sql_bin" sql --url="$database_url" --set=errexit=true \
	--execute="SHOW BACKUP FROM LATEST IN 'external://$BACKUP_EXTERNAL_CONNECTION' WITH check_files"

echo "backup completed and file presence was validated for database $database"
