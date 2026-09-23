package integrations

import (
	"context"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID() (string, error)
}

// SecretCipher is the sole path from plaintext credentials to persisted
// credentials. Implementations should provide authenticated encryption.
type SecretCipher interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// OAuthTokenRevoker performs a provider-side best-effort revocation of an
// OAuth access token. Implementations must not retain token after the call.
// ConnectionService deliberately treats failures as advisory because local
// credential destruction is the authoritative disconnect operation.
type OAuthTokenRevoker interface {
	RevokeOAuthToken(ctx context.Context, providerID string, token []byte) error
}

type ConnectionStore interface {
	SaveConnection(ctx context.Context, record ConnectionRecord) error
	GetConnection(ctx context.Context, id string) (ConnectionRecord, error)
	ListConnections(ctx context.Context, subjectID string) ([]ConnectionRecord, error)
	// PurgeConnectionData immediately removes facts and sync jobs owned by a
	// revoked connection while retaining its credential-free tombstone for the
	// bounded operational retention window.
	PurgeConnectionData(ctx context.Context, id string) error
}

// AtomicConnectionRevocationStore is the production disconnect boundary. One
// transaction makes the connection locally unusable, clears its credential
// columns, purges owned facts/jobs, and durably enqueues any encrypted OAuth
// token needed by the worker for provider-side revocation.
type AtomicConnectionRevocationStore interface {
	RevokeConnectionAggregate(context.Context, string, time.Time) (ProviderConnection, error)
}

type OAuthTokenRevocationStore interface {
	ClaimOAuthTokenRevocations(context.Context, time.Time, time.Time, int) ([]OAuthTokenRevocationJob, error)
	CompleteOAuthTokenRevocation(context.Context, string, string) error
	RetryOAuthTokenRevocation(context.Context, string, string, time.Time) error
}

// SubjectSyncSettingsReader exposes only the subject preferences needed by
// scheduling. The bool is false when a connection refers to an unmanaged
// legacy subject, in which case SchedulerConfig defaults apply.
type SubjectSyncSettingsReader interface {
	LoadSubjectSyncSettings(context.Context, string) (SubjectSyncSettings, bool, error)
}

type SubjectSyncSettings struct {
	Timezone      string
	Enabled       bool
	Interval      time.Duration
	FailurePolicy activity.FetchFailurePolicy
}

type CustomProviderStore interface {
	SaveCustomProvider(ctx context.Context, record CustomProviderRecord) error
	GetCustomProvider(ctx context.Context, id string) (CustomProviderRecord, error)
	ListCustomProviders(ctx context.Context, subjectID string) ([]CustomProviderRecord, error)
	DeleteCustomProvider(ctx context.Context, id string) error
	SaveIngestedActivities(ctx context.Context, activities []IngestedActivity) ([]IngestedActivity, error)
	ListIngestedActivities(ctx context.Context, providerID string) ([]IngestedActivity, error)
}

// AtomicCustomProviderStore separates insert from conditional mutation and
// removes provider-owned facts/events with the provider row in one transaction.
// This prevents stale PATCH/rotate/ingest work from recreating deleted state.
type AtomicCustomProviderStore interface {
	CreateCustomProvider(context.Context, CustomProviderRecord) error
	UpdateCustomProvider(context.Context, CustomProviderRecord) error
	DeleteCustomProviderAggregate(context.Context, string) error
}

type IngestIdempotencyStore interface {
	CreateIngestIdempotencyKey(context.Context, IngestIdempotencyRecord) (bool, error)
	GetIngestIdempotencyKey(context.Context, string, []byte) (IngestIdempotencyRecord, bool, error)
}

// AtomicIngestIdempotencyStore extends the durable replay store with a
// pre-mutation reservation lifecycle. A reservation has ResponseStatus zero;
// completion and release require both the same request hash and the unique
// reservation token, so a previous owner cannot mutate a replacement lease.
type AtomicIngestIdempotencyStore interface {
	IngestIdempotencyStore
	CompleteIngestIdempotencyKey(context.Context, IngestIdempotencyRecord) (bool, error)
	ReleaseIngestIdempotencyKey(context.Context, string, []byte, []byte, string) error
}

// AtomicCustomIngestStore is the production transaction boundary for durable
// custom ingest. The callback performs only database work through the same
// transaction context; the implementation locks and rechecks current provider
// status and secret before allowing any reservation or activity write.
type AtomicCustomIngestStore interface {
	RunAuthenticatedIngest(context.Context, string, []byte, func(context.Context, CustomProviderRecord) error) error
}

// TransactionalActivitySink refuses custom-ingest fact writes unless its
// Cockroach pool owns the active transaction in ctx.
type TransactionalActivitySink interface {
	SaveFactsInCurrentTransaction(context.Context, activity.SaveFactsInput) error
}

type SyncJobStore interface {
	SaveSyncJob(ctx context.Context, job SyncJob) error
	GetSyncJob(ctx context.Context, id string) (SyncJob, error)
	ListSyncJobs(ctx context.Context, connectionID string) ([]SyncJob, error)
	ListSyncJobsPage(ctx context.Context, connectionID string, after *SyncJobCursor, limit int) ([]SyncJob, error)
}

// SyncIdempotencyStore is an optional durable capability of SyncJobStore.
// Keys are already SHA-256 digests when they cross this boundary.
type SyncIdempotencyStore interface {
	GetSyncJobByIdempotencyKey(ctx context.Context, connectionID string, keyHash []byte, now time.Time) (SyncJob, bool, error)
}

// SyncJobClaimStore atomically transitions a queued (or abandoned leased) job
// to running. Completion is conditioned on the opaque claim token so an old
// worker cannot overwrite a newer lease after expiry.
type SyncJobClaimStore interface {
	ListClaimableSyncJobs(context.Context, time.Time, int) ([]string, error)
	ClaimSyncJob(context.Context, string, time.Time, time.Time) (claimToken string, acquired bool, err error)
	CompleteClaimedSyncJob(context.Context, SyncJob, string) error
}

// SyncExecutionStore is an optional connection-store capability used to
// prevent concurrent execution across multiple SyncService instances sharing
// the same store. Implementations must acquire atomically.
type SyncExecutionStore interface {
	TryAcquireSyncExecution(ctx context.Context, connectionID string) (claimToken string, acquired bool, err error)
	ReleaseSyncExecution(ctx context.Context, connectionID, claimToken string) error
}

// SyncConnectionStateStore is the least-privilege worker mutation boundary.
// Unlike ConnectionStore.SaveConnection it must never insert a missing row;
// implementations condition updates on the current execution claim and a
// syncable connection state.
type SyncConnectionStateStore interface {
	UpdateConnectionAfterSync(ctx context.Context, record ConnectionRecord, claimToken string) error
}

// RetryPolicy calculates the next retry after consecutiveFailureCount. A false
// return means that the bounded retry budget has been exhausted.
type RetryPolicy interface {
	NextRetry(consecutiveFailureCount int) (time.Duration, bool)
}

type JitterSource interface {
	Float64() float64
}

type ActivitySink interface {
	SaveEnvironments(ctx context.Context, input activity.SaveEnvironmentsInput) error
	SaveFacts(ctx context.Context, input activity.SaveFactsInput) error
}

// FencedActivitySink is the production synchronization write boundary. The
// implementation must atomically verify that connectionID is still syncable
// and that claimToken still owns its execution lease before committing either
// environments or facts. This prevents a disconnect or subject deletion that
// races provider I/O from being undone by a stale worker.
type FencedActivitySink interface {
	SaveConnectionActivity(
		ctx context.Context,
		connectionID string,
		claimToken string,
		environments activity.SaveEnvironmentsInput,
		facts activity.SaveFactsInput,
	) error
}

type CredentialSource interface {
	AccessToken(ctx context.Context) ([]byte, error)
	RefreshToken(ctx context.Context) ([]byte, error)
}

type ProviderSyncRequest struct {
	Connection    ProviderConnection
	Credentials   CredentialSource
	Timezone      string
	From          *activity.Date
	To            *activity.Date
	Force         bool
	FailurePolicy activity.FetchFailurePolicy
}

type ProviderSyncResult struct {
	Environments []activity.Environment
	Facts        []activity.Fact
}

// ProviderSyncer is implemented by Git hosting adapters. Plaintext credentials
// are available only on demand and are never stored in the request value.
type ProviderSyncer interface {
	ProviderID() string
	Sync(ctx context.Context, request ProviderSyncRequest) (ProviderSyncResult, error)
}
