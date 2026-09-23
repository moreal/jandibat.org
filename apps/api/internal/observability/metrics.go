// Package observability provides the process-local metrics needed to verify
// the service SLOs. It deliberately implements the small Prometheus text
// surface used by jandibat instead of coupling domain code to a metrics SDK.
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var durationBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// Resource contains the bounded deployment identity attached to every sample.
type Resource struct {
	BuildSHA    string
	Environment string
	Region      string
}

// ResourceFromEnvironment obtains resource identity from conventional
// deployment variables. The configured application environment wins over
// APP_ENV; an absent application environment follows Config's development
// default while absent build and region values remain explicitly unknown.
func ResourceFromEnvironment(environment string) Resource {
	return Resource{
		BuildSHA:    firstValue(os.Getenv("BUILD_SHA"), os.Getenv("GIT_SHA"), "unknown"),
		Environment: firstValue(environment, os.Getenv("APP_ENV"), "development"),
		Region:      firstValue(os.Getenv("REGION"), os.Getenv("FLY_REGION"), os.Getenv("AWS_REGION"), "unknown"),
	}
}

type httpKey struct {
	route, method, statusClass string
}

type durationKey struct {
	route, method string
}

type providerKey struct {
	provider, outcome string
}

type syncJobKey struct {
	provider, status, trigger string
}

type auditKey struct {
	action, outcome string
}

type retentionKey struct {
	table, outcome string
}

type credentialDecryptKey struct {
	keyID, outcome string
}

type histogram struct {
	count   uint64
	sum     float64
	buckets [len(durationBuckets)]uint64
}

type freshnessKey struct {
	provider, visibility string
}

type freshnessGroup struct {
	total     uint64
	within30m uint64
	within2h  uint64
	oldest    float64
}

// QueueAgeProbe reports the age in seconds of the oldest ready sync job.
type QueueAgeProbe func(context.Context) (float64, error)

// DeletionAgeProbe reports the age in seconds of the oldest unfinished
// deletion request.
type DeletionAgeProbe func(context.Context) (float64, error)

// FreshnessSample is one bounded provider/visibility activity freshness gauge.
type FreshnessSample struct {
	Provider   string
	Visibility string
	Seconds    float64
}

// ActivityFreshnessProbe reports time since the newest fact ingestion for
// each provider and subject visibility class.
type ActivityFreshnessProbe func(context.Context) ([]FreshnessSample, error)

// RevocationDLQProbe reports the current dead-letter count and oldest age.
type RevocationDLQProbe func(context.Context) (count, oldestSeconds float64, err error)

// Registry is safe for concurrent request recording and Prometheus scrapes.
type Registry struct {
	mu sync.RWMutex

	resource          Resource
	http              map[httpKey]uint64
	duration          map[durationKey]*histogram
	provider          map[providerKey]uint64
	providerDuration  map[string]*histogram
	syncJobs          map[syncJobKey]uint64
	syncJobStartDelay *histogram
	magicLinkDelivery map[string]uint64
	customIngest      map[string]uint64
	audit             map[auditKey]uint64
	auditDelay        *histogram
	retention         map[retentionKey]uint64
	credentialDecrypt map[credentialDecryptKey]uint64
	cockroachErrors   map[string]uint64
	dbPoolWait        *histogram

	pgxPool          *pgxpool.Pool
	queueProbe       QueueAgeProbe
	deletionProbe    DeletionAgeProbe
	freshnessProbe   ActivityFreshnessProbe
	revocationProbe  RevocationDLQProbe
	collectionErrors map[string]uint64
}

// NewRegistry constructs an isolated registry, primarily for process startup
// and tests. Resource values are normalized so labels are never omitted.
func NewRegistry(resource Resource) *Registry {
	return &Registry{
		resource:          normalizeResource(resource),
		http:              make(map[httpKey]uint64),
		duration:          make(map[durationKey]*histogram),
		provider:          make(map[providerKey]uint64),
		providerDuration:  make(map[string]*histogram),
		syncJobs:          make(map[syncJobKey]uint64),
		syncJobStartDelay: &histogram{},
		magicLinkDelivery: make(map[string]uint64),
		customIngest:      make(map[string]uint64),
		audit:             make(map[auditKey]uint64),
		auditDelay:        &histogram{},
		retention:         make(map[retentionKey]uint64),
		credentialDecrypt: make(map[credentialDecryptKey]uint64),
		cockroachErrors:   make(map[string]uint64),
		dbPoolWait:        &histogram{},
		collectionErrors:  make(map[string]uint64),
	}
}

var defaultRegistry = NewRegistry(ResourceFromEnvironment(""))

// Default returns the registry shared by the current executable.
func Default() *Registry { return defaultRegistry }

// SetResource configures the deployment identity used by subsequent scrapes.
func (registry *Registry) SetResource(resource Resource) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.resource = normalizeResource(resource)
	registry.mu.Unlock()
}

// ObserveHTTP records one routed server request. Unknown methods and routes
// collapse into bounded sentinel values rather than accepting attacker input.
func (registry *Registry) ObserveHTTP(route, method string, status int, elapsed time.Duration) {
	if registry == nil {
		return
	}
	key := httpKey{route: boundedRoute(route), method: boundedMethod(method), statusClass: statusClass(status)}
	duration := durationKey{route: key.route, method: key.method}
	registry.mu.Lock()
	registry.http[key]++
	observeHistogram(registry.duration, duration, elapsed.Seconds())
	registry.mu.Unlock()
}

// ObserveProvider records a request to a bounded built-in/custom provider
// class. Provider IDs not in the built-in set collapse to "custom".
func (registry *Registry) ObserveProvider(provider, outcome string, elapsed time.Duration) {
	if registry == nil {
		return
	}
	provider = boundedProvider(provider)
	outcome = boundedProviderOutcome(outcome)
	registry.mu.Lock()
	registry.provider[providerKey{provider: provider, outcome: outcome}]++
	observeStringHistogram(registry.providerDuration, provider, elapsed.Seconds())
	registry.mu.Unlock()
}

// ObserveSyncJob records a terminal or queued sync job transition.
func (registry *Registry) ObserveSyncJob(provider, status, trigger string) {
	if registry == nil {
		return
	}
	key := syncJobKey{
		provider: boundedProvider(provider),
		status:   normalizedSyncStatus(status),
		trigger:  boundedValue(trigger, []string{"manual", "scheduled"}, "unknown"),
	}
	registry.mu.Lock()
	registry.syncJobs[key]++
	registry.mu.Unlock()
}

// ObserveSyncJobStartDelay records the durable enqueue-to-start delay for a
// job that reached execution.
func (registry *Registry) ObserveSyncJobStartDelay(elapsed time.Duration) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	observeHistogramValue(registry.syncJobStartDelay, max(0, elapsed.Seconds()))
	registry.mu.Unlock()
}

// ObserveMagicLinkDelivery records only bounded queue outcomes. Recipient,
// token, provider response, and claim identifiers are intentionally excluded.
func (registry *Registry) ObserveMagicLinkDelivery(outcome string) {
	if registry == nil {
		return
	}
	outcome = boundedValue(outcome, []string{"sent", "retry", "dead", "superseded"}, "unknown")
	registry.mu.Lock()
	registry.magicLinkDelivery[outcome]++
	registry.mu.Unlock()
}

// ObserveCustomIngest adds event outcomes (rather than request outcomes) so
// accepted+duplicate+rejected can be reconciled with the input batch size.
func (registry *Registry) ObserveCustomIngest(outcome string, count uint64) {
	if registry == nil || count == 0 {
		return
	}
	outcome = boundedValue(outcome, []string{"accepted", "duplicate", "rejected"}, "unknown")
	registry.mu.Lock()
	registry.customIngest[outcome] += count
	registry.mu.Unlock()
}

// ObserveAudit records whether an audit event was durably persisted and its
// occurrence-to-persistence delay.
func (registry *Registry) ObserveAudit(action, outcome string, delay time.Duration) {
	if registry == nil {
		return
	}
	if strings.TrimSpace(action) == "" {
		action = "unknown"
	}
	outcome = boundedValue(outcome, []string{"succeeded", "failed"}, "unknown")
	registry.mu.Lock()
	registry.audit[auditKey{action: action, outcome: outcome}]++
	observeHistogramValue(registry.auditDelay, max(0, delay.Seconds()))
	registry.mu.Unlock()
}

// ObserveRetention adds a bounded row count for a maintenance dataset.
func (registry *Registry) ObserveRetention(table, outcome string, rows uint64) {
	if registry == nil || rows == 0 {
		return
	}
	if strings.TrimSpace(table) == "" {
		table = "unknown"
	}
	outcome = boundedValue(outcome, []string{"succeeded", "failed"}, "unknown")
	registry.mu.Lock()
	registry.retention[retentionKey{table: table, outcome: outcome}] += rows
	registry.mu.Unlock()
}

// observeCredentialDecrypt records use of one configured credential key.
// The caller has already collapsed unconfigured/corrupt identifiers so a
// persisted attacker-controlled envelope cannot create metric cardinality.
func (registry *Registry) observeCredentialDecrypt(keyID, outcome string) {
	if registry == nil {
		return
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		keyID = "unknown"
	}
	outcome = boundedValue(outcome, []string{"succeeded", "failed"}, "failed")
	registry.mu.Lock()
	registry.credentialDecrypt[credentialDecryptKey{keyID: keyID, outcome: outcome}]++
	registry.mu.Unlock()
}

// ObserveCockroachTransactionError records a bounded pgx transport outcome.
func (registry *Registry) ObserveCockroachTransactionError(outcome string) {
	if registry == nil {
		return
	}
	outcome = boundedValue(outcome, []string{"retry_required", "sql_error", "driver_error"}, "driver_error")
	registry.mu.Lock()
	registry.cockroachErrors[outcome]++
	registry.mu.Unlock()
}

// ObserveDBPoolWait records the complete pgxpool acquisition latency. Pool
// acquisition is the only connection checkout queue used by OpenDatabase, so
// this histogram includes the wait that the database SLO is intended to bound.
func (registry *Registry) ObserveDBPoolWait(elapsed time.Duration) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	observeHistogramValue(registry.dbPoolWait, max(0, elapsed.Seconds()))
	registry.mu.Unlock()
}

// RegisterPGXPool exposes the number of currently acquired pgx connections.
// The existing acquisition tracer separately records checkout wait time.
func (registry *Registry) RegisterPGXPool(pool *pgxpool.Pool) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.pgxPool = pool
	registry.mu.Unlock()
}

// RegisterQueueAgeProbe exposes the worker queue age without making metric
// collection depend on a concrete database implementation.
func (registry *Registry) RegisterQueueAgeProbe(probe QueueAgeProbe) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.queueProbe = probe
	registry.mu.Unlock()
}

// RegisterDeletionAgeProbe exposes unfinished deletion request age.
func (registry *Registry) RegisterDeletionAgeProbe(probe DeletionAgeProbe) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.deletionProbe = probe
	registry.mu.Unlock()
}

// RegisterActivityFreshnessProbe exposes provider/visibility freshness.
func (registry *Registry) RegisterActivityFreshnessProbe(probe ActivityFreshnessProbe) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.freshnessProbe = probe
	registry.mu.Unlock()
}

// RegisterRevocationDLQProbe exposes durable OAuth revocation dead letters.
func (registry *Registry) RegisterRevocationDLQProbe(probe RevocationDLQProbe) {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	registry.revocationProbe = probe
	registry.mu.Unlock()
}

const sqlQueueAgeQuery = `
SELECT COALESCE(EXTRACT(EPOCH FROM (current_timestamp - MIN(available_at))), 0)
FROM provider_sync_jobs
WHERE status = 'queued' AND available_at <= current_timestamp`

// PGXQueueAgeProbe reports the oldest ready sync job using the runtime pool.
func PGXQueueAgeProbe(pool *pgxpool.Pool) QueueAgeProbe {
	if pool == nil {
		return nil
	}
	return func(ctx context.Context) (float64, error) {
		var seconds float64
		if err := pool.QueryRow(ctx, sqlQueueAgeQuery).Scan(&seconds); err != nil {
			return 0, err
		}
		return max(0, seconds), nil
	}
}

const sqlDeletionAgeQuery = `
SELECT COALESCE(EXTRACT(EPOCH FROM (current_timestamp - MIN(requested_at))), 0)
FROM (
  SELECT requested_at
  FROM deletion_request_inbox
  WHERE status = 'requested'
  UNION ALL
  SELECT requested_at
  FROM deletion_requests
  WHERE status <> 'completed'
) AS unfinished_deletion_requests`

// PGXDeletionAgeProbe counts requested inbox entries and noncompleted requests.
func PGXDeletionAgeProbe(pool *pgxpool.Pool) DeletionAgeProbe {
	if pool == nil {
		return nil
	}
	return func(ctx context.Context) (float64, error) {
		var seconds float64
		if err := pool.QueryRow(ctx, sqlDeletionAgeQuery).Scan(&seconds); err != nil {
			return 0, err
		}
		return max(0, seconds), nil
	}
}

const sqlActivityFreshnessQuery = `
WITH subject_provider_freshness AS (
  SELECT
    facts.subject_id,
    CASE
      WHEN facts.custom_provider_id IS NOT NULL THEN 'custom'
      ELSE COALESCE(NULLIF(connections.sync_cursor->>'provider_id', ''), environments.key, 'unknown')
    END AS provider,
    CASE
      WHEN NOT subjects.is_public OR COALESCE(environments.metadata->>'visibility', '') = 'private' THEN 'private'
      ELSE 'public'
    END AS visibility,
    MAX(facts.ingested_at) AS last_ingested_at
  FROM activity_facts AS facts
  JOIN subjects ON subjects.id = facts.subject_id
  JOIN environments ON environments.id = facts.environment_id
  LEFT JOIN provider_connections AS connections ON connections.id = facts.provider_connection_id
  GROUP BY facts.subject_id, provider, visibility
)
SELECT provider, visibility,
  COALESCE(EXTRACT(EPOCH FROM (current_timestamp - last_ingested_at)), 0)
FROM subject_provider_freshness`

// PGXActivityFreshnessProbe groups fact ingestion by bounded provider and visibility.
func PGXActivityFreshnessProbe(pool *pgxpool.Pool) ActivityFreshnessProbe {
	if pool == nil {
		return nil
	}
	return func(ctx context.Context) ([]FreshnessSample, error) {
		rows, err := pool.Query(ctx, sqlActivityFreshnessQuery)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var samples []FreshnessSample
		for rows.Next() {
			var sample FreshnessSample
			if err := rows.Scan(&sample.Provider, &sample.Visibility, &sample.Seconds); err != nil {
				return nil, err
			}
			sample.Provider = boundedProvider(sample.Provider)
			sample.Visibility = boundedValue(sample.Visibility, []string{"public", "private"}, "unknown")
			sample.Seconds = max(0, sample.Seconds)
			samples = append(samples, sample)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return samples, nil
	}
}

const sqlRevocationDLQQuery = `
SELECT
  count(*)::FLOAT8,
  COALESCE(EXTRACT(EPOCH FROM (current_timestamp - MIN(updated_at))), 0)
FROM provider_token_revocation_jobs
WHERE status = 'dead'`

// PGXRevocationDLQProbe observes durable dead letters without changing jobs.
func PGXRevocationDLQProbe(pool *pgxpool.Pool) RevocationDLQProbe {
	if pool == nil {
		return nil
	}
	return func(ctx context.Context) (float64, float64, error) {
		var count, age float64
		if err := pool.QueryRow(ctx, sqlRevocationDLQQuery).Scan(&count, &age); err != nil {
			return 0, 0, err
		}
		return max(0, count), max(0, age), nil
	}
}

// Handler serves the Prometheus 0.0.4 exposition format.
func (registry *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := registry.writePrometheus(r.Context(), w); err != nil {
			http.Error(w, "metrics collection failed", http.StatusInternalServerError)
		}
	})
}

func (registry *Registry) writePrometheus(ctx context.Context, output io.Writer) error {
	if registry == nil {
		return errors.New("nil metrics registry")
	}
	registry.mu.RLock()
	resource := registry.resource
	httpCounts := cloneMap(registry.http)
	durations := cloneHistograms(registry.duration)
	providerCounts := cloneMap(registry.provider)
	providerDurations := cloneStringHistograms(registry.providerDuration)
	syncJobs := cloneMap(registry.syncJobs)
	syncJobStartDelay := *registry.syncJobStartDelay
	magicLinkDelivery := cloneMap(registry.magicLinkDelivery)
	customIngest := cloneMap(registry.customIngest)
	auditCounts := cloneMap(registry.audit)
	auditDelay := *registry.auditDelay
	retention := cloneMap(registry.retention)
	credentialDecrypt := cloneMap(registry.credentialDecrypt)
	cockroachErrors := cloneMap(registry.cockroachErrors)
	dbPoolWait := *registry.dbPoolWait
	pgxPool := registry.pgxPool
	queueProbe := registry.queueProbe
	deletionProbe := registry.deletionProbe
	freshnessProbe := registry.freshnessProbe
	revocationProbe := registry.revocationProbe
	collectionErrors := cloneMap(registry.collectionErrors)
	registry.mu.RUnlock()

	queueAge := math.NaN()
	if queueProbe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var err error
		queueAge, err = queueProbe(probeCtx)
		cancel()
		if err != nil {
			registry.mu.Lock()
			registry.collectionErrors["sync_queue"]++
			collectionErrors["sync_queue"] = registry.collectionErrors["sync_queue"]
			registry.mu.Unlock()
			queueAge = math.NaN()
		}
	}
	deletionAge := math.NaN()
	if deletionProbe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var err error
		deletionAge, err = deletionProbe(probeCtx)
		cancel()
		if err != nil {
			registry.mu.Lock()
			registry.collectionErrors["deletion_requests"]++
			collectionErrors["deletion_requests"] = registry.collectionErrors["deletion_requests"]
			registry.mu.Unlock()
			deletionAge = math.NaN()
		}
	}
	var freshness []FreshnessSample
	if freshnessProbe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var err error
		freshness, err = freshnessProbe(probeCtx)
		cancel()
		if err != nil {
			registry.mu.Lock()
			registry.collectionErrors["activity_freshness"]++
			collectionErrors["activity_freshness"] = registry.collectionErrors["activity_freshness"]
			registry.mu.Unlock()
			freshness = nil
		}
	}
	revocationDead, revocationDeadAge := math.NaN(), math.NaN()
	if revocationProbe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var err error
		revocationDead, revocationDeadAge, err = revocationProbe(probeCtx)
		cancel()
		if err != nil {
			registry.mu.Lock()
			registry.collectionErrors["provider_revocation_dlq"]++
			collectionErrors["provider_revocation_dlq"] = registry.collectionErrors["provider_revocation_dlq"]
			registry.mu.Unlock()
			revocationDead, revocationDeadAge = math.NaN(), math.NaN()
		}
	}

	writer := &promWriter{output: output, resource: resource}
	writer.family("http_server_requests_total", "Total HTTP requests received by the API.", "counter")
	for _, key := range sortedHTTPKeys(httpCounts) {
		writer.sample("http_server_requests_total", labels{{"route", key.route}, {"method", key.method}, {"status_class", key.statusClass}}, float64(httpCounts[key]))
	}
	writer.family("http_server_request_duration_seconds", "HTTP handler duration in seconds.", "histogram")
	for _, key := range sortedDurationKeys(durations) {
		writer.histogram("http_server_request_duration_seconds", labels{{"route", key.route}, {"method", key.method}}, durations[key])
	}
	writer.family("provider_requests_total", "Total outbound provider requests.", "counter")
	for _, key := range sortedProviderKeys(providerCounts) {
		writer.sample("provider_requests_total", labels{{"provider", key.provider}, {"outcome", key.outcome}}, float64(providerCounts[key]))
	}
	writer.family("provider_request_duration_seconds", "Outbound provider request duration in seconds.", "histogram")
	for _, provider := range sortedStringKeys(providerDurations) {
		writer.histogram("provider_request_duration_seconds", labels{{"provider", provider}}, providerDurations[provider])
	}
	writer.family("sync_jobs_total", "Total observed sync jobs.", "counter")
	for _, key := range sortedSyncJobKeys(syncJobs) {
		writer.sample("sync_jobs_total", labels{{"provider", key.provider}, {"status", key.status}, {"trigger", key.trigger}}, float64(syncJobs[key]))
	}
	writer.family("sync_job_start_delay_seconds", "Delay from durable sync job creation to execution start in seconds.", "histogram")
	writer.histogram("sync_job_start_delay_seconds", nil, syncJobStartDelay)
	writer.family("magic_link_mail_delivery_total", "Magic Link mail delivery outcomes from the durable worker.", "counter")
	for _, outcome := range sortedStringKeys(magicLinkDelivery) {
		writer.sample("magic_link_mail_delivery_total", labels{{"outcome", outcome}}, float64(magicLinkDelivery[outcome]))
	}
	writer.family("custom_ingest_events_total", "Total custom ingest events by outcome.", "counter")
	for _, outcome := range sortedStringKeys(customIngest) {
		writer.sample("custom_ingest_events_total", labels{{"outcome", outcome}}, float64(customIngest[outcome]))
	}
	writer.family("audit_events_total", "Total audit persistence attempts.", "counter")
	for _, key := range sortedAuditKeys(auditCounts) {
		writer.sample("audit_events_total", labels{{"action", key.action}, {"outcome", key.outcome}}, float64(auditCounts[key]))
	}
	writer.family("audit_persist_delay_seconds", "Delay from audit occurrence to persistence in seconds.", "histogram")
	writer.histogram("audit_persist_delay_seconds", nil, auditDelay)
	writer.family("retention_rows_total", "Total rows removed by retention processing.", "counter")
	for _, key := range sortedRetentionKeys(retention) {
		writer.sample("retention_rows_total", labels{{"table", key.table}, {"outcome", key.outcome}}, float64(retention[key]))
	}
	writer.family("credential_decrypt_operations_total", "Credential decrypt operations by configured key and outcome.", "counter")
	for _, key := range sortedCredentialDecryptKeys(credentialDecrypt) {
		writer.sample("credential_decrypt_operations_total", labels{{"key_id", key.keyID}, {"outcome", key.outcome}}, float64(credentialDecrypt[key]))
	}
	writer.family("cockroach_transaction_errors_total", "CockroachDB transaction and query errors observed at the pgx boundary.", "counter")
	for _, outcome := range sortedStringKeys(cockroachErrors) {
		writer.sample("cockroach_transaction_errors_total", labels{{"outcome", outcome}}, float64(cockroachErrors[outcome]))
	}

	if pgxPool != nil {
		stats := pgxPool.Stat()
		writer.family("db_pool_in_use", "Database connections currently in use.", "gauge")
		writer.sample("db_pool_in_use", nil, float64(stats.AcquiredConns()))
	}
	writer.family("db_pool_wait_seconds", "Cumulative time spent waiting to acquire a database connection.", "counter")
	writer.sample("db_pool_wait_seconds", nil, dbPoolWait.sum)
	writer.family("db_pool_wait_duration_seconds", "Database connection acquisition duration in seconds.", "histogram")
	writer.histogram("db_pool_wait_duration_seconds", nil, dbPoolWait)
	if queueProbe != nil {
		writer.family("sync_queue_oldest_ready_seconds", "Age of the oldest ready sync job in seconds.", "gauge")
		writer.sample("sync_queue_oldest_ready_seconds", nil, queueAge)
	}
	if deletionProbe != nil {
		writer.family("deletion_request_age_seconds", "Age of the oldest unfinished deletion request in seconds.", "gauge")
		writer.sample("deletion_request_age_seconds", nil, deletionAge)
	}
	if freshnessProbe != nil {
		groups := groupFreshness(freshness)
		writer.family("activity_freshness_seconds", "Oldest subject activity freshness in seconds.", "gauge")
		writer.family("activity_freshness_subjects", "Subjects meeting instantaneous activity freshness thresholds.", "gauge")
		for _, key := range sortedFreshnessKeys(groups) {
			metricLabels := labels{{"provider", key.provider}, {"visibility", key.visibility}}
			writer.sample("activity_freshness_seconds", metricLabels, groups[key].oldest)
			writer.sample("activity_freshness_subjects", append(append(labels(nil), metricLabels...), label{"threshold", "le_30m"}), float64(groups[key].within30m))
			writer.sample("activity_freshness_subjects", append(append(labels(nil), metricLabels...), label{"threshold", "le_2h"}), float64(groups[key].within2h))
			writer.sample("activity_freshness_subjects", append(append(labels(nil), metricLabels...), label{"threshold", "total"}), float64(groups[key].total))
		}
	}
	if revocationProbe != nil {
		writer.family("provider_token_revocation_dead_jobs", "Current OAuth token revocation dead-letter jobs.", "gauge")
		writer.sample("provider_token_revocation_dead_jobs", nil, revocationDead)
		writer.family("provider_token_revocation_oldest_dead_seconds", "Age of the oldest OAuth token revocation dead-letter job.", "gauge")
		writer.sample("provider_token_revocation_oldest_dead_seconds", nil, revocationDeadAge)
	}
	writer.family("observability_capability", "Whether an SLO metric capability is implemented by this process.", "gauge")
	writer.sample("observability_capability", labels{{"capability", "db_pool_wait_p99"}, {"state", "implemented"}}, 1)
	writer.family("observability_collection_errors_total", "Metric collection errors by bounded collector.", "counter")
	for _, collector := range sortedStringKeys(collectionErrors) {
		writer.sample("observability_collection_errors_total", labels{{"collector", collector}}, float64(collectionErrors[collector]))
	}
	return writer.err
}

// InstrumentRoundTripper measures outbound provider traffic while preserving
// the fixed-target transport wrapper protocol used by the SSRF guard.
func InstrumentRoundTripper(next http.RoundTripper, registry *Registry) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &providerRoundTripper{next: next, registry: registry}
}

type providerRoundTripper struct {
	next     http.RoundTripper
	registry *Registry
}

func (transport *providerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	started := time.Now()
	response, err := transport.next.RoundTrip(request)
	outcome := "dependency_error"
	if err == nil {
		outcome = statusClass(response.StatusCode)
	} else if errors.Is(err, context.DeadlineExceeded) {
		outcome = "timeout"
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		outcome = "timeout"
	}
	transport.registry.ObserveProvider(providerFromRequest(request), outcome, time.Since(started))
	return response, err
}

func (transport *providerRoundTripper) ProviderBaseTransport() http.RoundTripper {
	return transport.next
}

func (transport *providerRoundTripper) WrapProviderTransport(next http.RoundTripper) http.RoundTripper {
	return &providerRoundTripper{next: next, registry: transport.registry}
}

type label struct{ name, value string }
type labels []label

type promWriter struct {
	output   io.Writer
	resource Resource
	err      error
}

func (writer *promWriter) family(name, help, metricType string) {
	writer.line("# HELP " + name + " " + help + "\n")
	writer.line("# TYPE " + name + " " + metricType + "\n")
}

func (writer *promWriter) histogram(name string, metricLabels labels, value histogram) {
	for index, boundary := range durationBuckets {
		bucketLabels := append(append(labels(nil), metricLabels...), label{"le", strconv.FormatFloat(boundary, 'g', -1, 64)})
		writer.sample(name+"_bucket", bucketLabels, float64(value.buckets[index]))
	}
	writer.sample(name+"_bucket", append(append(labels(nil), metricLabels...), label{"le", "+Inf"}), float64(value.count))
	writer.sample(name+"_sum", metricLabels, value.sum)
	writer.sample(name+"_count", metricLabels, float64(value.count))
}

func (writer *promWriter) sample(name string, metricLabels labels, value float64) {
	all := append(append(labels(nil), metricLabels...),
		label{"build_sha", writer.resource.BuildSHA},
		label{"environment", writer.resource.Environment},
		label{"region", writer.resource.Region},
	)
	parts := make([]string, len(all))
	for index, item := range all {
		parts[index] = item.name + "=\"" + escapeLabel(item.value) + "\""
	}
	formatted := strconv.FormatFloat(value, 'g', -1, 64)
	if math.IsNaN(value) {
		formatted = "NaN"
	}
	writer.line(name + "{" + strings.Join(parts, ",") + "} " + formatted + "\n")
}

func (writer *promWriter) line(value string) {
	if writer.err != nil {
		return
	}
	_, writer.err = io.WriteString(writer.output, value)
}

func observeHistogram[K comparable](values map[K]*histogram, key K, seconds float64) {
	item := values[key]
	if item == nil {
		item = &histogram{}
		values[key] = item
	}
	observeHistogramValue(item, max(0, seconds))
}

func observeStringHistogram(values map[string]*histogram, key string, seconds float64) {
	observeHistogram(values, key, seconds)
}

func observeHistogramValue(item *histogram, seconds float64) {
	item.count++
	item.sum += seconds
	for index, boundary := range durationBuckets {
		if seconds <= boundary {
			item.buckets[index]++
		}
	}
}

func normalizeResource(resource Resource) Resource {
	return Resource{
		BuildSHA:    firstValue(strings.TrimSpace(resource.BuildSHA), "unknown"),
		Environment: firstValue(strings.TrimSpace(resource.Environment), "unknown"),
		Region:      firstValue(strings.TrimSpace(resource.Region), "unknown"),
	}
}

func firstValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "unknown"
}

func boundedRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" {
		return "unmatched"
	}
	if len(route) > 256 || !strings.HasPrefix(route, "/") {
		return "unmatched"
	}
	return route
}

func boundedMethod(method string) string {
	return boundedValue(strings.ToUpper(strings.TrimSpace(method)), []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace,
	}, "OTHER")
}

func boundedProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return "github"
	case "gitlab":
		return "gitlab"
	case "codeberg":
		return "codeberg"
	case "", "unknown":
		return "unknown"
	default:
		return "custom"
	}
}

func boundedProviderOutcome(outcome string) string {
	return boundedValue(outcome, []string{"2xx", "3xx", "4xx", "5xx", "timeout", "dependency_error"}, "dependency_error")
}

func normalizedSyncStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "queued":
		return "queued"
	case "running", "succeeded", "failed", "cancelled":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "unknown"
	}
}

func groupFreshness(samples []FreshnessSample) map[freshnessKey]freshnessGroup {
	result := make(map[freshnessKey]freshnessGroup)
	for _, sample := range samples {
		key := freshnessKey{
			provider:   boundedProvider(sample.Provider),
			visibility: boundedValue(sample.Visibility, []string{"public", "private"}, "unknown"),
		}
		seconds := max(0, sample.Seconds)
		value := result[key]
		value.total++
		value.oldest = max(value.oldest, seconds)
		if seconds <= 30*time.Minute.Seconds() {
			value.within30m++
		}
		if seconds <= 2*time.Hour.Seconds() {
			value.within2h++
		}
		result[key] = value
	}
	return result
}

func sortedFreshnessKeys(values map[freshnessKey]freshnessGroup) []freshnessKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].provider+keys[i].visibility < keys[j].provider+keys[j].visibility
	})
	return keys
}

func boundedValue(value string, allowed []string, fallback string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "unknown"
	}
	return strconv.Itoa(status/100) + "xx"
}

func providerFromRequest(request *http.Request) string {
	if request == nil || request.URL == nil {
		return "unknown"
	}
	host := strings.ToLower(request.URL.Hostname())
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return "github"
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return "gitlab"
	case host == "codeberg.org" || strings.HasSuffix(host, ".codeberg.org"):
		return "codeberg"
	default:
		return "custom"
	}
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func cloneMap[K comparable](source map[K]uint64) map[K]uint64 {
	result := make(map[K]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneHistograms[K comparable](source map[K]*histogram) map[K]histogram {
	result := make(map[K]histogram, len(source))
	for key, value := range source {
		result[key] = *value
	}
	return result
}

func cloneStringHistograms(source map[string]*histogram) map[string]histogram {
	return cloneHistograms(source)
}

func sortedHTTPKeys(values map[httpKey]uint64) []httpKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i].route, keys[i].method, keys[i].statusClass) < fmt.Sprint(keys[j].route, keys[j].method, keys[j].statusClass)
	})
	return keys
}

func sortedDurationKeys(values map[durationKey]histogram) []durationKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool { return keys[i].route+keys[i].method < keys[j].route+keys[j].method })
	return keys
}

func sortedProviderKeys(values map[providerKey]uint64) []providerKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool { return keys[i].provider+keys[i].outcome < keys[j].provider+keys[j].outcome })
	return keys
}

func sortedSyncJobKeys(values map[syncJobKey]uint64) []syncJobKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].provider+keys[i].status+keys[i].trigger < keys[j].provider+keys[j].status+keys[j].trigger
	})
	return keys
}

func sortedAuditKeys(values map[auditKey]uint64) []auditKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool { return keys[i].action+keys[i].outcome < keys[j].action+keys[j].outcome })
	return keys
}

func sortedRetentionKeys(values map[retentionKey]uint64) []retentionKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool { return keys[i].table+keys[i].outcome < keys[j].table+keys[j].outcome })
	return keys
}

func sortedCredentialDecryptKeys(values map[credentialDecryptKey]uint64) []credentialDecryptKey {
	keys := mapKeys(values)
	sort.Slice(keys, func(i, j int) bool { return keys[i].keyID+keys[i].outcome < keys[j].keyID+keys[j].outcome })
	return keys
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := mapKeys(values)
	sort.Strings(keys)
	return keys
}

func mapKeys[K comparable, V any](values map[K]V) []K {
	keys := make([]K, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
