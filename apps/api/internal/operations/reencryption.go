package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidReencryptionConfig  = errors.New("operations: invalid re-encryption configuration")
	ErrInvalidSecretRecord        = errors.New("operations: invalid encrypted secret record")
	ErrReencryptionResumeMismatch = errors.New("operations: re-encryption checkpoint does not match active key")
)

type SecretKind string

const (
	SecretConnectionAccessToken  SecretKind = "connection_access_token"
	SecretConnectionRefreshToken SecretKind = "connection_refresh_token"
	SecretOAuthRevocationToken   SecretKind = "oauth_revocation_token"
)

// SecretLocator is a stable cursor and must uniquely identify one encrypted
// column. Store implementations return records in locator order.
type SecretLocator struct {
	Kind SecretKind
	ID   string
}

type EncryptedSecretRecord struct {
	Locator    SecretLocator
	KeyID      string
	Ciphertext []byte
}

// SecretReencryptionStore exposes only the minimum persistence operations
// needed for online key rotation. ReplaceEncryptedSecret must compare both the
// old key ID and ciphertext and return false when a concurrent write won.
type SecretReencryptionStore interface {
	// ListSecretsForReencryption returns every encrypted secret in stable cursor
	// order. The worker must inspect envelope versions itself because a
	// version-1 value can already have the active key ID in its database column.
	ListSecretsForReencryption(ctx context.Context, after SecretLocator, limit int) ([]EncryptedSecretRecord, error)
	ReplaceEncryptedSecret(ctx context.Context, expected EncryptedSecretRecord, newKeyID string, ciphertext []byte) (bool, error)
}

type ReencryptionResult struct {
	Scanned  int
	Rotated  int
	Pending  int
	Skipped  int
	Failed   int
	LastSeen SecretLocator
	ByKey    map[string]ReencryptionKeyResult
	DryRun   bool
	Resumed  bool
}

type ReencryptionKeyResult struct {
	Scanned int `json:"scanned"`
	Rotated int `json:"rotated"`
	Pending int `json:"pending"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

type ReencryptionOperatorOptions struct {
	DryRun bool
	Resume bool
	Scope  string
}

type ReencryptionWorker struct {
	store     SecretReencryptionStore
	keyring   RotatingSecretCipher
	batchSize int
}

func NewReencryptionWorker(store SecretReencryptionStore, keyring RotatingSecretCipher, batchSize int) (*ReencryptionWorker, error) {
	if store == nil || keyring == nil || batchSize <= 0 {
		return nil, ErrInvalidReencryptionConfig
	}
	return &ReencryptionWorker{store: store, keyring: keyring, batchSize: batchSize}, nil
}

// Run rotates all records visible at the beginning of each cursor page. A
// corrupt record does not prevent later records from rotating; all failures are
// joined after the scan. Compare-and-swap prevents overwriting concurrent
// credential refreshes.
func (worker *ReencryptionWorker) Run(ctx context.Context) (ReencryptionResult, error) {
	return worker.runFrom(ctx, SecretLocator{}, false, nil)
}

type reencryptionCheckpointPayload struct {
	Version     int                `json:"version"`
	ActiveKeyID string             `json:"active_key_id"`
	After       SecretLocator      `json:"after"`
	Result      ReencryptionResult `json:"result"`
}

// RunOperator adds a side-effect-free inventory mode and durable cursor
// resume to the same CAS-protected scan used by the periodic worker.
func (worker *ReencryptionWorker) RunOperator(ctx context.Context, checkpoints MaintenanceCheckpointStore, options ReencryptionOperatorOptions) (ReencryptionResult, error) {
	if options.DryRun {
		return worker.runFrom(ctx, SecretLocator{}, true, nil)
	}
	scope := strings.TrimSpace(options.Scope)
	if checkpoints == nil || scope == "" || len(scope) > 255 {
		return ReencryptionResult{}, fmt.Errorf("%w: execute requires a checkpoint store and scope", ErrInvalidReencryptionConfig)
	}
	after := SecretLocator{}
	prefix := ReencryptionResult{ByKey: make(map[string]ReencryptionKeyResult)}
	if options.Resume {
		checkpoint, found, err := checkpoints.LoadCheckpoint(ctx, MaintenanceCredentialEncryption, scope)
		if err != nil {
			return prefix, fmt.Errorf("load re-encryption checkpoint: %w", err)
		}
		if found {
			var payload reencryptionCheckpointPayload
			if err := json.Unmarshal(checkpoint.Payload, &payload); err != nil || payload.Version != 1 ||
				payload.ActiveKeyID != worker.keyring.ActiveKeyID() ||
				(payload.After != (SecretLocator{}) && (!validSecretKind(payload.After.Kind) || strings.TrimSpace(payload.After.ID) == "")) {
				return prefix, ErrReencryptionResumeMismatch
			}
			after = payload.After
			prefix = payload.Result
			if prefix.ByKey == nil {
				prefix.ByKey = make(map[string]ReencryptionKeyResult)
			}
			prefix.Resumed = true
		}
	}
	result, runErr := worker.runFrom(ctx, after, false, func(pageResult ReencryptionResult) error {
		combined := combineReencryptionResults(prefix, pageResult)
		payload := reencryptionCheckpointPayload{
			Version: 1, ActiveKeyID: worker.keyring.ActiveKeyID(), After: pageResult.LastSeen, Result: combined,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode re-encryption checkpoint: %w", err)
		}
		if err := checkpoints.SaveCheckpoint(ctx, MaintenanceCheckpoint{
			Operation: MaintenanceCredentialEncryption, Scope: scope, Payload: encoded, UpdatedAt: time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("save re-encryption checkpoint: %w", err)
		}
		return nil
	})
	result = combineReencryptionResults(prefix, result)
	result.Resumed = prefix.Resumed
	if runErr != nil {
		return result, runErr
	}
	if err := checkpoints.DeleteCheckpoint(ctx, MaintenanceCredentialEncryption, scope); err != nil {
		return result, fmt.Errorf("delete completed re-encryption checkpoint: %w", err)
	}
	return result, nil
}

func (worker *ReencryptionWorker) runFrom(ctx context.Context, start SecretLocator, dryRun bool, pageComplete func(ReencryptionResult) error) (ReencryptionResult, error) {
	result := ReencryptionResult{ByKey: make(map[string]ReencryptionKeyResult), DryRun: dryRun}
	var failures []error
	after := start
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		records, err := worker.store.ListSecretsForReencryption(ctx, after, worker.batchSize)
		if err != nil {
			return result, fmt.Errorf("list secrets after %s/%s: %w", after.Kind, after.ID, err)
		}
		if len(records) > worker.batchSize {
			return result, fmt.Errorf("%w: store returned %d records for limit %d", ErrInvalidSecretRecord, len(records), worker.batchSize)
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return result, errors.Join(err, errors.Join(failures...))
			}
			if err := validateEncryptedSecret(record, after); err != nil {
				return result, err
			}
			after = record.Locator
			result.LastSeen = after
			result.Scanned++
			keyID := reencryptionSourceKeyID(record)
			updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Scanned++ })

			needsReencryption, inspectErr := worker.keyring.NeedsReencryption(record.Ciphertext, record.KeyID)
			if inspectErr != nil {
				result.Failed++
				updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Failed++ })
				failures = append(failures, fmt.Errorf("inspect %s/%s: %w", record.Locator.Kind, record.Locator.ID, inspectErr))
				continue
			}
			if !needsReencryption {
				result.Skipped++
				updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Skipped++ })
				continue
			}

			rotated, rotateErr := worker.keyring.Reencrypt(ctx, record.Ciphertext)
			if rotateErr != nil {
				result.Failed++
				updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Failed++ })
				failures = append(failures, fmt.Errorf("re-encrypt %s/%s: %w", record.Locator.Kind, record.Locator.ID, rotateErr))
				continue
			}
			if dryRun {
				clear(rotated)
				result.Pending++
				updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Pending++ })
				continue
			}
			replaced, replaceErr := worker.store.ReplaceEncryptedSecret(ctx, record, worker.keyring.ActiveKeyID(), rotated)
			clear(rotated)
			if replaceErr != nil {
				result.Failed++
				updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Failed++ })
				failures = append(failures, fmt.Errorf("replace %s/%s: %w", record.Locator.Kind, record.Locator.ID, replaceErr))
				continue
			}
			if !replaced {
				result.Skipped++
				updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Skipped++ })
				continue
			}
			result.Rotated++
			updateReencryptionKeyResult(result.ByKey, keyID, func(key *ReencryptionKeyResult) { key.Rotated++ })
		}
		if pageComplete != nil && len(records) != 0 {
			if err := pageComplete(result); err != nil {
				return result, errors.Join(err, errors.Join(failures...))
			}
		}
		if len(records) < worker.batchSize {
			break
		}
	}
	return result, errors.Join(failures...)
}

func reencryptionSourceKeyID(record EncryptedSecretRecord) string {
	if keyID, enveloped, err := CiphertextKeyID(record.Ciphertext); err == nil && enveloped && strings.TrimSpace(keyID) != "" {
		return keyID
	}
	if keyID := strings.TrimSpace(record.KeyID); keyID != "" {
		return keyID
	}
	return "legacy-or-unknown"
}

func updateReencryptionKeyResult(results map[string]ReencryptionKeyResult, keyID string, update func(*ReencryptionKeyResult)) {
	result := results[keyID]
	update(&result)
	results[keyID] = result
}

func combineReencryptionResults(left, right ReencryptionResult) ReencryptionResult {
	result := ReencryptionResult{
		Scanned: left.Scanned + right.Scanned, Rotated: left.Rotated + right.Rotated,
		Pending: left.Pending + right.Pending, Skipped: left.Skipped + right.Skipped,
		Failed: left.Failed + right.Failed, LastSeen: right.LastSeen,
		ByKey: make(map[string]ReencryptionKeyResult), DryRun: left.DryRun || right.DryRun,
		Resumed: left.Resumed || right.Resumed,
	}
	if result.LastSeen == (SecretLocator{}) {
		result.LastSeen = left.LastSeen
	}
	for keyID, item := range left.ByKey {
		result.ByKey[keyID] = item
	}
	for keyID, item := range right.ByKey {
		current := result.ByKey[keyID]
		current.Scanned += item.Scanned
		current.Rotated += item.Rotated
		current.Pending += item.Pending
		current.Skipped += item.Skipped
		current.Failed += item.Failed
		result.ByKey[keyID] = current
	}
	return result
}

func validateEncryptedSecret(record EncryptedSecretRecord, after SecretLocator) error {
	if !validSecretKind(record.Locator.Kind) || strings.TrimSpace(record.Locator.ID) == "" || len(record.Ciphertext) == 0 {
		return fmt.Errorf("%w: kind, ID, and ciphertext are required", ErrInvalidSecretRecord)
	}
	if compareSecretLocators(record.Locator, after) <= 0 {
		return fmt.Errorf("%w: records are not strictly ordered after %s/%s", ErrInvalidSecretRecord, after.Kind, after.ID)
	}
	return nil
}

func validSecretKind(kind SecretKind) bool {
	switch kind {
	case SecretConnectionAccessToken, SecretConnectionRefreshToken, SecretOAuthRevocationToken:
		return true
	default:
		return false
	}
}

func compareSecretLocators(left, right SecretLocator) int {
	if comparison := bytes.Compare([]byte(left.Kind), []byte(right.Kind)); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.ID, right.ID)
}
