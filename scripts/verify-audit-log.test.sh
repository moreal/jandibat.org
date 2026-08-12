#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/jandibat-audit-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
fake="$temporary/cockroach"

write_fake() {
	row=$1
	printf '%s\n' '#!/bin/sh' "printf '%b\\n' 'total_events\tinvalid_required_fields\torphan_intents\tduplicate_http_outcomes\toutcomes_without_intent\tinvalid_completed_deletions\tduplicate_deletion_outcomes\torphan_deletion_outcomes\tsecret_canary_matches\tinvalid_outbox_deliveries\tstale_outbox_jobs\tdead_outbox_jobs' '$row'" >"$fake"
	chmod 700 "$fake"
}

if AUDIT_DATABASE_URL=postgresql://fixture COCKROACH_SQL_BIN="$fake" sh "$root/scripts/verify-audit-log.sh" --window nope >/dev/null 2>&1; then
	echo "invalid duration unexpectedly accepted" >&2
	exit 1
fi
write_fake '3\t0\t0\t0\t0\t0\t0\t0\t0\t0\t0\t0'
output="$temporary/report.json"
AUDIT_DATABASE_URL=postgresql://fixture COCKROACH_SQL_BIN="$fake" sh "$root/scripts/verify-audit-log.sh" --window 24h --orphan-age 5m --output "$output" >/dev/null
grep -q '"result":"passed"' "$output"
write_fake '3\t0\t1\t0\t0\t0\t0\t0\t0\t0\t0\t0'
if AUDIT_DATABASE_URL=postgresql://fixture COCKROACH_SQL_BIN="$fake" sh "$root/scripts/verify-audit-log.sh" --window 24h >/dev/null 2>&1; then
	echo "orphan audit intent unexpectedly passed" >&2
	exit 1
fi
grep -q "event.metadata->>'backup_expiry_at' ~" "$root/scripts/verify-audit-log.sh"
if grep -q "authorization|cookie|session_token" "$root/scripts/verify-audit-log.sh"; then
	echo "secret scan must not reject redacted sensitive metadata keys" >&2
	exit 1
fi
grep -q "COALESCE(actor_id" "$root/scripts/verify-audit-log.sh"
grep -q "COALESCE(target_id" "$root/scripts/verify-audit-log.sh"
grep -q "invalid_outbox_deliveries" "$root/scripts/verify-audit-log.sh"
echo "audit verifier argument, artifact, robust-cast, and failure fixtures passed"
