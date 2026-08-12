#!/bin/sh
set -eu

: "${MIGRATION_DATABASE_URL:?MIGRATION_DATABASE_URL is required}"
: "${BACKUP_EXTERNAL_CONNECTION:?BACKUP_EXTERNAL_CONNECTION is required}"
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
database=${COCKROACH_DATABASE:-jandibat}

case "$BACKUP_EXTERNAL_CONNECTION" in
	*[!A-Za-z0-9_]*|'') echo "BACKUP_EXTERNAL_CONNECTION must be a safe identifier" >&2; exit 2 ;;
esac

query() {
	"$sql_bin" sql --url="$MIGRATION_DATABASE_URL" --format=tsv --set=errexit=true --execute="$1"
}

users=$(query "SHOW USERS" | tr -d '\r')
for role in jandibat_backup jandibat_restore; do
	if ! printf '%s\n' "$users" | awk -F '\t' -v role="$role" 'NR > 1 && $1 == role { found = 1 } END { exit !found }'; then
		echo "required backup/restore role is missing: $role" >&2
		exit 1
	fi
done

database_grants=$(query "SHOW GRANTS ON DATABASE $database" | tr -d '\r')
system_grants=$(query "SHOW SYSTEM GRANTS" | tr -d '\r')
connection_grants=$(query "SHOW GRANTS ON EXTERNAL CONNECTION $BACKUP_EXTERNAL_CONNECTION" | tr -d '\r')

if ! printf '%s\n' "$database_grants" | awk -F '\t' '$2 == "jandibat_backup" && ($3 == "BACKUP" || $3 == "ALL") { found=1 } END { exit !found }'; then
	echo "jandibat_backup is missing database BACKUP" >&2; exit 1
fi
if printf '%s\n' "$database_grants" | awk -F '\t' '$2 == "jandibat_backup" && ($3 == "RESTORE" || $3 == "ALL") { found=1 } END { exit !found }'; then
	echo "jandibat_backup must not have database RESTORE" >&2; exit 1
fi
if printf '%s\n' "$system_grants" | awk -F '\t' '$1 == "jandibat_backup" && ($2 == "RESTORE" || $2 == "ALL") { found=1 } END { exit !found }'; then
	echo "jandibat_backup must not have system RESTORE" >&2; exit 1
fi
if ! printf '%s\n' "$system_grants" | awk -F '\t' '$1 == "jandibat_restore" && ($2 == "RESTORE" || $2 == "ALL") { found=1 } END { exit !found }'; then
	echo "jandibat_restore is missing system RESTORE" >&2; exit 1
fi
for role in jandibat_backup jandibat_restore; do
	if ! printf '%s\n' "$connection_grants" | awk -F '\t' -v role="$role" '$2 == role && ($3 == "USAGE" || $3 == "ALL") { found=1 } END { exit !found }'; then
		echo "$role is missing external connection USAGE" >&2; exit 1
	fi
done

echo "backup-only and short-lived restore role boundaries passed"
