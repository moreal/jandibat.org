#!/bin/sh
set -eu

database_url=${MIGRATION_DATABASE_URL:-${DATABASE_URL:-}}
database=${COCKROACH_DATABASE:-jandibat}
sql_bin=${COCKROACH_SQL_BIN:-cockroach}

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
if ! command -v "$sql_bin" >/dev/null 2>&1; then
	echo "Cockroach SQL client not found: $sql_bin" >&2
	exit 127
fi

sql() {
	"$sql_bin" sql --url="$database_url" --set=errexit=true "$@"
}

connected_database=$(sql --format=tsv --execute="SELECT current_database()" | tail -n 1 | tr -d '\r')
if [ "$connected_database" != "$database" ]; then
	echo "role configuration URL points to '$connected_database', expected '$database'" >&2
	exit 2
fi

users=$(sql --format=tsv --execute="SHOW USERS" | tr -d '\r')
for role in jandibat_migrator jandibat_api jandibat_worker jandibat_maintenance; do
	if ! printf '%s\n' "$users" | awk -F '\t' -v role="$role" 'NR > 1 && $1 == role && $2 !~ /NOLOGIN/ { found = 1 } END { exit !found }'; then
		echo "required LOGIN database user does not exist: $role" >&2
		exit 2
	fi
done

all_runtime_tables='users, user_settings, subjects, subject_settings, user_passkeys, magic_link_tokens, magic_link_mail_outbox, user_sessions, auth_challenges, environments, provider_connections, provider_connection_private_consents, provider_sync_jobs, provider_token_revocation_jobs, custom_providers, custom_provider_secrets, activity_facts, custom_activity_events, ingest_idempotency_keys, timeline_cache, activity_refresh_cache, api_rate_limit_buckets, audit_events, mutation_audit_outbox, maintenance_checkpoints, legal_holds, deletion_request_inbox, deletion_requests, deletion_request_claims, deleted_identity_tombstones, deleted_identity_tombstones_v2'

sql --execute="
REVOKE ALL ON TABLE $all_runtime_tables FROM jandibat_api;
REVOKE ALL ON TABLE $all_runtime_tables FROM jandibat_worker;
REVOKE ALL ON TABLE $all_runtime_tables FROM jandibat_maintenance;

GRANT CONNECT ON DATABASE $database TO jandibat_api, jandibat_worker, jandibat_maintenance;
GRANT USAGE ON SCHEMA public TO jandibat_api, jandibat_worker, jandibat_maintenance;
REVOKE CREATE ON SCHEMA public FROM public;
REVOKE CREATE ON SCHEMA public FROM jandibat_api, jandibat_worker, jandibat_maintenance;
GRANT CONNECT ON DATABASE $database TO jandibat_migrator;
GRANT USAGE, CREATE ON SCHEMA public TO jandibat_migrator;

GRANT SELECT ON TABLE
  users, user_settings, subjects, subject_settings, magic_link_tokens,
  user_sessions, user_passkeys, auth_challenges, environments, activity_facts,
  activity_refresh_cache, provider_connections, provider_connection_private_consents, provider_sync_jobs,
  custom_providers, custom_provider_secrets, custom_activity_events, ingest_idempotency_keys,
  api_rate_limit_buckets
TO jandibat_api;
GRANT SELECT ON TABLE deletion_request_inbox, deleted_identity_tombstones, deleted_identity_tombstones_v2, magic_link_mail_outbox TO jandibat_api;
GRANT INSERT ON TABLE
  users, user_settings, subjects, subject_settings,
  user_sessions, user_passkeys, auth_challenges, environments, activity_facts,
  activity_refresh_cache, provider_connections, provider_connection_private_consents, provider_sync_jobs,
  custom_providers, custom_provider_secrets, custom_activity_events, ingest_idempotency_keys,
  api_rate_limit_buckets, audit_events, mutation_audit_outbox, deletion_request_inbox, magic_link_mail_outbox
TO jandibat_api;
GRANT INSERT ON TABLE provider_token_revocation_jobs TO jandibat_api;
GRANT UPDATE ON TABLE
  users, user_settings, subjects, subject_settings, magic_link_tokens,
  user_sessions, user_passkeys, auth_challenges, environments, activity_facts,
  activity_refresh_cache, provider_connections, provider_connection_private_consents, provider_sync_jobs,
  custom_providers, custom_provider_secrets, ingest_idempotency_keys, api_rate_limit_buckets
TO jandibat_api;
GRANT UPDATE ON TABLE magic_link_mail_outbox TO jandibat_api;
GRANT DELETE ON TABLE provider_connection_private_consents TO jandibat_api;
GRANT DELETE ON TABLE
  user_passkeys, custom_providers, custom_provider_secrets, custom_activity_events, provider_connection_private_consents, ingest_idempotency_keys,
  activity_facts, activity_refresh_cache, provider_sync_jobs
TO jandibat_api;

GRANT SELECT ON TABLE
  provider_connections, provider_connection_private_consents, provider_sync_jobs, provider_token_revocation_jobs,
  magic_link_mail_outbox, mutation_audit_outbox,
  subjects, subject_settings, environments, activity_facts, custom_providers
TO jandibat_worker;
GRANT INSERT ON TABLE
  provider_sync_jobs, environments, activity_facts, audit_events
TO jandibat_worker;
GRANT UPDATE ON TABLE
  provider_connections, provider_sync_jobs, provider_token_revocation_jobs, magic_link_mail_outbox, mutation_audit_outbox,
  environments, activity_facts
TO jandibat_worker;
GRANT DELETE ON TABLE activity_facts, provider_token_revocation_jobs TO jandibat_worker;

GRANT SELECT ON TABLE
  provider_connections, provider_connection_private_consents, audit_events, activity_facts, custom_activity_events,
  environments, provider_sync_jobs, user_sessions, magic_link_tokens,
  auth_challenges, user_passkeys, ingest_idempotency_keys, timeline_cache,
  activity_refresh_cache, api_rate_limit_buckets, provider_token_revocation_jobs,
  magic_link_mail_outbox, mutation_audit_outbox, maintenance_checkpoints, legal_holds, deletion_request_inbox, deletion_requests, deletion_request_claims, users, subjects,
  subject_settings, user_settings, custom_providers, custom_provider_secrets, deleted_identity_tombstones, deleted_identity_tombstones_v2
TO jandibat_maintenance;
GRANT INSERT ON TABLE
  maintenance_checkpoints, deletion_requests, deletion_request_claims, provider_token_revocation_jobs,
  deleted_identity_tombstones, deleted_identity_tombstones_v2
TO jandibat_maintenance;
GRANT UPDATE ON TABLE
  provider_connections, provider_token_revocation_jobs, maintenance_checkpoints, activity_facts,
  deletion_request_inbox, deletion_requests, deletion_request_claims, users, subjects,
  deleted_identity_tombstones_v2
TO jandibat_maintenance;
GRANT DELETE ON TABLE
  users, user_settings, subjects, subject_settings, user_passkeys, activity_facts, custom_activity_events,
  custom_providers, custom_provider_secrets, provider_connections, provider_connection_private_consents, environments, provider_sync_jobs,
  user_sessions, magic_link_tokens,
  auth_challenges, ingest_idempotency_keys, timeline_cache,
  activity_refresh_cache, api_rate_limit_buckets, deletion_request_inbox, deletion_request_claims,
  deleted_identity_tombstones, deleted_identity_tombstones_v2, magic_link_mail_outbox, mutation_audit_outbox,
  maintenance_checkpoints
TO jandibat_maintenance;
GRANT INSERT ON TABLE audit_events TO jandibat_maintenance;
"

echo "configured least-privilege runtime grants for $database"
