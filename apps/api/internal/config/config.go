package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentProduction  = "production"

	ProcessAPI         = "api"
	ProcessWorker      = "worker"
	ProcessMaintenance = "maintenance"
)

var (
	ErrInvalidEnvironment = errors.New("config: APP_ENV must be development or production")
	ErrMissingSecret      = errors.New("config: production secrets are required")
	ErrForbiddenSecret    = errors.New("config: secret is forbidden for this process")
)

// Config contains process-level settings. Feature packages receive only the
// individual settings they need rather than depending on Config directly.
type Config struct {
	Environment              string
	APIAddress               string
	WorkerHealthAddress      string
	MaintenanceHealthAddress string
	PublicURL                *url.URL
	WebURL                   *url.URL
	DatabaseURL              string
	WorkerDatabaseURL        string
	MaintenanceDatabaseURL   string
	DevelopmentAllInOne      bool

	SessionSigningKey []byte
	// DeletionPseudonymKey is dedicated to irreversible identifiers in
	// deletion audit events. It must never reuse the session-signing key.
	DeletionPseudonymKey []byte
	// DeletedIdentityHMACKeys are dedicated versioned keys for one-way email
	// tombstones. They must not reuse session, deletion-pseudonym, or
	// credential-encryption keys.
	DeletedIdentityHMACActiveKeyID string
	DeletedIdentityHMACKeys        map[string][]byte
	// CredentialCipherKey is the transition-only, pre-keyring credential key.
	// It is never selected for new writes when CredentialEncryptionKeys is
	// configured, but remains available for reading legacy, un-enveloped rows.
	CredentialCipherKey      []byte
	CredentialActiveKeyID    string
	CredentialEncryptionKeys map[string][]byte
	// CredentialEncryptionPublicKeys contains DER PKIX RSA public keys used for
	// version-3 encrypt-only writes. Private keys are loaded only by worker and
	// maintenance processes and must never be present in the API container.
	CredentialEncryptionPublicKeys  map[string][]byte
	CredentialEncryptionPrivateKeys map[string][]byte

	SMTPAddress  string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string

	GitHubClientID     string
	GitHubClientSecret string
	GitLabClientID     string
	GitLabClientSecret string
	CodebergClientID   string
	CodebergSecret     string

	SchedulerInterval    time.Duration
	ShutdownTimeout      time.Duration
	RetentionInterval    time.Duration
	ReencryptionInterval time.Duration
	MaintenanceTimeout   time.Duration
	MaintenanceBatchSize int
	RetentionMaxBatches  int
	TrustProxyHeaders    bool
}

type Lookup func(string) (string, bool)

func Load(lookup Lookup) (Config, error) {
	return LoadForProcess(lookup, ProcessAPI)
}

// LoadForProcess parses shared runtime settings while enforcing only the
// secrets needed by the selected executable. Worker and maintenance processes
// therefore do not need access to the API session-signing secret.
func LoadForProcess(lookup Lookup, process string) (Config, error) {
	if process != ProcessAPI && process != ProcessWorker && process != ProcessMaintenance {
		return Config{}, fmt.Errorf("config: unsupported process %q", process)
	}
	environment := valueOr(lookup, "APP_ENV", EnvironmentDevelopment)
	if environment != EnvironmentDevelopment && environment != EnvironmentProduction {
		return Config{}, ErrInvalidEnvironment
	}

	publicURL, err := parseURL("PUBLIC_BASE_URL", valueOr(lookup, "PUBLIC_BASE_URL", "http://localhost:8080"))
	if err != nil {
		return Config{}, err
	}
	webURL, err := parseURL("WEB_BASE_URL", valueOr(lookup, "WEB_BASE_URL", "http://localhost:5173"))
	if err != nil {
		return Config{}, err
	}

	schedulerInterval, err := duration(lookup, "SCHEDULER_INTERVAL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := duration(lookup, "SHUTDOWN_TIMEOUT", 40*time.Second)
	if err != nil {
		return Config{}, err
	}
	retentionInterval, err := duration(lookup, "RETENTION_INTERVAL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	reencryptionInterval, err := duration(lookup, "REENCRYPTION_INTERVAL", time.Hour)
	if err != nil {
		return Config{}, err
	}
	maintenanceTimeout, err := duration(lookup, "MAINTENANCE_TIMEOUT", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	maintenanceBatchSize, err := positiveInteger(lookup, "MAINTENANCE_BATCH_SIZE", 500)
	if err != nil {
		return Config{}, err
	}
	retentionMaxBatches, err := positiveInteger(lookup, "RETENTION_MAX_BATCHES", 30)
	if err != nil {
		return Config{}, err
	}
	trustProxyHeaders, err := boolean(lookup, "TRUST_PROXY_HEADERS", false)
	if err != nil {
		return Config{}, err
	}
	developmentAllInOne, err := boolean(lookup, "DEVELOPMENT_ALL_IN_ONE", false)
	if err != nil {
		return Config{}, err
	}
	if environment == EnvironmentProduction && developmentAllInOne {
		return Config{}, fmt.Errorf("config: DEVELOPMENT_ALL_IN_ONE is forbidden in production")
	}

	sessionKey, err := secret(lookup, "SESSION_SIGNING_KEY")
	if err != nil {
		return Config{}, err
	}
	deletionPseudonymKey, err := secretURL(lookup, "DELETION_PSEUDONYM_KEY")
	if err != nil {
		return Config{}, err
	}
	if len(deletionPseudonymKey) != 0 && len(deletionPseudonymKey) < 32 {
		return Config{}, fmt.Errorf("config: DELETION_PSEUDONYM_KEY needs at least 32 bytes when set")
	}
	cipherKey, err := secret(lookup, "CREDENTIAL_ENCRYPTION_KEY")
	if err != nil {
		return Config{}, err
	}
	if len(cipherKey) != 0 && len(cipherKey) != 32 {
		return Config{}, fmt.Errorf("config: CREDENTIAL_ENCRYPTION_KEY must decode to exactly 32 bytes when set")
	}
	activeCredentialKeyID := valueOr(lookup, "CREDENTIAL_ACTIVE_KEY_ID", "")
	credentialKeys, err := secretMap(lookup, "CREDENTIAL_ENCRYPTION_KEYS")
	if err != nil {
		return Config{}, err
	}
	credentialPublicKeys, err := secretBlobMap(lookup, "CREDENTIAL_ENCRYPTION_PUBLIC_KEYS")
	if err != nil {
		return Config{}, err
	}
	credentialPrivateKeys, err := secretBlobMap(lookup, "CREDENTIAL_ENCRYPTION_PRIVATE_KEYS")
	if err != nil {
		return Config{}, err
	}
	deletedIdentityHMACActiveKeyID := valueOr(lookup, "DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID", "")
	deletedIdentityHMACKeys, err := secretMap(lookup, "DELETED_IDENTITY_HMAC_KEYS")
	if err != nil {
		return Config{}, err
	}
	if environment == EnvironmentProduction {
		if activeCredentialKeyID == "" || len(credentialPublicKeys[activeCredentialKeyID]) == 0 {
			return Config{}, fmt.Errorf("%w: CREDENTIAL_ACTIVE_KEY_ID must select a key from CREDENTIAL_ENCRYPTION_PUBLIC_KEYS", ErrMissingSecret)
		}
		if process == ProcessAPI && (len(credentialPrivateKeys) != 0 || len(credentialKeys) != 0 || len(cipherKey) != 0) {
			return Config{}, fmt.Errorf("%w: API may receive only CREDENTIAL_ENCRYPTION_PUBLIC_KEYS", ErrForbiddenSecret)
		}
		if process != ProcessAPI && len(credentialPrivateKeys[activeCredentialKeyID]) == 0 {
			return Config{}, fmt.Errorf("%w: CREDENTIAL_ACTIVE_KEY_ID must select a key from CREDENTIAL_ENCRYPTION_PRIVATE_KEYS", ErrMissingSecret)
		}
		if process == ProcessAPI && len(sessionKey) < 32 {
			return Config{}, fmt.Errorf("%w: SESSION_SIGNING_KEY needs at least 32 bytes", ErrMissingSecret)
		}
		if process == ProcessMaintenance && len(deletionPseudonymKey) < 32 {
			return Config{}, fmt.Errorf("%w: DELETION_PSEUDONYM_KEY needs at least 32 bytes", ErrMissingSecret)
		}
		if process == ProcessAPI || process == ProcessMaintenance {
			if deletedIdentityHMACActiveKeyID == "" || len(deletedIdentityHMACKeys[deletedIdentityHMACActiveKeyID]) != 32 {
				return Config{}, fmt.Errorf("%w: DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID must select a 32-byte key from DELETED_IDENTITY_HMAC_KEYS", ErrMissingSecret)
			}
		} else if len(deletedIdentityHMACKeys) != 0 || deletedIdentityHMACActiveKeyID != "" {
			return Config{}, fmt.Errorf("%w: worker must not receive DELETED_IDENTITY_HMAC_KEYS", ErrForbiddenSecret)
		}
		smtpValues := []string{
			valueOr(lookup, "SMTP_ADDR", ""), valueOr(lookup, "SMTP_USERNAME", ""),
			valueOr(lookup, "SMTP_PASSWORD", ""), valueOr(lookup, "SMTP_FROM", ""),
		}
		if process == ProcessWorker {
			for _, value := range smtpValues {
				if strings.TrimSpace(value) == "" {
					return Config{}, fmt.Errorf("%w: worker requires the complete SMTP configuration", ErrMissingSecret)
				}
			}
		} else {
			for _, value := range smtpValues {
				if strings.TrimSpace(value) != "" {
					return Config{}, fmt.Errorf("%w: SMTP credentials are worker-only", ErrForbiddenSecret)
				}
			}
		}
		if err := rejectSecretDomainReuse(sessionKey, deletionPseudonymKey, deletedIdentityHMACKeys,
			credentialKeys, credentialPublicKeys, credentialPrivateKeys); err != nil {
			return Config{}, err
		}
	}

	return Config{
		Environment:                     environment,
		APIAddress:                      valueOr(lookup, "API_ADDR", ":8080"),
		WorkerHealthAddress:             valueOr(lookup, "WORKER_HEALTH_ADDR", ":8081"),
		MaintenanceHealthAddress:        valueOr(lookup, "MAINTENANCE_HEALTH_ADDR", ":8082"),
		PublicURL:                       publicURL,
		WebURL:                          webURL,
		DatabaseURL:                     valueOr(lookup, "DATABASE_URL", ""),
		WorkerDatabaseURL:               valueOr(lookup, "WORKER_DATABASE_URL", ""),
		MaintenanceDatabaseURL:          valueOr(lookup, "MAINTENANCE_DATABASE_URL", ""),
		DevelopmentAllInOne:             developmentAllInOne,
		SessionSigningKey:               sessionKey,
		DeletionPseudonymKey:            deletionPseudonymKey,
		DeletedIdentityHMACActiveKeyID:  deletedIdentityHMACActiveKeyID,
		DeletedIdentityHMACKeys:         deletedIdentityHMACKeys,
		CredentialCipherKey:             cipherKey,
		CredentialActiveKeyID:           activeCredentialKeyID,
		CredentialEncryptionKeys:        credentialKeys,
		CredentialEncryptionPublicKeys:  credentialPublicKeys,
		CredentialEncryptionPrivateKeys: credentialPrivateKeys,
		SMTPAddress:                     valueOr(lookup, "SMTP_ADDR", ""),
		SMTPUsername:                    valueOr(lookup, "SMTP_USERNAME", ""),
		SMTPPassword:                    valueOr(lookup, "SMTP_PASSWORD", ""),
		SMTPFrom:                        valueOr(lookup, "SMTP_FROM", ""),
		GitHubClientID:                  valueOr(lookup, "GITHUB_CLIENT_ID", ""),
		GitHubClientSecret:              valueOr(lookup, "GITHUB_CLIENT_SECRET", ""),
		GitLabClientID:                  valueOr(lookup, "GITLAB_CLIENT_ID", ""),
		GitLabClientSecret:              valueOr(lookup, "GITLAB_CLIENT_SECRET", ""),
		CodebergClientID:                valueOr(lookup, "CODEBERG_CLIENT_ID", ""),
		CodebergSecret:                  valueOr(lookup, "CODEBERG_CLIENT_SECRET", ""),
		SchedulerInterval:               schedulerInterval,
		ShutdownTimeout:                 shutdownTimeout,
		RetentionInterval:               retentionInterval,
		ReencryptionInterval:            reencryptionInterval,
		MaintenanceTimeout:              maintenanceTimeout,
		MaintenanceBatchSize:            maintenanceBatchSize,
		RetentionMaxBatches:             retentionMaxBatches,
		TrustProxyHeaders:               trustProxyHeaders,
	}, nil
}

func rejectSecretDomainReuse(sessionKey, deletionKey []byte, maps ...map[string][]byte) error {
	type material struct {
		domain string
		value  []byte
	}
	values := make([]material, 0)
	if len(sessionKey) != 0 {
		values = append(values, material{domain: "session", value: sessionKey})
	}
	if len(deletionKey) != 0 {
		values = append(values, material{domain: "deletion", value: deletionKey})
	}
	domains := []string{"identity-hmac", "credential-symmetric", "credential-public", "credential-private"}
	for index, configured := range maps {
		for _, value := range configured {
			values = append(values, material{domain: domains[index], value: value})
		}
	}
	for left := range values {
		for right := left + 1; right < len(values); right++ {
			if values[left].domain != values[right].domain && string(values[left].value) == string(values[right].value) {
				return fmt.Errorf("%w: secret material is reused across %s and %s domains", ErrForbiddenSecret, values[left].domain, values[right].domain)
			}
		}
	}
	return nil
}

func valueOr(lookup Lookup, key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func parseURL(key, value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("config: %s must be an absolute URL", key)
	}
	return parsed, nil
}

func duration(lookup Lookup, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive duration", key)
	}
	return parsed, nil
}

func boolean(lookup Lookup, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean", key)
	}
	return parsed, nil
}

func positiveInteger(lookup Lookup, key string, fallback int) (int, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive integer", key)
	}
	return parsed, nil
}

func secret(lookup Lookup, key string) ([]byte, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("config: %s must be unpadded base64: %w", key, err)
	}
	return decoded, nil
}

func secretURL(lookup Lookup, key string) ([]byte, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("config: %s must be unpadded base64url: %w", key, err)
	}
	return decoded, nil
}

func secretMap(lookup Lookup, key string) (map[string][]byte, error) {
	return secretMapWithLength(lookup, key, 32)
}

func secretBlobMap(lookup Lookup, key string) (map[string][]byte, error) {
	return secretMapWithLength(lookup, key, 0)
}

func secretMapWithLength(lookup Lookup, key string, exactLength int) (map[string][]byte, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return map[string][]byte{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("config: %s must be a JSON object mapping key IDs to unpadded base64 values: %w", key, err)
	}
	delimiter, object := token.(json.Delim)
	if !object || delimiter != '{' {
		return nil, fmt.Errorf("config: %s must be a JSON object mapping key IDs to unpadded base64 values", key)
	}
	decoded := make(map[string][]byte)
	for decoder.More() {
		rawToken, tokenErr := decoder.Token()
		rawID, stringKey := rawToken.(string)
		if tokenErr != nil || !stringKey {
			return nil, fmt.Errorf("config: %s contains an invalid key ID", key)
		}
		if _, duplicate := decoded[rawID]; duplicate {
			return nil, fmt.Errorf("config: %s contains duplicate key ID %q", key, rawID)
		}
		var encodedMaterial string
		if err := decoder.Decode(&encodedMaterial); err != nil {
			return nil, fmt.Errorf("config: %s key %q must contain an unpadded base64 string", key, rawID)
		}
		id := strings.TrimSpace(rawID)
		if id == "" || len(id) > 128 || id != rawID {
			return nil, fmt.Errorf("config: %s contains an invalid key ID", key)
		}
		material, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(encodedMaterial))
		if err != nil || len(material) == 0 || len(material) > 16<<10 || exactLength > 0 && len(material) != exactLength {
			if exactLength > 0 {
				return nil, fmt.Errorf("config: %s key %q must be exactly %d bytes encoded as unpadded base64", key, id, exactLength)
			}
			return nil, fmt.Errorf("config: %s key %q must be a non-empty DER value up to 16 KiB encoded as unpadded base64", key, id)
		}
		decoded[id] = material
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("config: %s must contain a complete JSON object", key)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("config: %s must contain exactly one JSON object", key)
	}
	return decoded, nil
}
