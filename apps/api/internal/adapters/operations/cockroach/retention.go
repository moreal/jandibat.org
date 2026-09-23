package cockroach

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

var ErrInvalidPurgeRequest = errors.New("operations cockroach: invalid purge request")
var errRetentionPGXPoolRequired = errors.New("operations cockroach: retention requires a pgx pool")

type retentionSQLSpec struct {
	table                string
	keys                 []string
	timestamp            string
	extraFilter          string
	dateCutoff           bool
	subjectExpression    string
	accountExpression    string
	auditEventExpression string
}

var retentionSQLSpecs = map[operations.RetentionDataset]retentionSQLSpec{
	operations.RetentionAuditEvents: {table: "audit_events", keys: []string{"id"}, timestamp: "occurred_at", auditEventExpression: "candidate.id::STRING"},
	operations.RetentionActivityFacts: {
		table: "activity_facts", keys: []string{"id"}, timestamp: "activity_date", dateCutoff: true,
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionCustomActivities: {
		table: "custom_activity_events", keys: []string{"id"}, timestamp: "activity_date", dateCutoff: true,
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionRevokedProviderMetadata: {
		table: "provider_connections", keys: []string{"id"}, timestamp: "updated_at",
		extraFilter:       "candidate.status = 'revoked' AND NOT EXISTS (SELECT 1 FROM provider_token_revocation_jobs WHERE provider_token_revocation_jobs.connection_id = candidate.id)",
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionOrphanedProviderEnvironments: {
		table: "environments", keys: []string{"id"}, timestamp: "updated_at",
		extraFilter: "candidate.scope = 'subject' AND candidate.owner_subject_id IS NOT NULL " +
			"AND (candidate.id LIKE 'connection:%' OR candidate.id LIKE 'custom-provider:%') " +
			"AND NOT EXISTS (SELECT 1 FROM provider_connections WHERE provider_connections.environment_id = candidate.id) " +
			"AND NOT EXISTS (SELECT 1 FROM custom_providers WHERE custom_providers.environment_id = candidate.id) " +
			"AND NOT EXISTS (SELECT 1 FROM provider_sync_jobs WHERE provider_sync_jobs.environment_id = candidate.id) " +
			"AND NOT EXISTS (SELECT 1 FROM custom_activity_events WHERE custom_activity_events.environment_id = candidate.id) " +
			"AND NOT EXISTS (SELECT 1 FROM activity_facts WHERE activity_facts.environment_id = candidate.id) " +
			"AND NOT EXISTS (SELECT 1 FROM activity_refresh_cache WHERE activity_refresh_cache.environment_id = candidate.id)",
		subjectExpression: "candidate.owner_subject_id",
	},
	operations.RetentionSuccessfulSyncJobs: {
		table: "provider_sync_jobs", keys: []string{"id"}, timestamp: "finished_at",
		extraFilter:       "candidate.finished_at IS NOT NULL AND candidate.status = 'succeeded'",
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionFailedSyncJobs: {
		table: "provider_sync_jobs", keys: []string{"id"}, timestamp: "finished_at",
		extraFilter:       "candidate.finished_at IS NOT NULL AND candidate.status IN ('failed', 'cancelled')",
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionSessions: {
		table: "user_sessions", keys: []string{"id"}, timestamp: "LEAST(candidate.expires_at, COALESCE(candidate.revoked_at, candidate.expires_at))",
		accountExpression: "candidate.user_id",
	},
	operations.RetentionMagicLinks: {
		table: "magic_link_tokens", keys: []string{"id"}, timestamp: "expires_at",
		accountExpression: "COALESCE(candidate.user_id, (SELECT account.id FROM users AS account WHERE account.primary_email = candidate.email))",
	},
	operations.RetentionMagicLinkMailOutbox: {
		table: "magic_link_mail_outbox", keys: []string{"id"},
		timestamp:         "CASE WHEN candidate.status = 'sent' THEN candidate.updated_at ELSE candidate.terminal_at END",
		extraFilter:       "candidate.status IN ('sent', 'dead', 'superseded')",
		accountExpression: "(SELECT account.id FROM users AS account WHERE account.primary_email = candidate.recipient_email)",
	},
	operations.RetentionMutationAuditOutbox: {
		table: "mutation_audit_outbox", keys: []string{"id"},
		timestamp:            "COALESCE(candidate.delivered_at, candidate.terminal_at)",
		extraFilter:          "candidate.status IN ('delivered', 'dead')",
		auditEventExpression: "candidate.audit_event_id::STRING",
	},
	operations.RetentionAuthChallenges: {
		table: "auth_challenges", keys: []string{"id"}, timestamp: "expires_at", accountExpression: "candidate.user_id",
	},
	operations.RetentionIdempotencyKeys: {
		table: "ingest_idempotency_keys", keys: []string{"custom_provider_id", "key_hash"}, timestamp: "expires_at",
		subjectExpression: "(SELECT provider.subject_id FROM custom_providers AS provider WHERE provider.id = candidate.custom_provider_id)",
	},
	operations.RetentionTimelineCache: {
		table: "timeline_cache", keys: []string{"id"}, timestamp: "expires_at", extraFilter: "candidate.expires_at IS NOT NULL",
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionActivityRefresh: {
		table: "activity_refresh_cache", keys: []string{"subject_id", "environment_id", "activity_date"}, timestamp: "fetched_at",
		subjectExpression: "candidate.subject_id",
	},
	operations.RetentionRateLimitBuckets: {
		table: "api_rate_limit_buckets", keys: []string{"scope", "key_hash", "window_start"}, timestamp: "expires_at",
	},
	operations.RetentionDeletedIdentityTombstones: {
		table: "deleted_identity_tombstones", keys: []string{"email_hash"}, timestamp: "expires_at",
		accountExpression: "(SELECT request.target_id FROM deletion_requests AS request WHERE request.id = candidate.deletion_request_id AND request.target_type = 'account')",
	},
	operations.RetentionDeletedIdentityTombstonesV2: {
		table: "deleted_identity_tombstones_v2", keys: []string{"identity_key_id", "identity_digest"}, timestamp: "expires_at",
		accountExpression: "(SELECT request.target_id FROM deletion_requests AS request WHERE request.id = candidate.deletion_request_id AND request.target_type = 'account')",
	},
	operations.RetentionDeletionRequests: {
		table: "deletion_requests", keys: []string{"id"}, timestamp: "backup_expiry_at",
		extraFilter:       "candidate.status = 'completed' AND candidate.backup_expiry_at IS NOT NULL",
		subjectExpression: "CASE WHEN candidate.target_type = 'subject' THEN candidate.target_id ELSE NULL END",
		accountExpression: "CASE WHEN candidate.target_type = 'account' THEN candidate.target_id ELSE NULL END",
	},
	operations.RetentionDeletionRequestInbox: {
		table: "deletion_request_inbox", keys: []string{"id"}, timestamp: "promoted_at",
		extraFilter:       "candidate.status = 'promoted' AND candidate.promoted_at IS NOT NULL",
		subjectExpression: "CASE WHEN candidate.target_type = 'subject' THEN candidate.target_id ELSE NULL END",
		accountExpression: "CASE WHEN candidate.target_type = 'account' THEN candidate.target_id ELSE NULL END",
	},
}

func (store *Store) PurgeExpired(ctx context.Context, request operations.RetentionPurgeRequest) (int64, error) {
	if request.Before.IsZero() || request.Limit <= 0 {
		return 0, ErrInvalidPurgeRequest
	}
	asOf := request.AsOf.UTC()
	if asOf.IsZero() {
		asOf = request.Before.UTC()
	}
	query, err := buildRetentionPurgeQuery(request.Dataset)
	if err != nil {
		return 0, err
	}
	if store.pool == nil {
		return 0, errRetentionPGXPoolRequired
	}
	var deleted int64
	err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		result, err := tx.Exec(txctx, query, request.Before.UTC(), request.Limit, asOf)
		if err != nil {
			return fmt.Errorf("purge %s: %w", request.Dataset, err)
		}
		deleted = result.RowsAffected()
		return nil
	})
	return deleted, err
}

func (store *Store) CountExpired(ctx context.Context, request operations.RetentionPurgeRequest) (int64, error) {
	if request.Before.IsZero() || request.AsOf.IsZero() {
		return 0, ErrInvalidPurgeRequest
	}
	query, err := buildRetentionCountQuery(request.Dataset)
	if err != nil {
		return 0, err
	}
	if store.pool == nil {
		return 0, errRetentionPGXPoolRequired
	}
	var count int64
	if err := appdb.PGXExecutorFor(ctx, store.pool).QueryRow(ctx, query, request.Before.UTC(), request.AsOf.UTC()).Scan(&count); err != nil {
		return 0, fmt.Errorf("count expired %s: %w", request.Dataset, err)
	}
	return count, nil
}

func buildRetentionPurgeQuery(dataset operations.RetentionDataset) (string, error) {
	spec, ok := retentionSQLSpecs[dataset]
	if !ok {
		return "", fmt.Errorf("%w: unknown dataset %q", ErrInvalidPurgeRequest, dataset)
	}
	keys := strings.Join(spec.keys, ", ")
	candidateKeys := "candidate." + strings.Join(spec.keys, ", candidate.")
	filters := buildRetentionFilters(spec, "$1", "$3")
	orderTimestamp := retentionColumn(spec.timestamp)
	return fmt.Sprintf(`DELETE FROM %s
WHERE (%s) IN (
  SELECT %s FROM %s AS candidate
  WHERE %s
  ORDER BY %s, %s
  LIMIT $2
)`, spec.table, keys, candidateKeys, spec.table, filters, orderTimestamp, candidateKeys), nil
}

func buildRetentionCountQuery(dataset operations.RetentionDataset) (string, error) {
	spec, ok := retentionSQLSpecs[dataset]
	if !ok {
		return "", fmt.Errorf("%w: unknown dataset %q", ErrInvalidPurgeRequest, dataset)
	}
	return fmt.Sprintf(`SELECT count(*)
FROM %s AS candidate
WHERE %s`, spec.table, buildRetentionFilters(spec, "$1", "$2")), nil
}

func buildRetentionFilters(spec retentionSQLSpec, cutoffParameter, asOfParameter string) string {
	cutoff := cutoffParameter
	if spec.dateCutoff {
		cutoff += "::DATE"
	}
	filters := retentionColumn(spec.timestamp) + " < " + cutoff + " AND " + asOfParameter + "::TIMESTAMPTZ IS NOT NULL"
	if spec.extraFilter != "" {
		filters += " AND " + spec.extraFilter
	}
	if holdFilter := buildLegalHoldFilter(spec, asOfParameter); holdFilter != "" {
		filters += " AND NOT EXISTS (SELECT 1 FROM legal_holds AS hold WHERE hold.expires_at > " + asOfParameter + " AND (" + holdFilter + "))"
	}
	return filters
}

func retentionColumn(expression string) string {
	if strings.ContainsAny(expression, "( ") {
		return expression
	}
	return "candidate." + expression
}

func buildLegalHoldFilter(spec retentionSQLSpec, asOfParameter string) string {
	var targets []string
	if spec.subjectExpression != "" {
		targets = append(targets,
			"(hold.target_type = 'subject' AND hold.target_id = "+spec.subjectExpression+")",
			"(hold.target_type = 'account' AND EXISTS (SELECT 1 FROM subjects AS held_subject WHERE held_subject.id = "+spec.subjectExpression+" AND held_subject.owner_user_id = hold.target_id))",
		)
	}
	if spec.accountExpression != "" {
		targets = append(targets, "(hold.target_type = 'account' AND hold.target_id = "+spec.accountExpression+")")
	}
	if spec.auditEventExpression != "" {
		targets = append(targets,
			"(hold.target_type = 'audit_event' AND hold.target_id = "+spec.auditEventExpression+")",
			"(hold.target_type = 'subject' AND candidate.target_type = 'subject' AND hold.target_id = candidate.target_id)",
			"(hold.target_type = 'account' AND ((candidate.actor_type = 'user' AND hold.target_id = candidate.actor_id) OR (candidate.target_type IN ('account', 'user') AND hold.target_id = candidate.target_id)))",
		)
	}
	return strings.Join(targets, " OR ")
}
