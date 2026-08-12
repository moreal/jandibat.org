#!/bin/sh
set -eu

: "${BACKUP_DATABASE_URL:?BACKUP_DATABASE_URL is required}"
: "${BACKUP_EXTERNAL_CONNECTION:?BACKUP_EXTERNAL_CONNECTION is required}"

database=${COCKROACH_DATABASE:-jandibat}
label=${BACKUP_SCHEDULE_LABEL:-jandibat_hourly_backup}
sql_bin=${COCKROACH_SQL_BIN:-cockroach}

for value in "$database" "$label" "$BACKUP_EXTERNAL_CONNECTION"; do
	case "$value" in
		*[!A-Za-z0-9_]*|'') echo "database, schedule label, and external connection must be safe identifiers" >&2; exit 2 ;;
	esac
done

sql() {
	"$sql_bin" sql --url="$BACKUP_DATABASE_URL" --set=errexit=true "$@"
}

existing=$(sql --format=tsv --execute="SELECT count(*) FROM [SHOW SCHEDULES] WHERE label = '$label'" | tail -n 1 | tr -d '\r')
case "$existing" in
	0)
		sql --execute="
CREATE SCHEDULE IF NOT EXISTS $label
FOR BACKUP DATABASE $database
INTO 'external://$BACKUP_EXTERNAL_CONNECTION'
WITH revision_history
RECURRING '10 * * * *'
FULL BACKUP '10 0 * * *'
WITH SCHEDULE OPTIONS
  first_run = 'now',
  on_execution_failure = 'retry',
  on_previous_running = 'wait',
  ignore_existing_backups"
		;;
	2) ;;
	*) echo "backup schedule '$label' has unexpected component count: $existing" >&2; exit 1 ;;
esac

summary=$(sql --format=tsv --execute="
SELECT count(*) AS components,
       count(*) FILTER (WHERE schedule_status = 'ACTIVE') AS active_components,
       count(*) FILTER (WHERE recurrence = '10 * * * *') AS hourly_components,
       count(*) FILTER (WHERE recurrence = '10 0 * * *') AS daily_components,
       count(*) FILTER (WHERE command::STRING LIKE '%external://$BACKUP_EXTERNAL_CONNECTION%') AS destination_components
FROM [SHOW SCHEDULES]
WHERE label = '$label'" | tail -n 1 | tr -d '\r')

components=$(printf '%s' "$summary" | awk -F '\t' '{print $1}')
active=$(printf '%s' "$summary" | awk -F '\t' '{print $2}')
hourly=$(printf '%s' "$summary" | awk -F '\t' '{print $3}')
daily=$(printf '%s' "$summary" | awk -F '\t' '{print $4}')
destination=$(printf '%s' "$summary" | awk -F '\t' '{print $5}')
if [ "$components" != 2 ] || [ "$hourly" != 1 ] || [ "$daily" != 1 ] || [ "$destination" != 2 ] || [ "$active" -lt 1 ]; then
	echo "backup schedule is not a valid hourly-incremental/daily-full pair: $summary" >&2
	exit 1
fi

latest_end=$(sql --format=tsv --execute="SELECT max(end_time) FROM [SHOW BACKUP FROM LATEST IN 'external://$BACKUP_EXTERNAL_CONNECTION']" | tail -n 1 | tr -d '\r')
fresh=$(sql --format=tsv --execute="SELECT '$latest_end'::TIMESTAMPTZ >= now() - INTERVAL '65 minutes'" | tail -n 1 | tr -d '\r')
if [ "$fresh" != true ]; then
	echo "latest validated backup is older than 65 minutes: $latest_end" >&2
	exit 1
fi

printf '{"checkedAt":"%s","schedule":"%s","components":%s,"activeComponents":%s,"latestBackupEnd":"%s","result":"passed"}\n' \
	"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$label" "$components" "$active" "$latest_end"
