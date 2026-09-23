#!/bin/sh
set -eu

: "${MIGRATION_DATABASE_URL:?MIGRATION_DATABASE_URL is required}"
: "${API_DATABASE_URL:?API_DATABASE_URL is required}"
: "${WORKER_DATABASE_URL:?WORKER_DATABASE_URL is required}"
: "${MAINTENANCE_DATABASE_URL:?MAINTENANCE_DATABASE_URL is required}"
sql_bin=${COCKROACH_SQL_BIN:-cockroach}

if ! command -v "$sql_bin" >/dev/null 2>&1; then
	echo "Cockroach SQL client not found: $sql_bin" >&2
	exit 127
fi

sql() {
	url=$1
	statement=$2
	"$sql_bin" sql --url="$url" --set=errexit=true --execute="$statement"
}

expect_denied() {
	name=$1
	url=$2
	statement=$3
	if sql "$url" "$statement" >/dev/null 2>&1; then
		echo "runtime role unexpectedly allowed forbidden operation: $name" >&2
		exit 1
	fi
}

sql "$API_DATABASE_URL" "SELECT count(*) FROM subjects" >/dev/null
sql "$API_DATABASE_URL" "SELECT ingest_token_hash FROM custom_provider_secrets WHERE false" >/dev/null
sql "$API_DATABASE_URL" "BEGIN; INSERT INTO custom_provider_secrets (provider_id, ingest_token_hash) SELECT id, b'role-verify' FROM custom_providers WHERE false; ROLLBACK" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT count(*) FROM subject_settings" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "SELECT count(*) FROM provider_connections" >/dev/null

sql "$API_DATABASE_URL" "BEGIN; INSERT INTO audit_events (id, occurred_at, actor_type, action, target_type, outcome, request_id, metadata) VALUES (gen_random_uuid(), now(), 'system', 'role.verify', 'database', 'succeeded', 'role-verify-api', '{}'::JSONB); ROLLBACK" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO audit_events (id, occurred_at, actor_type, action, target_type, outcome, request_id, metadata) VALUES (gen_random_uuid(), now(), 'system', 'role.verify', 'database', 'succeeded', 'role-verify-maintenance', '{}'::JSONB); ROLLBACK" >/dev/null
sql "$API_DATABASE_URL" "BEGIN; INSERT INTO provider_token_revocation_jobs (connection_id, provider_id, token_ciphertext) SELECT id, 'github', b'role-verify' FROM provider_connections WHERE false; ROLLBACK" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT id FROM environments WHERE false" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT id FROM activity_facts WHERE false" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT id FROM custom_providers WHERE false" >/dev/null
expect_denied "worker custom provider digest read" "$WORKER_DATABASE_URL" "SELECT ingest_token_hash FROM custom_provider_secrets WHERE false"
sql "$WORKER_DATABASE_URL" "BEGIN; UPDATE provider_connections SET sync_cursor = sync_cursor, status = status, last_synced_at = last_synced_at, last_error = last_error, updated_at = updated_at WHERE false; ROLLBACK" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT count(*) FROM provider_token_revocation_jobs" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT count(*) FROM magic_link_mail_outbox" >/dev/null
sql "$WORKER_DATABASE_URL" "UPDATE magic_link_mail_outbox SET updated_at = updated_at WHERE false" >/dev/null
sql "$WORKER_DATABASE_URL" "DELETE FROM provider_token_revocation_jobs WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "SELECT count(*) FROM provider_token_revocation_jobs" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "UPDATE provider_token_revocation_jobs SET token_key_id = token_key_id WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO maintenance_checkpoints (operation, scope, payload) VALUES ('retention', 'role-verify', '{}'::JSONB); UPDATE maintenance_checkpoints SET payload = '{}'::JSONB WHERE operation = 'retention' AND scope = 'role-verify'; ROLLBACK" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "SELECT count(*) FROM legal_holds" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO deletion_requests (request_id, target_type, target_id) VALUES ('role-verify', 'subject', 'role-verify'); UPDATE deletion_requests SET updated_at = updated_at WHERE request_id = 'role-verify'; ROLLBACK" >/dev/null
sql "$API_DATABASE_URL" "INSERT INTO deletion_request_inbox (request_id, target_type, target_id) VALUES ('role-verify-api', 'subject', 'role-verify-api') ON CONFLICT (request_id) DO NOTHING" >/dev/null
sql "$API_DATABASE_URL" "SELECT status FROM deletion_request_inbox WHERE request_id = 'role-verify-api'" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "UPDATE deletion_request_inbox SET status = 'promoted', promoted_at = now() WHERE request_id = 'role-verify-api'; DELETE FROM deletion_request_inbox WHERE request_id = 'role-verify-api'" >/dev/null
sql "$API_DATABASE_URL" "BEGIN; INSERT INTO magic_link_mail_outbox (recipient_email) SELECT email FROM magic_link_tokens WHERE false; UPDATE magic_link_mail_outbox SET updated_at = updated_at WHERE false; ROLLBACK" >/dev/null
sql "$API_DATABASE_URL" "SELECT email_hash FROM deleted_identity_tombstones WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO deleted_identity_tombstones (email_hash, deletion_request_id, expires_at) SELECT decode(repeat('00', 32), 'hex'), id, now() + INTERVAL '35 days' FROM deletion_requests WHERE false; ROLLBACK" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO provider_token_revocation_jobs (connection_id, provider_id, token_ciphertext) SELECT id, 'github', b'role-verify-maintenance' FROM provider_connections WHERE false; ROLLBACK" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "DELETE FROM users WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "DELETE FROM subjects WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "DELETE FROM environments WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "DELETE FROM deleted_identity_tombstones WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "SELECT count(*) FROM magic_link_mail_outbox" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "DELETE FROM magic_link_mail_outbox WHERE false" >/dev/null
sql "$API_DATABASE_URL" "BEGIN; INSERT INTO mutation_audit_outbox (audit_event_id, request_id, occurred_at, actor_type, action, target_type, outcome) VALUES (gen_random_uuid(), 'role-verify-api-outbox-success', now(), 'system', 'role.verify', 'database', 'succeeded'), (gen_random_uuid(), 'role-verify-api-outbox-failure', now(), 'system', 'role.verify', 'database', 'failed'), (gen_random_uuid(), 'role-verify-api-outbox-denial', now(), 'system', 'role.verify', 'database', 'denied'); ROLLBACK" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT count(*) FROM mutation_audit_outbox" >/dev/null
sql "$WORKER_DATABASE_URL" "UPDATE mutation_audit_outbox SET updated_at = updated_at WHERE false" >/dev/null
sql "$WORKER_DATABASE_URL" "BEGIN; INSERT INTO audit_events (id, occurred_at, actor_type, action, target_type, outcome, request_id, metadata) VALUES (gen_random_uuid(), now(), 'system', 'role.verify.dispatch', 'database', 'succeeded', 'role-verify-worker', '{}'::JSONB); ROLLBACK" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "SELECT count(*) FROM mutation_audit_outbox" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "DELETE FROM mutation_audit_outbox WHERE false" >/dev/null
sql "$API_DATABASE_URL" "SELECT identity_digest FROM deleted_identity_tombstones_v2 WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO deleted_identity_tombstones_v2 (identity_key_id, identity_digest, deletion_request_id, expires_at) SELECT 'role-verify', decode(repeat('00', 32), 'hex'), id, now() + INTERVAL '35 days' FROM deletion_requests WHERE false; UPDATE deleted_identity_tombstones_v2 SET expires_at = expires_at WHERE false; DELETE FROM deleted_identity_tombstones_v2 WHERE false; ROLLBACK" >/dev/null
sql "$API_DATABASE_URL" "SELECT connection_id FROM provider_connection_private_consents WHERE false" >/dev/null
sql "$API_DATABASE_URL" "BEGIN; INSERT INTO provider_connection_private_consents (connection_id, enabled) SELECT id, true FROM provider_connections WHERE false; ROLLBACK" >/dev/null
sql "$API_DATABASE_URL" "DELETE FROM provider_connection_private_consents WHERE false" >/dev/null
sql "$API_DATABASE_URL" "DELETE FROM custom_activity_events WHERE false" >/dev/null
sql "$WORKER_DATABASE_URL" "SELECT connection_id FROM provider_connection_private_consents WHERE false" >/dev/null
sql "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO deletion_request_claims (deletion_request_id) SELECT id FROM deletion_requests WHERE false; UPDATE deletion_request_claims SET updated_at = updated_at WHERE false; DELETE FROM deletion_request_claims WHERE false; ROLLBACK" >/dev/null

expect_denied "api audit read" "$API_DATABASE_URL" "SELECT count(*) FROM audit_events"
expect_denied "api magic token insert" "$API_DATABASE_URL" "INSERT INTO magic_link_tokens (email, token_hash, expires_at) SELECT email, b'forbidden', now() + INTERVAL '1 minute' FROM magic_link_tokens WHERE false"
expect_denied "api audit delete" "$API_DATABASE_URL" "DELETE FROM audit_events WHERE false"
expect_denied "api revocation queue read" "$API_DATABASE_URL" "SELECT count(*) FROM provider_token_revocation_jobs"
expect_denied "worker auth read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM user_sessions"
expect_denied "worker user read" "$WORKER_DATABASE_URL" "SELECT id FROM users WHERE false"
expect_denied "worker audit delete" "$WORKER_DATABASE_URL" "DELETE FROM audit_events WHERE false"
expect_denied "worker revocation queue insert" "$WORKER_DATABASE_URL" "INSERT INTO provider_token_revocation_jobs (connection_id, provider_id, token_ciphertext) SELECT id, 'github', b'forbidden' FROM provider_connections WHERE false"
expect_denied "worker provider connection insert" "$WORKER_DATABASE_URL" "INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status) SELECT gen_random_uuid(), subject_id, environment_id, 'none', 'active' FROM provider_connections WHERE false"
expect_denied "maintenance auth insert" "$MAINTENANCE_DATABASE_URL" "INSERT INTO user_sessions (id, user_id, session_token_hash, created_at, expires_at) SELECT gen_random_uuid(), user_id, b'forbidden', now(), now() + INTERVAL '1 minute' FROM user_sessions WHERE false"
expect_denied "maintenance audit update" "$MAINTENANCE_DATABASE_URL" "UPDATE audit_events SET outcome = 'failed' WHERE false"
expect_denied "maintenance revocation queue delete" "$MAINTENANCE_DATABASE_URL" "DELETE FROM provider_token_revocation_jobs WHERE false"
expect_denied "api maintenance checkpoint read" "$API_DATABASE_URL" "SELECT count(*) FROM maintenance_checkpoints"
expect_denied "api deletion request update" "$API_DATABASE_URL" "UPDATE deletion_requests SET updated_at = updated_at WHERE false"
expect_denied "api durable deletion request read" "$API_DATABASE_URL" "SELECT count(*) FROM deletion_requests"
expect_denied "api deletion inbox update" "$API_DATABASE_URL" "UPDATE deletion_request_inbox SET status = 'promoted', promoted_at = now() WHERE false"
expect_denied "api deletion inbox delete" "$API_DATABASE_URL" "DELETE FROM deletion_request_inbox WHERE false"
expect_denied "api identity tombstone insert" "$API_DATABASE_URL" "INSERT INTO deleted_identity_tombstones (email_hash, deletion_request_id, expires_at) SELECT decode(repeat('00', 32), 'hex'), id, now() + INTERVAL '35 days' FROM deletion_requests WHERE false"
expect_denied "api HMAC identity tombstone insert" "$API_DATABASE_URL" "INSERT INTO deleted_identity_tombstones_v2 (identity_key_id, identity_digest, deletion_request_id, expires_at) SELECT 'forbidden', decode(repeat('00', 32), 'hex'), id, now() + INTERVAL '35 days' FROM deletion_requests WHERE false"
expect_denied "api primary provider delete" "$API_DATABASE_URL" "DELETE FROM provider_connections WHERE false"
expect_denied "api primary subject delete" "$API_DATABASE_URL" "DELETE FROM subjects WHERE false"
expect_denied "api environment delete" "$API_DATABASE_URL" "DELETE FROM environments WHERE false"
expect_denied "api deletion claim read" "$API_DATABASE_URL" "SELECT count(*) FROM deletion_request_claims"
expect_denied "api deletion claim insert" "$API_DATABASE_URL" "INSERT INTO deletion_request_claims (deletion_request_id) SELECT id FROM deletion_requests WHERE false"
expect_denied "worker private consent mutate" "$WORKER_DATABASE_URL" "UPDATE provider_connection_private_consents SET updated_at = updated_at WHERE false"
expect_denied "worker deletion claim read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM deletion_request_claims"
expect_denied "worker deletion request read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM deletion_requests"
expect_denied "worker identity tombstone read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM deleted_identity_tombstones_v2"
expect_denied "worker magic mail insert" "$WORKER_DATABASE_URL" "INSERT INTO magic_link_mail_outbox (recipient_email) VALUES ('forbidden-worker@example.invalid')"
expect_denied "worker magic mail delete" "$WORKER_DATABASE_URL" "DELETE FROM magic_link_mail_outbox WHERE false"
expect_denied "worker magic token read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM magic_link_tokens"
expect_denied "worker magic token insert" "$WORKER_DATABASE_URL" "INSERT INTO magic_link_tokens (email, token_hash, expires_at) VALUES ('forbidden-worker@example.invalid', decode(repeat('00', 32), 'hex'), now() + INTERVAL '1 minute')"
expect_denied "worker magic token update" "$WORKER_DATABASE_URL" "UPDATE magic_link_tokens SET consumed_at = consumed_at WHERE false"
expect_denied "worker magic token delete" "$WORKER_DATABASE_URL" "DELETE FROM magic_link_tokens WHERE false"
expect_denied "maintenance magic mail insert" "$MAINTENANCE_DATABASE_URL" "INSERT INTO magic_link_mail_outbox (recipient_email) VALUES ('forbidden-maintenance@example.invalid')"
expect_denied "maintenance magic mail update" "$MAINTENANCE_DATABASE_URL" "UPDATE magic_link_mail_outbox SET updated_at = updated_at WHERE false"
expect_denied "worker deletion inbox read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM deletion_request_inbox"
expect_denied "maintenance legal hold delete" "$MAINTENANCE_DATABASE_URL" "DELETE FROM legal_holds WHERE false"
expect_denied "maintenance legal hold insert" "$MAINTENANCE_DATABASE_URL" "BEGIN; INSERT INTO legal_holds (target_type, target_id, approval_ref, reason, expires_at) VALUES ('subject', 'forbidden', 'forbidden', 'forbidden', now() + INTERVAL '1 hour'); ROLLBACK"
expect_denied "maintenance legal hold update" "$MAINTENANCE_DATABASE_URL" "UPDATE legal_holds SET expires_at = expires_at WHERE false"
expect_denied "api mutation audit outbox read" "$API_DATABASE_URL" "SELECT count(*) FROM mutation_audit_outbox"
expect_denied "api mutation audit outbox update" "$API_DATABASE_URL" "UPDATE mutation_audit_outbox SET updated_at = updated_at WHERE false"
expect_denied "api mutation audit outbox delete" "$API_DATABASE_URL" "DELETE FROM mutation_audit_outbox WHERE false"
expect_denied "worker mutation audit outbox insert" "$WORKER_DATABASE_URL" "INSERT INTO mutation_audit_outbox (audit_event_id, request_id, occurred_at, actor_type, action, target_type, outcome) VALUES (gen_random_uuid(), 'forbidden', now(), 'system', 'forbidden', 'database', 'succeeded')"
expect_denied "worker mutation audit outbox delete" "$WORKER_DATABASE_URL" "DELETE FROM mutation_audit_outbox WHERE false"
expect_denied "worker audit update" "$WORKER_DATABASE_URL" "UPDATE audit_events SET outcome = outcome WHERE false"
expect_denied "worker audit read" "$WORKER_DATABASE_URL" "SELECT count(*) FROM audit_events"
expect_denied "maintenance mutation audit outbox insert" "$MAINTENANCE_DATABASE_URL" "INSERT INTO mutation_audit_outbox (audit_event_id, request_id, occurred_at, actor_type, action, target_type, outcome) VALUES (gen_random_uuid(), 'forbidden', now(), 'system', 'forbidden', 'database', 'succeeded')"
expect_denied "maintenance mutation audit outbox update" "$MAINTENANCE_DATABASE_URL" "UPDATE mutation_audit_outbox SET updated_at = updated_at WHERE false"

schema_grants=$("$sql_bin" sql --url="$MIGRATION_DATABASE_URL" --format=tsv --set=errexit=true --execute="SHOW GRANTS ON SCHEMA public")
if printf '%s\n' "$schema_grants" | awk -F '\t' 'NR > 1 && ($3 == "jandibat_api" || $3 == "jandibat_worker" || $3 == "jandibat_maintenance" || $3 == "public") && ($4 == "CREATE" || $4 == "ALL") { found = 1 } END { exit !found }'; then
	echo "runtime or public role unexpectedly has schema CREATE privilege" >&2
	exit 1
fi
if ! printf '%s\n' "$schema_grants" | awk -F '\t' 'NR > 1 && $3 == "jandibat_migrator" && ($4 == "CREATE" || $4 == "ALL") { found = 1 } END { exit !found }'; then
	echo "migration role is missing schema CREATE privilege" >&2
	exit 1
fi

echo "runtime role positive and negative checks passed"
