#!/bin/sh
set -eu

: "${BACKUP_EXTERNAL_CONNECTION:?BACKUP_EXTERNAL_CONNECTION is required}"

database_url=${RESTORE_DATABASE_URL:-}
source_database_url=${SOURCE_DATABASE_URL:-${MIGRATION_DATABASE_URL:-}}
database=${COCKROACH_DATABASE:-jandibat}
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
suffix=${RESTORE_SUFFIX:-$(date -u +%Y%m%d%H%M%S)}
keep=${RESTORE_KEEP_DATABASE:-false}
require_full=${RESTORE_REQUIRE_FULL_DRILL:-false}
max_rpo=${RESTORE_MAX_RPO_SECONDS:-3600}
max_rto=${RESTORE_MAX_RTO_SECONDS:-14400}
drill_started_at=${RESTORE_DRILL_STARTED_AT:-}
restore_maintenance_url=${RESTORE_MAINTENANCE_DATABASE_URL:-}
restore_maintenance_url_template=${RESTORE_MAINTENANCE_DATABASE_URL_TEMPLATE:-}
maintenance_bin=${RESTORE_MAINTENANCE_BIN:-}
api_bin=${RESTORE_API_BIN:-}
api_database_url_template=${RESTORE_API_DATABASE_URL_TEMPLATE:-}
api_base_url=${RESTORE_API_BASE_URL:-}
fixture_subject=${RESTORE_FIXTURE_SUBJECT:-}
http_bin=${RESTORE_HTTP_BIN:-curl}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
api_smoke_result=not-run
deletion_replay_result=not-run
audit_reconciliation_result=not-run
tmp_files=
api_pid=

if [ -z "$database_url" ]; then echo "RESTORE_DATABASE_URL is required" >&2; exit 2; fi
if [ -z "$source_database_url" ]; then echo "SOURCE_DATABASE_URL or MIGRATION_DATABASE_URL is required" >&2; exit 2; fi
for value in "$database" "$BACKUP_EXTERNAL_CONNECTION" "$suffix"; do
	case "$value" in *[!A-Za-z0-9_]*|'') echo "database, external connection, and suffix must be safe identifiers" >&2; exit 2 ;; esac
done
for value in "$keep" "$require_full"; do
	case "$value" in true|false) ;; *) echo "boolean restore settings must be true or false" >&2; exit 2 ;; esac
done
for value in "$max_rpo" "$max_rto"; do
	case "$value" in ''|*[!0-9]*) echo "RPO/RTO limits must be integer seconds" >&2; exit 2 ;; esac
	[ "$value" -gt 0 ] || { echo "RPO/RTO limits must be positive" >&2; exit 2; }
done
if ! command -v "$sql_bin" >/dev/null 2>&1; then echo "Cockroach SQL client not found: $sql_bin" >&2; exit 127; fi

restore_database="${database}_restore_${suffix}"
cleanup() {
	if [ -n "$api_pid" ]; then kill "$api_pid" >/dev/null 2>&1 || true; wait "$api_pid" >/dev/null 2>&1 || true; fi
	for file in $tmp_files; do [ ! -f "$file" ] || rm -f "$file"; done
	if [ "$keep" = false ]; then
		"$sql_bin" sql --url="$database_url" --set=errexit=true \
			--execute="DROP DATABASE IF EXISTS $restore_database CASCADE" >/dev/null || true
	fi
}
trap cleanup EXIT HUP INT TERM

scalar() {
	url=$1
	statement=$2
	result=$(mktemp "${TMPDIR:-/tmp}/jandibat-restore-query.XXXXXX")
	tmp_files="$tmp_files $result"
	if ! "$sql_bin" sql --url="$url" --format=tsv --set=errexit=true --execute="$statement" >"$result"; then
		echo "restore verification SQL failed" >&2
		return 1
	fi
	tail -n 1 "$result" | tr -d '\r'
}

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
if [ -z "$drill_started_at" ]; then drill_started_at=$started_at; fi
case "$drill_started_at" in
	????-??-??T??:??:??Z) ;;
	*) echo "RESTORE_DRILL_STARTED_AT must be an RFC3339 UTC timestamp" >&2; exit 2 ;;
esac

backup_end=$(scalar "$database_url" "SELECT max(end_time)::STRING FROM [SHOW BACKUP FROM LATEST IN 'external://$BACKUP_EXTERNAL_CONNECTION']")
case "$backup_end" in ''|NULL) echo "latest backup has no end timestamp" >&2; exit 1 ;; esac
rpo_seconds=$(scalar "$database_url" "SELECT greatest(0, extract(epoch FROM now() - '$backup_end'::TIMESTAMPTZ)::INT8)")

"$sql_bin" sql --url="$database_url" --set=errexit=true \
	--execute="RESTORE DATABASE $database FROM LATEST IN 'external://$BACKUP_EXTERNAL_CONNECTION' WITH new_db_name = '$restore_database'" >&2

source_migrations=$(scalar "$source_database_url" "SELECT COALESCE(string_agg(version || ':' || checksum, ',' ORDER BY version), '') FROM $database.public.schema_migrations")
restore_migrations=$(scalar "$database_url" "SELECT COALESCE(string_agg(version || ':' || checksum, ',' ORDER BY version), '') FROM $restore_database.public.schema_migrations")
if [ -z "$source_migrations" ] || [ "$source_migrations" != "$restore_migrations" ]; then
	echo "restore migration checksums do not match the source database" >&2
	exit 1
fi

expected_tables='activity_facts activity_refresh_cache api_rate_limit_buckets audit_events auth_challenges custom_activity_events custom_providers deleted_identity_tombstones deleted_identity_tombstones_v2 deletion_request_claims deletion_request_inbox deletion_requests environments ingest_idempotency_keys legal_holds magic_link_mail_outbox magic_link_tokens maintenance_checkpoints mutation_audit_outbox provider_connection_private_consents provider_connections provider_sync_jobs provider_token_revocation_jobs schema_migrations subject_settings subjects timeline_cache user_passkeys user_sessions user_settings users'
row_counts=''
for table in $expected_tables; do
	present=$(scalar "$database_url" "SELECT count(*) FROM $restore_database.information_schema.tables WHERE table_schema = 'public' AND table_name = '$table'")
	[ "$present" = 1 ] || { echo "restore is missing expected table: $table" >&2; exit 1; }
done

source_constraints=$(scalar "$source_database_url" "SELECT COALESCE(string_agg(table_name || ':' || constraint_name || ':' || constraint_type, ',' ORDER BY table_name, constraint_name), '') FROM $database.information_schema.table_constraints WHERE table_schema = 'public'")
restore_constraints=$(scalar "$database_url" "SELECT COALESCE(string_agg(table_name || ':' || constraint_name || ':' || constraint_type, ',' ORDER BY table_name, constraint_name), '') FROM $restore_database.information_schema.table_constraints WHERE table_schema = 'public'")
[ "$source_constraints" = "$restore_constraints" ] || { echo "restore constraint inventory differs from source" >&2; exit 1; }
source_indexes=$(scalar "$source_database_url" "SELECT COALESCE(string_agg(table_name || ':' || index_name || ':' || non_unique || ':' || seq_in_index || ':' || column_name || ':' || direction || ':' || storing, ',' ORDER BY table_name, index_name, seq_in_index), '') FROM $database.information_schema.statistics WHERE table_schema = 'public'")
restore_indexes=$(scalar "$database_url" "SELECT COALESCE(string_agg(table_name || ':' || index_name || ':' || non_unique || ':' || seq_in_index || ':' || column_name || ':' || direction || ':' || storing, ',' ORDER BY table_name, index_name, seq_in_index), '') FROM $restore_database.information_schema.statistics WHERE table_schema = 'public'")
[ "$source_indexes" = "$restore_indexes" ] || { echo "restore index inventory differs from source" >&2; exit 1; }

for table in $expected_tables; do
	source_count=$(scalar "$source_database_url" "SELECT count(*) FROM $database.public.$table AS OF SYSTEM TIME '$backup_end'")
	restore_count=$(scalar "$database_url" "SELECT count(*) FROM $restore_database.public.$table")
	[ "$source_count" = "$restore_count" ] || { echo "restore row count differs from backup-time source for $table: $source_count != $restore_count" >&2; exit 1; }
	entry=$(printf '{"table":"%s","sourceAtBackup":%s,"restored":%s}' "$table" "$source_count" "$restore_count")
	if [ -z "$row_counts" ]; then row_counts=$entry; else row_counts="$row_counts,$entry"; fi
done

invariant_summary=$(scalar "$database_url" "
SELECT concat_ws(E'\\t',
  (SELECT count(*) FROM $restore_database.public.deletion_requests
    WHERE (status = 'completed') <> (completed_at IS NOT NULL)),
  (SELECT count(*) FROM $restore_database.public.deletion_request_claims
    WHERE (lease_until IS NULL) <> (claim_token IS NULL)),
  (SELECT count(*) FROM $restore_database.public.magic_link_mail_outbox
    WHERE (status = 'processing') <> (lease_until IS NOT NULL AND claim_token IS NOT NULL)),
  (SELECT count(*) FROM $restore_database.public.deleted_identity_tombstones_v2
    WHERE length(identity_digest) <> 32 OR expires_at <= created_at),
  (SELECT count(*) FROM $restore_database.public.deletion_requests AS request
    LEFT JOIN $restore_database.public.audit_events AS event ON event.id = request.audit_event_id
    WHERE request.status = 'completed' AND (
      event.id IS NULL OR event.action <> 'deletion.completed' OR event.outcome <> 'succeeded'
      OR event.request_id <> request.request_id)),
  (SELECT count(*) FROM $restore_database.public.mutation_audit_outbox AS outbox
    LEFT JOIN $restore_database.public.audit_events AS event ON event.id = outbox.audit_event_id
    WHERE outbox.status = 'delivered' AND (
      event.id IS NULL OR event.request_id <> outbox.request_id OR event.occurred_at <> outbox.occurred_at
      OR event.actor_type <> outbox.actor_type OR event.actor_id IS DISTINCT FROM outbox.actor_id
      OR event.action <> outbox.action OR event.target_type <> outbox.target_type
      OR event.target_id IS DISTINCT FROM outbox.target_id OR event.outcome <> outbox.outcome
      OR event.metadata IS DISTINCT FROM outbox.metadata)))")
case "$invariant_summary" in '0	0	0	0	0	0') ;; *) echo "restored data invariant violation: $invariant_summary" >&2; exit 1 ;; esac

post_backup_deletions=$(scalar "$source_database_url" "SELECT count(*) FROM $database.public.deletion_requests WHERE status = 'completed' AND completed_at > '$backup_end'::TIMESTAMPTZ")

if [ -z "$restore_maintenance_url" ] && [ -n "$restore_maintenance_url_template" ]; then
	restore_maintenance_url=$(printf '%s' "$restore_maintenance_url_template" | sed "s/{database}/$restore_database/g")
fi
if [ -n "$restore_maintenance_url" ]; then
	bound_database=$(scalar "$restore_maintenance_url" "SELECT current_database()")
	[ "$bound_database" = "$restore_database" ] || { echo "RESTORE_MAINTENANCE_DATABASE_URL must be bound to $restore_database" >&2; exit 1; }
fi

if [ -z "$api_base_url" ] && [ -n "$api_database_url_template" ] && [ -n "$api_bin" ] && command -v "$api_bin" >/dev/null 2>&1; then
	restore_api_url=$(printf '%s' "$api_database_url_template" | sed "s/{database}/$restore_database/g")
	bound_api_database=$(scalar "$restore_api_url" "SELECT current_database()")
	[ "$bound_api_database" = "$restore_database" ] || { echo "RESTORE_API_DATABASE_URL_TEMPLATE must bind to $restore_database" >&2; exit 1; }
	api_log=$(mktemp "${TMPDIR:-/tmp}/jandibat-restore-api.XXXXXX")
	tmp_files="$tmp_files $api_log"
	(
		unset CREDENTIAL_ENCRYPTION_PRIVATE_KEYS CREDENTIAL_ENCRYPTION_KEYS CREDENTIAL_ENCRYPTION_KEY DELETION_PSEUDONYM_KEY MAINTENANCE_DATABASE_URL WORKER_DATABASE_URL
		export DATABASE_URL="$restore_api_url" API_ADDR=:18083
		exec "$api_bin"
	) >"$api_log" 2>&1 &
	api_pid=$!
	api_base_url=http://127.0.0.1:18083
	attempt=1
	while [ "$attempt" -le 30 ]; do
		if "$http_bin" wget --spider -q -T 3 "$api_base_url/readyz"; then break; fi
		if ! kill -0 "$api_pid" 2>/dev/null; then echo "restored API exited before readiness" >&2; tail -n 50 "$api_log" >&2; api_smoke_result=failed; break; fi
		sleep 1
		attempt=$((attempt + 1))
	done
fi

if [ "$post_backup_deletions" -eq 0 ]; then
	deletion_replay_result=passed
elif [ -n "$restore_maintenance_url" ] && [ -n "$maintenance_bin" ] && command -v "$maintenance_bin" >/dev/null 2>&1; then
	replay_rows=$(mktemp "${TMPDIR:-/tmp}/jandibat-restore-replay.XXXXXX")
	tmp_files="$tmp_files $replay_rows"
	chmod 600 "$replay_rows"
	if ! "$sql_bin" sql --url="$source_database_url" --format=tsv --set=errexit=true --execute="SELECT request_id, target_type, target_id FROM $database.public.deletion_requests WHERE status = 'completed' AND completed_at > '$backup_end'::TIMESTAMPTZ ORDER BY completed_at, id" >"$replay_rows"; then
		echo "could not export the post-backup deletion replay manifest" >&2
		deletion_replay_result=failed
	else
		deletion_replay_result=passed
		while IFS="$(printf '\t')" read -r request_id target_type target_id; do
			[ "$request_id" != request_id ] || continue
			if ! MAINTENANCE_DATABASE_URL="$restore_maintenance_url" "$maintenance_bin" delete --request-id "$request_id" --target-type "$target_type" --target-id "$target_id" >/dev/null ||
			   ! MAINTENANCE_DATABASE_URL="$restore_maintenance_url" "$maintenance_bin" verify-deletion --request-id "$request_id" >/dev/null; then
				deletion_replay_result=failed
				break
			fi
		done <"$replay_rows"
	fi
fi

if [ -n "$restore_maintenance_url" ]; then
	audit_output=$(mktemp "${TMPDIR:-/tmp}/jandibat-restore-audit.XXXXXX")
	tmp_files="$tmp_files $audit_output"
	if AUDIT_DATABASE_URL="$restore_maintenance_url" COCKROACH_SQL_BIN="$sql_bin" sh "$script_dir/verify-audit-log.sh" --window 400d --orphan-age 5m --output "$audit_output" >/dev/null; then
		audit_reconciliation_result=passed
	else
		audit_reconciliation_result=failed
	fi
fi

if [ -n "$api_base_url" ] && [ -n "$fixture_subject" ]; then
	case "$fixture_subject" in *[!A-Za-z0-9._~-]*) echo "RESTORE_FIXTURE_SUBJECT must be URL-path safe" >&2; api_smoke_result=failed ;;
	*)
		if command -v "$http_bin" >/dev/null 2>&1 &&
		   "$http_bin" wget -q -O /dev/null -T 5 "$api_base_url/readyz" &&
		   "$http_bin" wget -q -O /dev/null -T 5 "$api_base_url/v1/activities/$fixture_subject" &&
		   "$http_bin" wget -q -O /dev/null -T 5 "$api_base_url/v1/render/$fixture_subject.svg"; then
			api_smoke_result=passed
		else
			api_smoke_result=failed
		fi
		;;
	esac
fi

finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
rto_seconds=$(scalar "$database_url" "SELECT greatest(0, extract(epoch FROM now() - '$drill_started_at'::TIMESTAMPTZ)::INT8)")
migration_count=$(printf '%s' "$source_migrations" | awk -F, '{print NF}')

full_result=passed
full_reason='all required restore drill evidence supplied'
if [ "$rpo_seconds" -gt "$max_rpo" ] || [ "$rto_seconds" -gt "$max_rto" ]; then
	full_result=failed
	full_reason='RPO or RTO threshold exceeded'
elif [ "$deletion_replay_result" = failed ] || [ "$api_smoke_result" = failed ] || [ "$audit_reconciliation_result" = failed ]; then
	full_result=failed
	full_reason='restore API, deletion replay, or audit reconciliation failed'
elif [ "$post_backup_deletions" -gt 0 ] && [ "$deletion_replay_result" != passed ]; then
	full_result=not-run
	full_reason='post-backup deletions require replay and residual verification'
elif [ "$api_smoke_result" != passed ] || [ "$audit_reconciliation_result" != passed ]; then
	full_result=not-run
	full_reason='restored API smoke or audit reconciliation evidence missing'
fi

printf '{"schemaVersion":2,"startedAt":"%s","finishedAt":"%s","drillStartedAt":"%s","backupEnd":"%s","sourceDatabase":"%s","restoreDatabase":"%s","migrationCount":%s,"rpoSeconds":%s,"rtoSeconds":%s,"maxRpoSeconds":%s,"maxRtoSeconds":%s,"postBackupCompletedDeletions":%s,"apiSmoke":"%s","deletionReplay":"%s","auditReconciliation":"%s","rowCounts":[%s],"invariantViolations":[0,0,0,0,0,0],"result":"%s","reason":"%s","temporaryDatabaseRetained":%s}\n' \
	"$started_at" "$finished_at" "$drill_started_at" "$backup_end" "$database" "$restore_database" "$migration_count" \
	"$rpo_seconds" "$rto_seconds" "$max_rpo" "$max_rto" "$post_backup_deletions" \
	"${api_smoke_result:-not-run}" "${deletion_replay_result:-not-run}" "${audit_reconciliation_result:-not-run}" "$row_counts" \
	"$full_result" "$full_reason" "$keep"

if [ "$full_result" = failed ] || { [ "$require_full" = true ] && [ "$full_result" != passed ]; }; then exit 1; fi
