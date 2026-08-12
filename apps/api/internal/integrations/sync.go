package integrations

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const syncIdempotencyRetention = 24 * time.Hour

const (
	syncJobClaimLease = 20 * time.Minute
	providerSyncLimit = 15 * time.Minute
)

type ManualSyncInput struct {
	ConnectionID   string
	IdempotencyKey string
	From           *activity.Date
	To             *activity.Date
	Force          bool
	FailurePolicy  activity.FetchFailurePolicy
}

type SyncRegistry struct {
	providers map[string]ProviderSyncer
}

func NewSyncRegistry(syncers ...ProviderSyncer) (*SyncRegistry, error) {
	registry := &SyncRegistry{providers: make(map[string]ProviderSyncer, len(syncers))}
	for _, syncer := range syncers {
		if syncer == nil || syncer.ProviderID() == "" {
			return nil, ErrInvalidProvider
		}
		if _, exists := registry.providers[syncer.ProviderID()]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateSyncer, syncer.ProviderID())
		}
		registry.providers[syncer.ProviderID()] = syncer
	}
	return registry, nil
}

func (registry *SyncRegistry) Lookup(providerID string) (ProviderSyncer, bool) {
	if registry == nil {
		return nil, false
	}
	syncer, ok := registry.providers[providerID]
	return syncer, ok
}

type SyncService struct {
	connections ConnectionStore
	jobs        SyncJobStore
	sink        ActivitySink
	cipher      SecretCipher
	registry    *SyncRegistry
	clock       Clock
	ids         IDGenerator
	retry       RetryPolicy
	executionMu sync.Mutex
	inFlight    map[string]struct{}
}

func NewSyncService(connections ConnectionStore, jobs SyncJobStore, sink ActivitySink, cipher SecretCipher, registry *SyncRegistry, clock Clock, ids IDGenerator) (*SyncService, error) {
	return NewSyncServiceWithRetryPolicy(connections, jobs, sink, cipher, registry, clock, ids, defaultRetryPolicy())
}

func NewSyncServiceWithRetryPolicy(connections ConnectionStore, jobs SyncJobStore, sink ActivitySink, cipher SecretCipher, registry *SyncRegistry, clock Clock, ids IDGenerator, retry RetryPolicy) (*SyncService, error) {
	if connections == nil || jobs == nil {
		return nil, ErrMissingStore
	}
	if sink == nil {
		return nil, ErrMissingActivitySink
	}
	if cipher == nil {
		return nil, ErrMissingCipher
	}
	if retry == nil {
		return nil, ErrInvalidRetryPolicy
	}
	if registry == nil {
		registry, _ = NewSyncRegistry()
	}
	return &SyncService{
		connections: connections,
		jobs:        jobs,
		sink:        sink,
		cipher:      cipher,
		registry:    registry,
		clock:       clockOrDefault(clock),
		ids:         idGeneratorOrDefault(ids),
		retry:       retry,
		inFlight:    make(map[string]struct{}),
	}, nil
}

func (service *SyncService) ManualSync(ctx context.Context, connectionID string) (SyncJob, error) {
	return service.sync(ctx, connectionID, SyncTriggerManual, SyncJob{})
}

// ManualSyncWithOptions performs a manual synchronization using the complete
// API request contract. Reusing an idempotency key with the same request
// returns the original job for at least 24 hours; a different request is a
// conflict. The plaintext key is never persisted.
func (service *SyncService) ManualSyncWithOptions(ctx context.Context, input ManualSyncInput) (SyncJob, error) {
	requested, err := service.manualSyncRequest(input)
	if err != nil {
		return SyncJob{}, err
	}
	return service.sync(ctx, input.ConnectionID, SyncTriggerManual, requested)
}

// EnqueueManualSync durably records a pending manual synchronization and
// returns without performing provider I/O. Scheduler.Run processes queued jobs.
func (service *SyncService) EnqueueManualSync(ctx context.Context, input ManualSyncInput) (SyncJob, error) {
	requested, err := service.manualSyncRequest(input)
	if err != nil {
		return SyncJob{}, err
	}
	_, release, err := service.acquireExecution(ctx, input.ConnectionID)
	if err != nil {
		return SyncJob{}, err
	}
	defer release()
	if previous, found, err := service.idempotentJob(ctx, input.ConnectionID, requested); err != nil || found {
		return previous, err
	}
	record, err := service.connections.GetConnection(ctx, input.ConnectionID)
	if err != nil {
		return SyncJob{}, err
	}
	if record.Connection.Status != ConnectionActive && record.Connection.Status != ConnectionError {
		return SyncJob{}, ErrConnectionNotSyncable
	}
	job, err := service.newSyncJob(input.ConnectionID, SyncTriggerManual, record.Connection.ConsecutiveFailures+1, requested)
	if err != nil {
		return SyncJob{}, err
	}
	if err := service.jobs.SaveSyncJob(ctx, job); err != nil {
		return SyncJob{}, err
	}
	return cloneSyncJob(job), nil
}

func (service *SyncService) ScheduledSync(ctx context.Context, connectionID string) (SyncJob, error) {
	return service.ScheduledSyncWithSettings(ctx, connectionID, SubjectSyncSettings{
		Enabled: true, FailurePolicy: activity.FetchFailureKeepStale,
	})
}

// ScheduledSyncWithSettings snapshots execution-affecting subject preferences
// onto the durable job so a queued/retried job keeps the policy it was created
// with. Enabled and Interval are scheduler concerns and are intentionally not
// interpreted here.
func (service *SyncService) ScheduledSyncWithSettings(ctx context.Context, connectionID string, settings SubjectSyncSettings) (SyncJob, error) {
	policy := normalizedFailurePolicy(settings.FailurePolicy)
	if policy != activity.FetchFailureKeepStale && policy != activity.FetchFailurePurge {
		return SyncJob{}, activity.ErrInvalidFetchFailurePolicy
	}
	if settings.Timezone != "" {
		if _, err := time.LoadLocation(settings.Timezone); err != nil {
			return SyncJob{}, fmt.Errorf("%w: %q", ErrInvalidSyncTimezone, settings.Timezone)
		}
	}
	return service.sync(ctx, connectionID, SyncTriggerScheduled, SyncJob{
		Timezone: settings.Timezone, FailurePolicy: policy,
	})
}

func (service *SyncService) GetJob(ctx context.Context, jobID string) (SyncJob, error) {
	return service.jobs.GetSyncJob(ctx, jobID)
}

func (service *SyncService) ListJobs(ctx context.Context, connectionID string) ([]SyncJob, error) {
	jobs, err := service.jobs.ListSyncJobs(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs, nil
}

// RunPending executes durable queued jobs. The connection execution lock and a
// status re-read make this safe when multiple scheduler replicas observe the
// same pending row.
func (service *SyncService) RunPending(ctx context.Context) ([]SyncJob, error) {
	if claims, ok := service.jobs.(SyncJobClaimStore); ok {
		ids, err := claims.ListClaimableSyncJobs(ctx, service.clock.Now(), 100)
		if err != nil {
			return nil, err
		}
		return service.runPendingIDs(ctx, ids)
	}
	jobs, err := service.jobs.ListSyncJobs(ctx, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.Status == SyncJobPending {
			ids = append(ids, job.ID)
		}
	}
	return service.runPendingIDs(ctx, ids)
}

func (service *SyncService) runPendingIDs(ctx context.Context, ids []string) ([]SyncJob, error) {
	completed := make([]SyncJob, 0, len(ids))
	var runErrors []error
	for _, id := range ids {
		completedJob, runErr := service.executePending(ctx, id)
		if runErr != nil && errors.Is(runErr, ErrSyncAlreadyRunning) {
			continue
		}
		if completedJob.ID != "" {
			completed = append(completed, completedJob)
		}
		if runErr != nil {
			runErrors = append(runErrors, runErr)
		}
	}
	return completed, errors.Join(runErrors...)
}

func (service *SyncService) executePending(ctx context.Context, jobID string) (SyncJob, error) {
	job, err := service.jobs.GetSyncJob(ctx, jobID)
	if err != nil {
		return SyncJob{}, err
	}
	executionClaim, release, err := service.acquireExecution(ctx, job.ConnectionID)
	if err != nil {
		return SyncJob{}, err
	}
	defer release()
	job, err = service.jobs.GetSyncJob(ctx, jobID)
	if err != nil {
		return SyncJob{}, err
	}
	_, usesClaims := service.jobs.(SyncJobClaimStore)
	if job.Status != SyncJobPending && !(usesClaims && job.Status == SyncJobRunning) {
		return cloneSyncJob(job), nil
	}
	record, syncErr := service.connections.GetConnection(ctx, job.ConnectionID)
	return service.executeAcquired(ctx, record, syncErr, job, executionClaim)
}

func (service *SyncService) sync(ctx context.Context, connectionID string, trigger SyncTrigger, requested SyncJob) (SyncJob, error) {
	if connectionID == "" {
		return SyncJob{}, ErrEmptyConnectionID
	}
	executionClaim, release, err := service.acquireExecution(ctx, connectionID)
	if err != nil {
		return SyncJob{}, err
	}
	defer release()
	if previous, found, err := service.idempotentJob(ctx, connectionID, requested); err != nil || found {
		return previous, err
	}

	record, syncErr := service.connections.GetConnection(ctx, connectionID)
	attempt := 1
	if syncErr == nil {
		attempt = record.Connection.ConsecutiveFailures + 1
	}
	job, err := service.newSyncJob(connectionID, trigger, attempt, requested)
	if err != nil {
		return SyncJob{}, err
	}
	if err := service.jobs.SaveSyncJob(ctx, job); err != nil {
		return SyncJob{}, err
	}
	return service.executeAcquired(ctx, record, syncErr, job, executionClaim)
}

func (service *SyncService) executeAcquired(ctx context.Context, record ConnectionRecord, syncErr error, job SyncJob, executionClaim string) (SyncJob, error) {
	attempt := job.Attempt
	started := service.clock.Now()
	job.Status = SyncJobRunning
	job.StartedAt = &started
	job.UpdatedAt = started
	if claims, ok := service.jobs.(SyncJobClaimStore); ok {
		claimToken, acquired, err := claims.ClaimSyncJob(ctx, job.ID, started, started.Add(syncJobClaimLease))
		if err != nil {
			return SyncJob{}, err
		}
		if !acquired {
			return SyncJob{}, ErrSyncAlreadyRunning
		}
		job.ClaimToken = claimToken
		leaseUntil := started.Add(syncJobClaimLease)
		job.ClaimLeaseUntil = &leaseUntil
	} else if err := service.jobs.SaveSyncJob(ctx, job); err != nil {
		return SyncJob{}, err
	}

	eligible := syncErr == nil && (record.Connection.Status == ConnectionActive || record.Connection.Status == ConnectionError)
	if syncErr == nil && !eligible {
		syncErr = ErrConnectionNotSyncable
	}
	if syncErr == nil {
		record.Connection.LastSyncAttempt = attempt
		record.Connection.LastSyncAttemptAt = &started
		record.Connection.NextSyncAttemptAt = nil
		record.Connection.UpdatedAt = started
		syncErr = service.saveConnectionState(ctx, record, executionClaim)
	}
	var result ProviderSyncResult
	if syncErr == nil {
		syncer, ok := service.registry.Lookup(record.Connection.ProviderID)
		if !ok {
			syncErr = fmt.Errorf("%w: %s", ErrProviderSyncerNotFound, record.Connection.ProviderID)
		} else {
			credentials := encryptedCredentialSource{cipher: service.cipher, credentials: record.Credentials}
			if !record.Connection.PrivateDataEnabled {
				// A credential can be retained for account linkage or a future
				// explicit opt-in, but public-only synchronization must never hand
				// it to a provider adapter that could return private activity.
				credentials.credentials = EncryptedCredentials{}
			}
			providerCtx, cancel := context.WithTimeout(ctx, providerSyncLimit)
			result, syncErr = syncer.Sync(providerCtx, ProviderSyncRequest{
				Connection:    cloneConnection(record.Connection),
				Credentials:   credentials,
				Timezone:      job.Timezone,
				From:          copyDatePointer(job.From),
				To:            copyDatePointer(job.To),
				Force:         job.Force,
				FailurePolicy: job.FailurePolicy,
			})
			cancel()
		}
	}
	if syncErr == nil {
		syncErr = validateSyncResult(record.Connection, result)
	}
	if syncErr == nil {
		environments := activity.SaveEnvironmentsInput{Environments: result.Environments}
		facts := activity.SaveFactsInput{
			Subject: activity.SubjectID(record.Connection.SubjectID),
			Facts:   result.Facts,
		}
		if fenced, ok := service.sink.(FencedActivitySink); ok {
			syncErr = fenced.SaveConnectionActivity(ctx, record.Connection.ID, executionClaim, environments, facts)
		} else {
			if len(result.Environments) > 0 {
				syncErr = service.sink.SaveEnvironments(ctx, environments)
			}
			if syncErr == nil && len(result.Facts) > 0 {
				syncErr = service.sink.SaveFacts(ctx, facts)
			}
		}
	}

	finished := service.clock.Now()
	job.FinishedAt = &finished
	job.UpdatedAt = finished
	if syncErr != nil {
		if eligible && job.FailurePolicy == activity.FetchFailurePurge {
			if purgeErr := service.purgeFailedRange(ctx, record.Connection, job); purgeErr != nil {
				syncErr = errors.Join(syncErr, purgeErr)
			}
		}
		job.Status = SyncJobFailed
		job.LastError = syncErr.Error()
		if eligible {
			record.Connection.Status = ConnectionError
			record.Connection.LastSyncAttempt = attempt
			record.Connection.ConsecutiveFailures = attempt
			record.Connection.LastError = syncErr.Error()
			record.Connection.UpdatedAt = finished
			if delay, retry := service.retry.NextRetry(attempt); retry {
				nextAttempt := finished.Add(delay)
				record.Connection.NextSyncAttemptAt = &nextAttempt
				job.NextAttemptAt = &nextAttempt
			} else {
				record.Connection.NextSyncAttemptAt = nil
			}
			if saveErr := service.saveConnectionState(ctx, record, executionClaim); saveErr != nil {
				syncErr = errors.Join(syncErr, saveErr)
			}
		}
	} else {
		job.Status = SyncJobSucceeded
		job.FactsWritten = len(result.Facts)
		record.Connection.Status = ConnectionActive
		record.Connection.LastError = ""
		record.Connection.LastSyncedAt = &finished
		record.Connection.LastSyncAttempt = attempt
		record.Connection.ConsecutiveFailures = 0
		record.Connection.NextSyncAttemptAt = nil
		record.Connection.UpdatedAt = finished
		if saveErr := service.saveConnectionState(ctx, record, executionClaim); saveErr != nil {
			syncErr = saveErr
			job.Status = SyncJobFailed
			job.LastError = saveErr.Error()
			if errors.Is(saveErr, ErrInvalidConnectionStatus) {
				if purgeErr := service.purgeConnectionFacts(ctx, record.Connection); purgeErr != nil {
					syncErr = errors.Join(syncErr, purgeErr)
				}
			}
		}
	}
	var saveErr error
	if claims, ok := service.jobs.(SyncJobClaimStore); ok && job.ClaimToken != "" {
		saveErr = claims.CompleteClaimedSyncJob(ctx, job, job.ClaimToken)
	} else {
		saveErr = service.jobs.SaveSyncJob(ctx, job)
	}
	if saveErr != nil {
		syncErr = errors.Join(syncErr, saveErr)
	}
	return cloneSyncJob(job), syncErr
}

func (service *SyncService) purgeConnectionFacts(ctx context.Context, connection ProviderConnection) error {
	sink, ok := service.sink.(activityReplacementSink)
	if !ok {
		return ErrPurgeUnsupported
	}
	return sink.ReplaceFacts(ctx, activity.LoadFactsInput{Subject: activity.SubjectID(connection.SubjectID)},
		[]activity.EnvironmentID{activity.EnvironmentID(connection.EnvironmentID)}, nil)
}

func (service *SyncService) saveConnectionState(ctx context.Context, record ConnectionRecord, executionClaim string) error {
	if store, ok := service.connections.(SyncConnectionStateStore); ok {
		return store.UpdateConnectionAfterSync(ctx, record, executionClaim)
	}
	return service.connections.SaveConnection(ctx, record)
}

func (service *SyncService) manualSyncRequest(input ManualSyncInput) (SyncJob, error) {
	if err := validateManualSyncInput(input); err != nil {
		return SyncJob{}, err
	}
	fingerprint, err := manualSyncFingerprint(input)
	if err != nil {
		return SyncJob{}, fmt.Errorf("fingerprint manual sync: %w", err)
	}
	keyHash := sha256.Sum256([]byte(input.IdempotencyKey))
	expires := service.clock.Now().Add(syncIdempotencyRetention)
	return SyncJob{
		From: input.From, To: input.To, Force: input.Force, FailurePolicy: normalizedFailurePolicy(input.FailurePolicy),
		IdempotencyKeyHash: keyHash[:], RequestHash: fingerprint[:], IdempotencyExpires: &expires,
	}, nil
}

func (service *SyncService) idempotentJob(ctx context.Context, connectionID string, requested SyncJob) (SyncJob, bool, error) {
	if len(requested.IdempotencyKeyHash) == 0 {
		return SyncJob{}, false, nil
	}
	idempotency, ok := service.jobs.(SyncIdempotencyStore)
	if !ok {
		return SyncJob{}, false, nil
	}
	previous, found, err := idempotency.GetSyncJobByIdempotencyKey(ctx, connectionID, requested.IdempotencyKeyHash, service.clock.Now())
	if err != nil || !found {
		return SyncJob{}, found, err
	}
	if subtle.ConstantTimeCompare(previous.RequestHash, requested.RequestHash) != 1 {
		return SyncJob{}, false, fmt.Errorf("%w: %w", ErrConflict, ErrIdempotencyConflict)
	}
	return previous, true, nil
}

func (service *SyncService) newSyncJob(connectionID string, trigger SyncTrigger, attempt int, requested SyncJob) (SyncJob, error) {
	jobID, err := service.ids.NewID()
	if err != nil {
		return SyncJob{}, fmt.Errorf("generate sync job id: %w", err)
	}
	now := service.clock.Now()
	return SyncJob{
		ID: jobID, ConnectionID: connectionID, Trigger: trigger, Status: SyncJobPending,
		Attempt: attempt, CreatedAt: now, UpdatedAt: now,
		From: copyDatePointer(requested.From), To: copyDatePointer(requested.To),
		Force: requested.Force, Timezone: requested.Timezone, FailurePolicy: normalizedFailurePolicy(requested.FailurePolicy),
		IdempotencyKeyHash: copyBytes(requested.IdempotencyKeyHash), RequestHash: copyBytes(requested.RequestHash),
		IdempotencyExpires: copyTimePointer(requested.IdempotencyExpires),
	}, nil
}

func validateSyncResult(connection ProviderConnection, result ProviderSyncResult) error {
	for index, fact := range result.Facts {
		if fact.Subject != "" && fact.Subject != activity.SubjectID(connection.SubjectID) {
			return fmt.Errorf("fact %d: subject does not match connection", index)
		}
	}
	return nil
}

type encryptedCredentialSource struct {
	cipher      SecretCipher
	credentials EncryptedCredentials
}

func (source encryptedCredentialSource) AccessToken(ctx context.Context) ([]byte, error) {
	if len(source.credentials.AccessToken) == 0 {
		return nil, nil
	}
	return source.cipher.Decrypt(ctx, source.credentials.AccessToken)
}

func (source encryptedCredentialSource) RefreshToken(ctx context.Context) ([]byte, error) {
	if len(source.credentials.RefreshToken) == 0 {
		return nil, nil
	}
	return source.cipher.Decrypt(ctx, source.credentials.RefreshToken)
}

func cloneSyncJob(job SyncJob) SyncJob {
	job.From = copyDatePointer(job.From)
	job.To = copyDatePointer(job.To)
	job.StartedAt = copyTimePointer(job.StartedAt)
	job.FinishedAt = copyTimePointer(job.FinishedAt)
	job.NextAttemptAt = copyTimePointer(job.NextAttemptAt)
	job.IdempotencyKeyHash = copyBytes(job.IdempotencyKeyHash)
	job.RequestHash = copyBytes(job.RequestHash)
	job.IdempotencyExpires = copyTimePointer(job.IdempotencyExpires)
	job.ClaimLeaseUntil = copyTimePointer(job.ClaimLeaseUntil)
	return job
}

type activityReplacementSink interface {
	ReplaceFacts(context.Context, activity.LoadFactsInput, []activity.EnvironmentID, []activity.Fact) error
}

func (service *SyncService) purgeFailedRange(ctx context.Context, connection ProviderConnection, job SyncJob) error {
	sink, ok := service.sink.(activityReplacementSink)
	if !ok {
		return ErrPurgeUnsupported
	}
	return sink.ReplaceFacts(ctx, activity.LoadFactsInput{
		Subject: activity.SubjectID(connection.SubjectID), From: copyDatePointer(job.From), To: copyDatePointer(job.To),
	}, []activity.EnvironmentID{activity.EnvironmentID(connection.EnvironmentID)}, nil)
}

func validateManualSyncInput(input ManualSyncInput) error {
	if input.ConnectionID == "" {
		return ErrEmptyConnectionID
	}
	length := utf8.RuneCountInString(input.IdempotencyKey)
	if length < minIdempotencyKeyLength || length > maxIdempotencyKeyLength {
		return ErrInvalidIdempotencyKey
	}
	if err := validateSyncDate(input.From); err != nil {
		return err
	}
	if err := validateSyncDate(input.To); err != nil {
		return err
	}
	if input.From != nil && input.To != nil && *input.From > *input.To {
		return ErrInvalidSyncDateRange
	}
	policy := normalizedFailurePolicy(input.FailurePolicy)
	if policy != activity.FetchFailureKeepStale && policy != activity.FetchFailurePurge {
		return activity.ErrInvalidFetchFailurePolicy
	}
	return nil
}

func validateSyncDate(date *activity.Date) error {
	if date == nil {
		return nil
	}
	parsed, err := time.Parse(time.DateOnly, string(*date))
	if err != nil || parsed.Format(time.DateOnly) != string(*date) {
		return ErrInvalidSyncDate
	}
	return nil
}

func normalizedFailurePolicy(policy activity.FetchFailurePolicy) activity.FetchFailurePolicy {
	if policy == "" {
		return activity.FetchFailureKeepStale
	}
	return policy
}

func manualSyncFingerprint(input ManualSyncInput) ([sha256.Size]byte, error) {
	payload, err := json.Marshal(struct {
		ConnectionID  string                      `json:"connectionId"`
		From          *activity.Date              `json:"from,omitempty"`
		To            *activity.Date              `json:"to,omitempty"`
		Force         bool                        `json:"force"`
		FailurePolicy activity.FetchFailurePolicy `json:"failurePolicy"`
	}{input.ConnectionID, input.From, input.To, input.Force, normalizedFailurePolicy(input.FailurePolicy)})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func copyDatePointer(value *activity.Date) *activity.Date {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (service *SyncService) acquireExecution(ctx context.Context, connectionID string) (string, func(), error) {
	if executionStore, ok := service.connections.(SyncExecutionStore); ok {
		claimToken, acquired, err := executionStore.TryAcquireSyncExecution(ctx, connectionID)
		if err != nil {
			return "", nil, err
		}
		if !acquired {
			return "", nil, ErrSyncAlreadyRunning
		}
		return claimToken, func() {
			_ = executionStore.ReleaseSyncExecution(context.WithoutCancel(ctx), connectionID, claimToken)
		}, nil
	}

	service.executionMu.Lock()
	if _, running := service.inFlight[connectionID]; running {
		service.executionMu.Unlock()
		return "", nil, ErrSyncAlreadyRunning
	}
	service.inFlight[connectionID] = struct{}{}
	service.executionMu.Unlock()
	return "", func() {
		service.executionMu.Lock()
		delete(service.inFlight, connectionID)
		service.executionMu.Unlock()
	}, nil
}
