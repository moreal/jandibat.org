package integrations

import (
	"encoding/json"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

// ProviderKind identifies the implementation used to collect activity.
type ProviderKind string

const (
	ProviderGitHub   ProviderKind = "github"
	ProviderGitLab   ProviderKind = "gitlab"
	ProviderCodeberg ProviderKind = "codeberg"
	ProviderCustom   ProviderKind = "custom"
)

type ProviderCategory string

const (
	CategoryGitHosting ProviderCategory = "git-hosting"
	CategoryCustom     ProviderCategory = "custom"
)

// ProviderCatalogItem is safe to return through an API. It never contains
// connection credentials.
type ProviderCatalogItem struct {
	ID                  string
	Name                string
	Description         string
	Kind                ProviderKind
	Category            ProviderCategory
	SupportsOAuth       bool
	SupportsToken       bool
	SupportsPrivateData bool
	CustomProviderID    string
}

type AuthMethod string

const (
	AuthNone   AuthMethod = "none"
	AuthOAuth2 AuthMethod = "oauth2"
	AuthToken  AuthMethod = "token"
)

type ConnectionStatus string

const (
	ConnectionPending  ConnectionStatus = "pending"
	ConnectionActive   ConnectionStatus = "active"
	ConnectionDisabled ConnectionStatus = "disabled"
	ConnectionRevoked  ConnectionStatus = "revoked"
	ConnectionError    ConnectionStatus = "error"
)

// ProviderConnection is the public, credential-free connection view.
type ProviderConnection struct {
	ID                string
	SubjectID         string
	ProviderID        string
	EnvironmentID     string
	AuthMethod        AuthMethod
	ExternalAccountID string
	// ExternalAccountLogin is the provider username used for outbound fetches.
	// It is distinct from the immutable provider account ID and is not exposed
	// as an API field.
	ExternalAccountLogin string
	Status               ConnectionStatus
	Scopes               []string
	PrivateDataEnabled   bool
	TokenExpiresAt       *time.Time
	LastSyncedAt         *time.Time
	LastSyncAttemptAt    *time.Time
	NextSyncAttemptAt    *time.Time
	LastSyncAttempt      int
	ConsecutiveFailures  int
	LastError            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// EncryptedCredentials is persistence data, not an API response model. Secret
// material is encrypted by ConnectionService before this value reaches a store.
type EncryptedCredentials struct {
	AccessToken  []byte
	RefreshToken []byte
}

// ConnectionRecord is the storage representation of a provider connection.
type ConnectionRecord struct {
	Connection  ProviderConnection
	Credentials EncryptedCredentials
}

type OAuthTokenRevocationJob struct {
	ID              string
	ConnectionID    string
	ProviderID      string
	TokenCiphertext []byte
	TokenKeyID      string
	ClaimToken      string
	Attempts        int
	AvailableAt     time.Time
	LeaseUntil      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type TokenCredentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
}

type CustomProviderStatus string

const (
	CustomProviderActive   CustomProviderStatus = "active"
	CustomProviderDisabled CustomProviderStatus = "disabled"
)

// CustomProvider is safe to expose to a provider owner. Ingest secrets are
// deliberately absent.
type CustomProvider struct {
	ID             string
	SubjectID      string
	EnvironmentID  string
	Slug           string
	Name           string
	Description    string
	Status         CustomProviderStatus
	AllowedActions []string
	AllowedMetrics []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CustomProviderRecord is the persistence representation. The historically
// named EncryptedIngestSecret field contains a SHA-256 digest, never the
// plaintext ingestion key. The persisted database column is ingest_token_hash.
type CustomProviderRecord struct {
	Provider              CustomProvider
	EncryptedIngestSecret []byte
}

type CustomActivity struct {
	ExternalID string
	Date       string
	Action     string
	Metric     string
	Value      int
	Metadata   map[string]string
	ObservedAt *time.Time
}

type IngestedActivity struct {
	ProviderID string
	SubjectID  string
	ExternalID string
	Date       string
	Action     string
	Metric     string
	Value      int
	Metadata   map[string]string
	ObservedAt *time.Time
	IngestedAt time.Time
}

type IngestIdempotencyRecord struct {
	ProviderID     string
	KeyHash        []byte
	RequestHash    []byte
	ResponseStatus int
	ResponseBody   json.RawMessage
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type SyncJobStatus string

const (
	SyncJobPending   SyncJobStatus = "pending"
	SyncJobRunning   SyncJobStatus = "running"
	SyncJobSucceeded SyncJobStatus = "succeeded"
	SyncJobFailed    SyncJobStatus = "failed"
)

type SyncTrigger string

const (
	SyncTriggerManual    SyncTrigger = "manual"
	SyncTriggerScheduled SyncTrigger = "scheduled"
)

type SyncJob struct {
	ID            string
	ConnectionID  string
	Trigger       SyncTrigger
	Status        SyncJobStatus
	From          *activity.Date
	To            *activity.Date
	Force         bool
	Timezone      string
	FailurePolicy activity.FetchFailurePolicy
	StartedAt     *time.Time
	FinishedAt    *time.Time
	NextAttemptAt *time.Time
	Attempt       int
	FactsWritten  int
	LastError     string
	// IdempotencyKeyHash and RequestHash contain one-way SHA-256 digests. They
	// are persistence metadata and must never be exposed through the API.
	IdempotencyKeyHash []byte
	RequestHash        []byte
	IdempotencyExpires *time.Time
	ClaimToken         string
	ClaimLeaseUntil    *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
