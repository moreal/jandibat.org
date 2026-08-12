package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidRetentionConfig  = errors.New("operations: invalid retention configuration")
	ErrInvalidPurgeResult      = errors.New("operations: invalid retention purge result")
	ErrRetentionResumeMismatch = errors.New("operations: retention checkpoint does not match this run")
)

type RetentionDataset string

const (
	RetentionAuditEvents                  RetentionDataset = "audit_events"
	RetentionActivityFacts                RetentionDataset = "activity_facts"
	RetentionCustomActivities             RetentionDataset = "custom_activity_events"
	RetentionRevokedProviderMetadata      RetentionDataset = "revoked_provider_metadata"
	RetentionOrphanedProviderEnvironments RetentionDataset = "orphaned_provider_environments"
	RetentionSuccessfulSyncJobs           RetentionDataset = "provider_sync_jobs_succeeded"
	RetentionFailedSyncJobs               RetentionDataset = "provider_sync_jobs_failed_or_cancelled"
	RetentionSessions                     RetentionDataset = "user_sessions"
	RetentionMagicLinks                   RetentionDataset = "magic_link_tokens"
	RetentionMagicLinkMailOutbox          RetentionDataset = "magic_link_mail_outbox"
	RetentionMutationAuditOutbox          RetentionDataset = "mutation_audit_outbox"
	RetentionAuthChallenges               RetentionDataset = "auth_challenges"
	RetentionIdempotencyKeys              RetentionDataset = "ingest_idempotency_keys"
	RetentionTimelineCache                RetentionDataset = "timeline_cache"
	RetentionActivityRefresh              RetentionDataset = "activity_refresh_cache"
	RetentionRateLimitBuckets             RetentionDataset = "api_rate_limit_buckets"
	RetentionDeletedIdentityTombstones    RetentionDataset = "deleted_identity_tombstones"
	RetentionDeletedIdentityTombstonesV2  RetentionDataset = "deleted_identity_tombstones_v2"
	RetentionDeletionRequests             RetentionDataset = "deletion_requests"
	RetentionDeletionRequestInbox         RetentionDataset = "deletion_request_inbox"
)

type Clock interface {
	Now() time.Time
}

type RetentionRule struct {
	Dataset   RetentionDataset
	RetainFor time.Duration
}

type RetentionPurgeRequest struct {
	Dataset RetentionDataset
	Before  time.Time
	// AsOf is the captured run time used to evaluate legal-hold expiry. It is
	// independent from Before, which is the dataset-specific retention cutoff.
	AsOf  time.Time
	Limit int
}

type RetentionStore interface {
	PurgeExpired(context.Context, RetentionPurgeRequest) (int64, error)
}

type RetentionPreviewStore interface {
	CountExpired(context.Context, RetentionPurgeRequest) (int64, error)
}

type RetentionConfig struct {
	Rules                []RetentionRule
	BatchSize            int
	MaxBatchesPerDataset int
}

type RetentionResult struct {
	Deleted map[RetentionDataset]int64
	// Matched contains side-effect-free dry-run counts. Deleted remains zero
	// for a dry run so callers cannot accidentally report planned work as done.
	Matched   map[RetentionDataset]int64
	Failed    []RetentionDataset
	Truncated []RetentionDataset
	DryRun    bool
}

type RetentionOperatorOptions struct {
	AsOf   time.Time
	DryRun bool
	Resume bool
	Scope  string
}

type RetentionWorker struct {
	store  RetentionStore
	clock  Clock
	config RetentionConfig
}

func NewRetentionWorker(store RetentionStore, clock Clock, config RetentionConfig) (*RetentionWorker, error) {
	if store == nil || clock == nil || config.BatchSize <= 0 || config.MaxBatchesPerDataset <= 0 || len(config.Rules) == 0 {
		return nil, ErrInvalidRetentionConfig
	}
	seen := make(map[RetentionDataset]struct{}, len(config.Rules))
	rules := append([]RetentionRule(nil), config.Rules...)
	for _, rule := range rules {
		if !ValidRetentionDataset(rule.Dataset) || rule.RetainFor < 0 {
			return nil, fmt.Errorf("%w: invalid rule for %q", ErrInvalidRetentionConfig, rule.Dataset)
		}
		if _, duplicate := seen[rule.Dataset]; duplicate {
			return nil, fmt.Errorf("%w: duplicate dataset %q", ErrInvalidRetentionConfig, rule.Dataset)
		}
		seen[rule.Dataset] = struct{}{}
	}
	return &RetentionWorker{store: store, clock: clock, config: RetentionConfig{
		Rules: rules, BatchSize: config.BatchSize, MaxBatchesPerDataset: config.MaxBatchesPerDataset,
	}}, nil
}

// Run purges each dataset independently so one unavailable table does not
// block every other retention policy. Each rule uses the same captured UTC
// time, making a run deterministic across batches.
func (worker *RetentionWorker) Run(ctx context.Context) (RetentionResult, error) {
	result := RetentionResult{Deleted: make(map[RetentionDataset]int64), Matched: make(map[RetentionDataset]int64)}
	now := worker.clock.Now().UTC()
	var failures []error
	for _, rule := range worker.config.Rules {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		before := now.Add(-rule.RetainFor)
		completed := false
		for batch := 0; batch < worker.config.MaxBatchesPerDataset; batch++ {
			deleted, err := worker.store.PurgeExpired(ctx, RetentionPurgeRequest{
				Dataset: rule.Dataset, Before: before, AsOf: now, Limit: worker.config.BatchSize,
			})
			if err != nil {
				result.Failed = append(result.Failed, rule.Dataset)
				failures = append(failures, fmt.Errorf("purge %s: %w", rule.Dataset, err))
				completed = true
				break
			}
			if deleted < 0 || deleted > int64(worker.config.BatchSize) {
				result.Failed = append(result.Failed, rule.Dataset)
				failures = append(failures, fmt.Errorf("%w: %s deleted %d with limit %d", ErrInvalidPurgeResult, rule.Dataset, deleted, worker.config.BatchSize))
				completed = true
				break
			}
			result.Deleted[rule.Dataset] += deleted
			if deleted < int64(worker.config.BatchSize) {
				completed = true
				break
			}
		}
		if !completed {
			result.Truncated = append(result.Truncated, rule.Dataset)
		}
	}
	sort.Slice(result.Failed, func(i, j int) bool { return result.Failed[i] < result.Failed[j] })
	sort.Slice(result.Truncated, func(i, j int) bool { return result.Truncated[i] < result.Truncated[j] })
	return result, errors.Join(failures...)
}

type retentionCheckpointPayload struct {
	Version          int                        `json:"version"`
	AsOf             time.Time                  `json:"as_of"`
	RulesFingerprint string                     `json:"rules_fingerprint"`
	RuleIndex        int                        `json:"rule_index"`
	Deleted          map[RetentionDataset]int64 `json:"deleted"`
}

// RunOperator performs an explicit, deterministic retention run. Dry runs use
// exact COUNT queries and never write checkpoints. Execute runs persist
// progress after every committed batch; if a process stops after DELETE but
// before the checkpoint write, replay is still safe because the deletion
// predicate is idempotent.
func (worker *RetentionWorker) RunOperator(ctx context.Context, checkpoints MaintenanceCheckpointStore, options RetentionOperatorOptions) (RetentionResult, error) {
	result := RetentionResult{
		Deleted: make(map[RetentionDataset]int64), Matched: make(map[RetentionDataset]int64), DryRun: options.DryRun,
	}
	asOf := options.AsOf.UTC()
	if asOf.IsZero() {
		asOf = worker.clock.Now().UTC()
	}
	if options.DryRun {
		preview, ok := worker.store.(RetentionPreviewStore)
		if !ok {
			return result, fmt.Errorf("%w: store does not support dry-run counts", ErrInvalidRetentionConfig)
		}
		for _, rule := range worker.config.Rules {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			count, err := preview.CountExpired(ctx, RetentionPurgeRequest{
				Dataset: rule.Dataset, Before: asOf.Add(-rule.RetainFor), AsOf: asOf,
			})
			if err != nil {
				result.Failed = append(result.Failed, rule.Dataset)
				continue
			}
			if count < 0 {
				return result, fmt.Errorf("%w: %s matched %d", ErrInvalidPurgeResult, rule.Dataset, count)
			}
			result.Matched[rule.Dataset] = count
		}
		sort.Slice(result.Failed, func(i, j int) bool { return result.Failed[i] < result.Failed[j] })
		if len(result.Failed) != 0 {
			return result, fmt.Errorf("retention dry run failed for %s", joinRetentionDatasets(result.Failed))
		}
		return result, nil
	}

	scope := strings.TrimSpace(options.Scope)
	if checkpoints == nil || scope == "" || len(scope) > 255 {
		return result, fmt.Errorf("%w: execute requires a checkpoint store and scope", ErrInvalidRetentionConfig)
	}
	fingerprint, err := retentionRulesFingerprint(worker.config)
	if err != nil {
		return result, err
	}
	payload := retentionCheckpointPayload{
		Version: 1, AsOf: asOf, RulesFingerprint: fingerprint, Deleted: make(map[RetentionDataset]int64),
	}
	if options.Resume {
		checkpoint, found, loadErr := checkpoints.LoadCheckpoint(ctx, MaintenanceRetention, scope)
		if loadErr != nil {
			return result, fmt.Errorf("load retention checkpoint: %w", loadErr)
		}
		if found {
			if decodeErr := json.Unmarshal(checkpoint.Payload, &payload); decodeErr != nil {
				return result, fmt.Errorf("%w: decode payload: %v", ErrRetentionResumeMismatch, decodeErr)
			}
			if payload.Version != 1 || payload.RulesFingerprint != fingerprint || payload.RuleIndex < 0 ||
				payload.RuleIndex >= len(worker.config.Rules) || (!options.AsOf.IsZero() && !payload.AsOf.Equal(asOf)) {
				return result, ErrRetentionResumeMismatch
			}
			asOf = payload.AsOf.UTC()
			for dataset, count := range payload.Deleted {
				result.Deleted[dataset] = count
			}
		}
	}

	for ruleIndex := payload.RuleIndex; ruleIndex < len(worker.config.Rules); ruleIndex++ {
		rule := worker.config.Rules[ruleIndex]
		completed := false
		for batch := 0; batch < worker.config.MaxBatchesPerDataset; batch++ {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			deleted, purgeErr := worker.store.PurgeExpired(ctx, RetentionPurgeRequest{
				Dataset: rule.Dataset, Before: asOf.Add(-rule.RetainFor), AsOf: asOf, Limit: worker.config.BatchSize,
			})
			if purgeErr != nil {
				result.Failed = append(result.Failed, rule.Dataset)
				return result, fmt.Errorf("purge %s: %w", rule.Dataset, purgeErr)
			}
			if deleted < 0 || deleted > int64(worker.config.BatchSize) {
				result.Failed = append(result.Failed, rule.Dataset)
				return result, fmt.Errorf("%w: %s deleted %d with limit %d", ErrInvalidPurgeResult, rule.Dataset, deleted, worker.config.BatchSize)
			}
			result.Deleted[rule.Dataset] += deleted
			payload.RuleIndex = ruleIndex
			payload.Deleted = result.Deleted
			if deleted < int64(worker.config.BatchSize) {
				payload.RuleIndex = ruleIndex + 1
				completed = true
			}
			if payload.RuleIndex < len(worker.config.Rules) {
				if saveErr := saveRetentionCheckpoint(ctx, checkpoints, scope, payload, worker.clock.Now()); saveErr != nil {
					return result, saveErr
				}
			}
			if completed {
				break
			}
		}
		if !completed {
			result.Truncated = append(result.Truncated, rule.Dataset)
			return result, nil
		}
	}
	if err := checkpoints.DeleteCheckpoint(ctx, MaintenanceRetention, scope); err != nil {
		return result, fmt.Errorf("delete completed retention checkpoint: %w", err)
	}
	return result, nil
}

func retentionRulesFingerprint(config RetentionConfig) (string, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode retention rules: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func saveRetentionCheckpoint(ctx context.Context, store MaintenanceCheckpointStore, scope string, payload retentionCheckpointPayload, now time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode retention checkpoint: %w", err)
	}
	checkpoint := MaintenanceCheckpoint{
		Operation: MaintenanceRetention, Scope: scope, Payload: encoded, UpdatedAt: now.UTC(),
	}
	if err := store.SaveCheckpoint(ctx, checkpoint); err != nil {
		return fmt.Errorf("save retention checkpoint: %w", err)
	}
	return nil
}

func joinRetentionDatasets(datasets []RetentionDataset) string {
	names := make([]string, len(datasets))
	for index, dataset := range datasets {
		names[index] = string(dataset)
	}
	return strings.Join(names, ", ")
}

func ValidRetentionDataset(dataset RetentionDataset) bool {
	switch dataset {
	case RetentionAuditEvents, RetentionActivityFacts, RetentionCustomActivities,
		RetentionRevokedProviderMetadata, RetentionOrphanedProviderEnvironments,
		RetentionSuccessfulSyncJobs, RetentionFailedSyncJobs,
		RetentionSessions, RetentionMagicLinks, RetentionMagicLinkMailOutbox,
		RetentionMutationAuditOutbox,
		RetentionAuthChallenges, RetentionIdempotencyKeys, RetentionTimelineCache,
		RetentionActivityRefresh, RetentionRateLimitBuckets, RetentionDeletedIdentityTombstones,
		RetentionDeletedIdentityTombstonesV2, RetentionDeletionRequests:
		return true
	case RetentionDeletionRequestInbox:
		return true
	default:
		return false
	}
}
