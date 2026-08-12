#!/bin/sh
set -eu

database_url=${AUDIT_DATABASE_URL:-${MAINTENANCE_DATABASE_URL:-}}
: "${database_url:?AUDIT_DATABASE_URL or MAINTENANCE_DATABASE_URL is required}"
sql_bin=${COCKROACH_SQL_BIN:-cockroach}
window=24h
orphan_age=5m
output=
sql_output=
cleanup() {
	if [ -n "$sql_output" ] && [ -f "$sql_output" ]; then rm -f "$sql_output"; fi
}
trap cleanup EXIT HUP INT TERM

while [ "$#" -gt 0 ]; do
	case "$1" in
		--window) [ "$#" -ge 2 ] || { echo "--window requires a value" >&2; exit 2; }; window=$2; shift 2 ;;
		--orphan-age) [ "$#" -ge 2 ] || { echo "--orphan-age requires a value" >&2; exit 2; }; orphan_age=$2; shift 2 ;;
		--output) [ "$#" -ge 2 ] || { echo "--output requires a value" >&2; exit 2; }; output=$2; shift 2 ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done

duration_seconds() {
	value=$1
	case "$value" in
		*[!0-9smhd]*|'') return 1 ;;
	esac
	number=${value%?}
	unit=${value#"$number"}
	case "$number" in ''|*[!0-9]*) return 1 ;; esac
	[ "$number" -gt 0 ] || return 1
	case "$unit" in s) factor=1 ;; m) factor=60 ;; h) factor=3600 ;; d) factor=86400 ;; *) return 1 ;; esac
	echo $((number * factor))
}

window_seconds=$(duration_seconds "$window") || { echo "--window must be a positive duration such as 24h" >&2; exit 2; }
orphan_seconds=$(duration_seconds "$orphan_age") || { echo "--orphan-age must be a positive duration such as 5m" >&2; exit 2; }
if ! command -v "$sql_bin" >/dev/null 2>&1; then echo "Cockroach SQL client not found: $sql_bin" >&2; exit 127; fi

query="
WITH bounded AS (
  SELECT * FROM audit_events
  WHERE occurred_at >= now() - $window_seconds * INTERVAL '1 second'
), metrics AS (
  SELECT
    (SELECT count(*) FROM bounded) AS total_events,
    (SELECT count(*) FROM bounded WHERE request_id = '' OR action = '' OR target_type = '' OR actor_type = '') AS invalid_required_fields,
    (SELECT count(*) FROM bounded AS intent
      WHERE intent.action = 'http.mutation.intent'
        AND intent.metadata->>'phase' = 'intent'
        AND intent.occurred_at < now() - $orphan_seconds * INTERVAL '1 second'
        AND NOT EXISTS (
          SELECT 1 FROM audit_events AS outcome
          WHERE outcome.request_id = intent.request_id AND outcome.metadata->>'phase' = 'outcome'
            AND outcome.occurred_at >= intent.occurred_at
            AND outcome.occurred_at <= intent.occurred_at + $orphan_seconds * INTERVAL '1 second'
        )) AS orphan_intents,
    (SELECT count(*) FROM (
      SELECT DISTINCT candidate.request_id FROM bounded AS candidate
      WHERE candidate.metadata->>'phase' IN ('intent', 'outcome')
        AND (
          (SELECT count(*) FROM audit_events AS intent
            WHERE intent.request_id = candidate.request_id
              AND intent.action = 'http.mutation.intent'
              AND intent.metadata->>'phase' = 'intent') <> 1
          OR
          (SELECT count(*) FROM audit_events AS outcome
            WHERE outcome.request_id = candidate.request_id
              AND outcome.metadata->>'phase' = 'outcome') <> 1
        )
    )) AS duplicate_http_outcomes,
    (SELECT count(*) FROM bounded AS outcome
      WHERE outcome.metadata->>'phase' = 'outcome'
        AND NOT EXISTS (
          SELECT 1 FROM audit_events AS intent
          WHERE intent.request_id = outcome.request_id
            AND intent.action = 'http.mutation.intent' AND intent.metadata->>'phase' = 'intent'
            AND intent.occurred_at <= outcome.occurred_at
            AND outcome.occurred_at <= intent.occurred_at + $orphan_seconds * INTERVAL '1 second'
        )) AS outcomes_without_intent,
    (SELECT count(*) FROM deletion_requests AS request
      LEFT JOIN audit_events AS event ON event.id = request.audit_event_id
      WHERE request.status = 'completed' AND (
        event.id IS NULL OR event.action <> 'deletion.completed' OR event.outcome <> 'succeeded'
        OR event.request_id <> request.request_id
        OR CASE
          WHEN event.metadata->>'backup_expiry_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$'
          THEN (event.metadata->>'backup_expiry_at')::TIMESTAMPTZ
          ELSE NULL
        END IS DISTINCT FROM request.backup_expiry_at
      )) AS invalid_completed_deletions,
    (SELECT count(*) FROM (
      SELECT request_id FROM audit_events WHERE action = 'deletion.completed' AND outcome = 'succeeded'
      GROUP BY request_id HAVING count(*) <> 1
    )) AS duplicate_deletion_outcomes,
    (SELECT count(*) FROM audit_events AS event
      WHERE event.action = 'deletion.completed' AND event.outcome = 'succeeded'
        AND NOT EXISTS (
          SELECT 1 FROM deletion_requests AS request
          WHERE request.audit_event_id = event.id AND request.request_id = event.request_id AND request.status = 'completed'
        )) AS orphan_deletion_outcomes,
    (SELECT count(*) FROM bounded WHERE concat_ws(' ', metadata::STRING, COALESCE(actor_id, ''), COALESCE(target_id, ''), action, request_id) ~* '(BEGIN [A-Z ]+ PRIVATE KEY|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|[?&](access_token|refresh_token|code|token)=[^&\"[:space:]]{8,})') AS secret_canary_matches,
    (SELECT count(*) FROM mutation_audit_outbox AS outbox
      LEFT JOIN audit_events AS event ON event.id = outbox.audit_event_id
      WHERE outbox.status = 'delivered' AND (
        event.id IS NULL OR event.request_id <> outbox.request_id OR event.occurred_at <> outbox.occurred_at
        OR event.actor_type <> outbox.actor_type OR event.actor_id IS DISTINCT FROM outbox.actor_id
        OR event.action <> outbox.action OR event.target_type <> outbox.target_type
        OR event.target_id IS DISTINCT FROM outbox.target_id OR event.outcome <> outbox.outcome
        OR event.metadata IS DISTINCT FROM outbox.metadata)) AS invalid_outbox_deliveries,
    (SELECT count(*) FROM mutation_audit_outbox
      WHERE (status = 'pending' AND created_at < now() - $orphan_seconds * INTERVAL '1 second')
         OR (status = 'processing' AND lease_until < now())) AS stale_outbox_jobs,
    (SELECT count(*) FROM mutation_audit_outbox WHERE status = 'dead') AS dead_outbox_jobs
)
SELECT total_events, invalid_required_fields, orphan_intents, duplicate_http_outcomes,
       outcomes_without_intent, invalid_completed_deletions, duplicate_deletion_outcomes,
       orphan_deletion_outcomes, secret_canary_matches, invalid_outbox_deliveries,
       stale_outbox_jobs, dead_outbox_jobs
FROM metrics"

sql_output=$(mktemp "${TMPDIR:-/tmp}/jandibat-audit-query.XXXXXX")
chmod 600 "$sql_output"
if ! "$sql_bin" sql --url="$database_url" --format=tsv --set=errexit=true --execute="$query" >"$sql_output"; then
	echo "audit reconciliation query failed" >&2
	exit 1
fi
row=$(tail -n 1 "$sql_output" | tr -d '\r')
old_ifs=$IFS; IFS="	"; set -- $row; IFS=$old_ifs
[ "$#" -eq 12 ] || { echo "unexpected audit verification result" >&2; exit 1; }
total=$1; invalid=$2; orphan_intents=$3; duplicate_http=$4; outcomes_without=$5; invalid_deletions=$6; duplicate_deletions=$7; orphan_deletions=$8; secret_matches=$9; invalid_outbox=${10}; stale_outbox=${11}; dead_outbox=${12}
failures=$((invalid + orphan_intents + duplicate_http + outcomes_without + invalid_deletions + duplicate_deletions + orphan_deletions + secret_matches + invalid_outbox + stale_outbox + dead_outbox))
result=passed; [ "$failures" -eq 0 ] || result=failed
report=$(printf '{"schemaVersion":2,"checkedAt":"%s","window":"%s","orphanAge":"%s","result":"%s","metrics":{"totalEvents":%s,"invalidRequiredFields":%s,"orphanIntents":%s,"duplicateHttpOutcomes":%s,"outcomesWithoutIntent":%s,"invalidCompletedDeletions":%s,"duplicateDeletionOutcomes":%s,"orphanDeletionOutcomes":%s,"secretCanaryMatches":%s,"invalidOutboxDeliveries":%s,"staleOutboxJobs":%s,"deadOutboxJobs":%s}}' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$window" "$orphan_age" "$result" "$total" "$invalid" "$orphan_intents" "$duplicate_http" "$outcomes_without" "$invalid_deletions" "$duplicate_deletions" "$orphan_deletions" "$secret_matches" "$invalid_outbox" "$stale_outbox" "$dead_outbox")
if [ -n "$output" ]; then umask 077; printf '%s\n' "$report" >"$output"; fi
printf '%s\n' "$report"
[ "$failures" -eq 0 ] || exit 1
