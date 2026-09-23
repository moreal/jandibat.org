#!/bin/sh
set -eu

# Always create a new database; never delete an existing database or PVC.
database="jandibat_snapshot_$(date +%s)_$$"
COCKROACH_DATABASE="$database" sh scripts/db-migrate.sh >/dev/null

sql() {
	docker compose exec -T cockroach cockroach sql --insecure \
		--host=127.0.0.1:26258 --database="$database" --set=errexit=true \
		--format=tsv --execute="$1"
}

count=$(sql "SELECT count(*) FROM schema_migrations" | tail -n 1 | tr -d '\r')
if [ "$count" != 3 ]; then
	echo "snapshot migration history has $count entries, expected 3" >&2
	exit 1
fi

sql "INSERT INTO subjects (id, handle, display_name, timezone) VALUES ('snapshot-migration-subject', 'snapshot-migration-subject', 'Snapshot Migration', 'UTC')" >/dev/null
sql "INSERT INTO environments (id, key, name, scope) VALUES ('snapshot-migration-env', 'snapshot-migration-env', 'Snapshot Migration', 'global')" >/dev/null
sql "INSERT INTO activity_snapshot_changes (subject_id, environment_id, activity_date, visibility_scope, changed_at) VALUES ('snapshot-migration-subject', 'snapshot-migration-env', '2026-02-28', 'public', now())" >/dev/null
if sql "INSERT INTO activity_snapshot_changes (subject_id, environment_id, activity_date, visibility_scope, changed_at) VALUES ('snapshot-migration-subject', 'snapshot-migration-env', '2026-02-28', 'hidden', now())" >/dev/null 2>&1; then
	echo 'snapshot marker accepted an invalid visibility scope' >&2
	exit 1
fi
sql "DELETE FROM environments WHERE id = 'snapshot-migration-env'" >/dev/null
count=$(sql "SELECT count(*) FROM activity_snapshot_changes WHERE subject_id = 'snapshot-migration-subject' AND environment_id = 'snapshot-migration-env'" | tail -n 1 | tr -d '\r')
if [ "$count" != 1 ]; then
	echo 'snapshot marker was not retained after environment deletion' >&2
	exit 1
fi
sql "DELETE FROM subjects WHERE id = 'snapshot-migration-subject'" >/dev/null
count=$(sql "SELECT count(*) FROM activity_snapshot_changes WHERE subject_id = 'snapshot-migration-subject'" | tail -n 1 | tr -d '\r')
if [ "$count" != 0 ]; then
	echo 'snapshot marker did not cascade with subject deletion' >&2
	exit 1
fi

echo "snapshot migration and tombstone constraints passed in isolated database $database"
