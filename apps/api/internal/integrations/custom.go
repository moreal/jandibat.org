package integrations

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	MaxCustomIngestBatch           = 1000
	MaxCustomMetricValue           = 1_000_000
	maxCustomProviderSlugLength    = 64
	maxCustomProviderNameLength    = 100
	maxCustomProviderDescription   = 500
	maxCustomAllowedValues         = 100
	maxCustomAllowedValueLength    = 64
	maxCustomExternalIDLength      = 255
	maxCustomMetadataProperties    = 50
	maxCustomMetadataValueLength   = 1000
	maxCustomActivityBackfillYears = 10
	maxCustomActivityFutureDays    = 1
	minCustomIngestSecretLength    = 32
	minIdempotencyKeyLength        = 8
	maxIdempotencyKeyLength        = 255
	customIdempotencyRetention     = 24 * time.Hour
)

var customProviderSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type customIngestIdempotencyRecord struct {
	fingerprint [sha256.Size]byte
	result      IngestCustomActivitiesResult
	expiresAt   time.Time
}

type CustomProviderService struct {
	store CustomProviderStore
	sink  ActivitySink
	clock Clock
	ids   IDGenerator

	idempotencyMu sync.Mutex
	idempotency   map[string]customIngestIdempotencyRecord
}

func NewCustomProviderService(store CustomProviderStore, cipher SecretCipher, sink ActivitySink, clock Clock, ids IDGenerator) (*CustomProviderService, error) {
	if store == nil {
		return nil, ErrMissingStore
	}
	if cipher == nil {
		return nil, ErrMissingCipher
	}
	if sink == nil {
		return nil, ErrMissingActivitySink
	}
	return &CustomProviderService{
		store:       store,
		sink:        sink,
		clock:       clockOrDefault(clock),
		ids:         idGeneratorOrDefault(ids),
		idempotency: make(map[string]customIngestIdempotencyRecord),
	}, nil
}

type CreateCustomProviderInput struct {
	SubjectID      string
	EnvironmentID  string
	Slug           string
	Name           string
	Description    string
	AllowedActions []string
	AllowedMetrics []string
	IngestSecret   string
}

func (service *CustomProviderService) Create(ctx context.Context, input CreateCustomProviderInput) (CustomProvider, error) {
	if err := validateCustomProviderInput(input); err != nil {
		return CustomProvider{}, err
	}
	id, err := service.ids.NewID()
	if err != nil {
		return CustomProvider{}, fmt.Errorf("generate custom provider id: %w", err)
	}
	ingestSecretHash := hashIngestSecret(input.IngestSecret)
	now := service.clock.Now()
	provider := CustomProvider{
		ID:        id,
		SubjectID: input.SubjectID,
		// The legacy "custom:<slug>" namespace can collide with an allowed UUID-
		// shaped slug. A distinct prefix makes every new ID both provider-bound
		// and disjoint from every legacy ID produced by older releases.
		EnvironmentID:  "custom-provider:" + id,
		Slug:           input.Slug,
		Name:           strings.TrimSpace(input.Name),
		Description:    strings.TrimSpace(input.Description),
		Status:         CustomProviderActive,
		AllowedActions: normalizeAllowed(input.AllowedActions, []string{"custom"}),
		AllowedMetrics: normalizeAllowed(input.AllowedMetrics, []string{"count"}),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := service.sink.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{
		Environments: []activity.Environment{customProviderEnvironment(provider)},
	}); err != nil {
		return CustomProvider{}, fmt.Errorf("publish custom provider environment: %w", err)
	}
	record := CustomProviderRecord{
		Provider:              provider,
		EncryptedIngestSecret: ingestSecretHash,
	}
	var saveErr error
	if atomicStore, ok := service.store.(AtomicCustomProviderStore); ok {
		saveErr = atomicStore.CreateCustomProvider(ctx, record)
	} else {
		saveErr = service.store.SaveCustomProvider(ctx, record)
	}
	if saveErr != nil {
		return CustomProvider{}, saveErr
	}
	return cloneCustomProvider(provider), nil
}

type UpdateCustomProviderInput struct {
	ID             string
	Name           string
	Description    string
	Status         CustomProviderStatus
	AllowedActions []string
	AllowedMetrics []string
}

func (service *CustomProviderService) Update(ctx context.Context, input UpdateCustomProviderInput) (CustomProvider, error) {
	if input.ID == "" {
		return CustomProvider{}, ErrInvalidProvider
	}
	if err := validateCustomProviderConfiguration(input.Name, input.Description, input.AllowedActions, input.AllowedMetrics); err != nil {
		return CustomProvider{}, err
	}
	if err := validateCustomProviderStatus(input.Status); err != nil {
		return CustomProvider{}, err
	}
	record, err := service.store.GetCustomProvider(ctx, input.ID)
	if err != nil {
		return CustomProvider{}, err
	}
	record.Provider.Name = strings.TrimSpace(input.Name)
	record.Provider.Description = strings.TrimSpace(input.Description)
	record.Provider.Status = input.Status
	record.Provider.AllowedActions = normalizeAllowed(input.AllowedActions, []string{"custom"})
	record.Provider.AllowedMetrics = normalizeAllowed(input.AllowedMetrics, []string{"count"})
	record.Provider.UpdatedAt = service.clock.Now()
	if err := service.sink.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{
		Environments: []activity.Environment{customProviderEnvironment(record.Provider)},
	}); err != nil {
		return CustomProvider{}, fmt.Errorf("publish custom provider environment: %w", err)
	}
	if err := service.saveExistingCustomProvider(ctx, record); err != nil {
		return CustomProvider{}, err
	}
	return cloneCustomProvider(record.Provider), nil
}

func (service *CustomProviderService) RotateIngestSecret(ctx context.Context, id string, ingestSecret string) error {
	if id == "" {
		return ErrInvalidProvider
	}
	if ingestSecret == "" {
		return ErrEmptyIngestSecret
	}
	if utf8.RuneCountInString(ingestSecret) < minCustomIngestSecretLength {
		return ErrIngestSecretTooShort
	}
	record, err := service.store.GetCustomProvider(ctx, id)
	if err != nil {
		return err
	}
	record.EncryptedIngestSecret = hashIngestSecret(ingestSecret)
	record.Provider.UpdatedAt = service.clock.Now()
	return service.saveExistingCustomProvider(ctx, record)
}

func (service *CustomProviderService) Get(ctx context.Context, id string) (CustomProvider, error) {
	record, err := service.store.GetCustomProvider(ctx, id)
	if err != nil {
		return CustomProvider{}, err
	}
	return cloneCustomProvider(record.Provider), nil
}

func (service *CustomProviderService) List(ctx context.Context, subjectID string) ([]CustomProvider, error) {
	if subjectID == "" {
		return nil, ErrEmptySubjectID
	}
	records, err := service.store.ListCustomProviders(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	providers := make([]CustomProvider, len(records))
	for index := range records {
		providers[index] = cloneCustomProvider(records[index].Provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Slug < providers[j].Slug })
	return providers, nil
}

func (service *CustomProviderService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return ErrInvalidProvider
	}
	record, err := service.store.GetCustomProvider(ctx, id)
	if err != nil {
		return err
	}
	if atomicStore, ok := service.store.(AtomicCustomProviderStore); ok {
		return atomicStore.DeleteCustomProviderAggregate(ctx, id)
	}
	replacer, ok := service.sink.(interface {
		ReplaceFacts(context.Context, activity.LoadFactsInput, []activity.EnvironmentID, []activity.Fact) error
	})
	if !ok {
		return ErrPurgeUnsupported
	}
	if err := replacer.ReplaceFacts(ctx, activity.LoadFactsInput{
		Subject: activity.SubjectID(record.Provider.SubjectID),
	}, []activity.EnvironmentID{activity.EnvironmentID(record.Provider.EnvironmentID)}, nil); err != nil {
		return fmt.Errorf("purge custom provider activity facts: %w", err)
	}
	return service.store.DeleteCustomProvider(ctx, id)
}

func (service *CustomProviderService) saveExistingCustomProvider(ctx context.Context, record CustomProviderRecord) error {
	if atomicStore, ok := service.store.(AtomicCustomProviderStore); ok {
		return atomicStore.UpdateCustomProvider(ctx, record)
	}
	return service.store.SaveCustomProvider(ctx, record)
}

type IngestCustomActivitiesInput struct {
	ProviderID     string
	IngestSecret   string
	IdempotencyKey string
	Activities     []CustomActivity
}

type IngestRejection struct {
	EventID string
	Code    string
	Detail  string
}

type IngestCustomActivitiesResult struct {
	Accepted   int
	Duplicate  int
	Rejected   int
	Rejections []IngestRejection
}

func (service *CustomProviderService) Ingest(ctx context.Context, input IngestCustomActivitiesInput) (result IngestCustomActivitiesResult, err error) {
	if input.ProviderID == "" {
		return IngestCustomActivitiesResult{}, ErrInvalidProvider
	}
	if input.IngestSecret == "" {
		return IngestCustomActivitiesResult{}, ErrUnauthorized
	}
	if len(input.Activities) == 0 {
		return IngestCustomActivitiesResult{}, ErrEmptyActivities
	}
	if len(input.Activities) > MaxCustomIngestBatch {
		return IngestCustomActivitiesResult{}, ErrTooManyActivities
	}
	if input.IdempotencyKey != "" {
		length := utf8.RuneCountInString(input.IdempotencyKey)
		if length < minIdempotencyKeyLength || length > maxIdempotencyKeyLength {
			return IngestCustomActivitiesResult{}, ErrInvalidIdempotencyKey
		}
	}
	record, err := service.store.GetCustomProvider(ctx, input.ProviderID)
	if err != nil {
		return IngestCustomActivitiesResult{}, err
	}
	if record.Provider.Status != CustomProviderActive {
		return IngestCustomActivitiesResult{}, ErrProviderDisabled
	}
	if err := authenticateIngestSecret(record.EncryptedIngestSecret, input.IngestSecret); err != nil {
		return IngestCustomActivitiesResult{}, err
	}

	now := service.clock.Now()
	fingerprint, err := customIngestFingerprint(input.Activities)
	if err != nil {
		return IngestCustomActivitiesResult{}, fmt.Errorf("fingerprint custom ingest request: %w", err)
	}
	if atomicStore, ok := service.store.(AtomicCustomIngestStore); ok {
		transactionalSink, ok := service.sink.(TransactionalActivitySink)
		if !ok {
			return IngestCustomActivitiesResult{}, ErrAtomicIngestUnavailable
		}
		var committed IngestCustomActivitiesResult
		err := atomicStore.RunAuthenticatedIngest(ctx, input.ProviderID, record.EncryptedIngestSecret,
			func(txctx context.Context, current CustomProviderRecord) error {
				var attemptErr error
				committed, attemptErr = service.ingestPrepared(txctx, input, current, now, fingerprint,
					transactionalSink.SaveFactsInCurrentTransaction, true)
				return attemptErr
			})
		if err != nil {
			return IngestCustomActivitiesResult{}, err
		}
		return committed, nil
	}
	return service.ingestPrepared(ctx, input, record, now, fingerprint, service.sink.SaveFacts, false)
}

func (service *CustomProviderService) ingestPrepared(
	ctx context.Context,
	input IngestCustomActivitiesInput,
	record CustomProviderRecord,
	now time.Time,
	fingerprint [sha256.Size]byte,
	saveFacts func(context.Context, activity.SaveFactsInput) error,
	atomic bool,
) (result IngestCustomActivitiesResult, err error) {
	var durableIdempotency AtomicIngestIdempotencyStore
	var idempotencyKeyHash [sha256.Size]byte
	var reservationToken string
	reservationAcquired := false
	if input.IdempotencyKey != "" {
		idempotencyKeyHash = sha256.Sum256([]byte(input.IdempotencyKey))
		if store, ok := service.store.(AtomicIngestIdempotencyStore); ok {
			durableIdempotency = store
			reservationToken, err = service.ids.NewID()
			if err != nil {
				return IngestCustomActivitiesResult{}, fmt.Errorf("generate ingest reservation token: %w", err)
			}
			created, err := store.CreateIngestIdempotencyKey(ctx, IngestIdempotencyRecord{
				ProviderID: input.ProviderID, KeyHash: idempotencyKeyHash[:], RequestHash: fingerprint[:],
				ReservationToken: reservationToken,
				ResponseStatus:   0, ResponseBody: json.RawMessage(`{}`), CreatedAt: now,
				ExpiresAt: now.Add(customIdempotencyRetention),
			})
			if err != nil {
				return IngestCustomActivitiesResult{}, err
			}
			if !created {
				previous, found, err := store.GetIngestIdempotencyKey(ctx, input.ProviderID, idempotencyKeyHash[:])
				if err != nil {
					return IngestCustomActivitiesResult{}, err
				}
				if !found {
					return IngestCustomActivitiesResult{}, idempotencyInProgressError()
				}
				return replayIngestIdempotency(previous, fingerprint)
			}
			reservationAcquired = true
			defer func() {
				if !reservationAcquired {
					return
				}
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if releaseErr := durableIdempotency.ReleaseIngestIdempotencyKey(
					cleanupCtx, input.ProviderID, idempotencyKeyHash[:], fingerprint[:], reservationToken,
				); releaseErr != nil {
					err = errors.Join(err, fmt.Errorf("release ingest idempotency reservation: %w", releaseErr))
				}
			}()
		} else {
			service.idempotencyMu.Lock()
			defer service.idempotencyMu.Unlock()
			service.expireIdempotencyRecords(now)
			key := customIngestIdempotencyKey(input.ProviderID, input.IdempotencyKey)
			if previous, ok := service.idempotency[key]; ok {
				if subtle.ConstantTimeCompare(previous.fingerprint[:], fingerprint[:]) != 1 {
					return IngestCustomActivitiesResult{}, fmt.Errorf("%w: %w", ErrConflict, ErrIdempotencyConflict)
				}
				return cloneCustomIngestResult(previous.result), nil
			}
		}
	}

	activities := make([]IngestedActivity, 0, len(input.Activities))
	result = IngestCustomActivitiesResult{Rejections: []IngestRejection{}}
	for index, item := range input.Activities {
		if err := validateCustomActivity(record.Provider, item, now); err != nil {
			result.Rejections = append(result.Rejections, IngestRejection{
				EventID: item.ExternalID,
				Code:    customIngestRejectionCode(err),
				Detail:  fmt.Sprintf("activity %d: %v", index, err),
			})
			continue
		}
		activities = append(activities, IngestedActivity{
			ProviderID: record.Provider.ID,
			SubjectID:  record.Provider.SubjectID,
			ExternalID: item.ExternalID,
			Date:       item.Date,
			Action:     item.Action,
			Metric:     item.Metric,
			Value:      item.Value,
			Metadata:   copyStringMap(item.Metadata),
			ObservedAt: copyTimePointer(item.ObservedAt),
			IngestedAt: now,
		})
	}
	result.Rejected = len(result.Rejections)

	accepted := []IngestedActivity{}
	if len(activities) > 0 {
		accepted, err = service.store.SaveIngestedActivities(ctx, activities)
		if err != nil {
			return IngestCustomActivitiesResult{}, err
		}
	}
	result.Accepted = len(accepted)
	result.Duplicate = len(activities) - len(accepted)

	// Project the canonical ledger entries for all valid requested event IDs,
	// including duplicates. If a previous projection failed, an idempotent retry
	// therefore repairs the timeline without accepting the event twice.
	if len(activities) > 0 {
		canonical, err := service.canonicalRequestedActivities(ctx, record.Provider.ID, activities)
		if err != nil {
			return IngestCustomActivitiesResult{}, err
		}
		facts := make([]activity.Fact, len(canonical))
		for index, item := range canonical {
			facts[index] = customActivityFact(record.Provider, item)
		}
		if err := saveFacts(ctx, activity.SaveFactsInput{
			Subject: activity.SubjectID(record.Provider.SubjectID),
			Facts:   facts,
		}); err != nil {
			return IngestCustomActivitiesResult{}, fmt.Errorf("publish custom activity facts: %w", err)
		}
	}
	if input.IdempotencyKey != "" {
		if durableIdempotency != nil {
			body, err := json.Marshal(result)
			if err != nil {
				return IngestCustomActivitiesResult{}, fmt.Errorf("encode idempotent ingest response: %w", err)
			}
			completed, err := durableIdempotency.CompleteIngestIdempotencyKey(ctx, IngestIdempotencyRecord{
				ProviderID: input.ProviderID, KeyHash: idempotencyKeyHash[:], RequestHash: fingerprint[:],
				ReservationToken: reservationToken,
				ResponseStatus:   202, ResponseBody: body, CreatedAt: now, ExpiresAt: now.Add(customIdempotencyRetention),
			})
			if err != nil {
				return IngestCustomActivitiesResult{}, err
			}
			if !completed {
				if atomic {
					return IngestCustomActivitiesResult{}, idempotencyInProgressError()
				}
				previous, found, err := durableIdempotency.GetIngestIdempotencyKey(ctx, input.ProviderID, idempotencyKeyHash[:])
				if err != nil {
					return IngestCustomActivitiesResult{}, fmt.Errorf("resolve ingest idempotency completion: %w", err)
				}
				if !found {
					return IngestCustomActivitiesResult{}, idempotencyInProgressError()
				}
				result, err = replayIngestIdempotency(previous, fingerprint)
				if err != nil {
					return IngestCustomActivitiesResult{}, err
				}
			}
			reservationAcquired = false
		} else {
			service.idempotency[customIngestIdempotencyKey(input.ProviderID, input.IdempotencyKey)] = customIngestIdempotencyRecord{
				fingerprint: fingerprint,
				result:      cloneCustomIngestResult(result),
				expiresAt:   now.Add(customIdempotencyRetention),
			}
		}
	}
	return result, nil
}

func replayIngestIdempotency(record IngestIdempotencyRecord, fingerprint [sha256.Size]byte) (IngestCustomActivitiesResult, error) {
	if subtle.ConstantTimeCompare(record.RequestHash, fingerprint[:]) != 1 {
		return IngestCustomActivitiesResult{}, fmt.Errorf("%w: %w", ErrConflict, ErrIdempotencyConflict)
	}
	if record.ResponseStatus == 0 {
		return IngestCustomActivitiesResult{}, idempotencyInProgressError()
	}
	if record.ResponseStatus != 202 {
		return IngestCustomActivitiesResult{}, fmt.Errorf("decode idempotent ingest response: unexpected status %d", record.ResponseStatus)
	}
	var result IngestCustomActivitiesResult
	if err := json.Unmarshal(record.ResponseBody, &result); err != nil {
		return IngestCustomActivitiesResult{}, fmt.Errorf("decode idempotent ingest response: %w", err)
	}
	return cloneCustomIngestResult(result), nil
}

func idempotencyInProgressError() error {
	return fmt.Errorf("%w: %w", ErrConflict, ErrIdempotencyInProgress)
}

func validateCustomProviderInput(input CreateCustomProviderInput) error {
	if input.SubjectID == "" {
		return ErrEmptySubjectID
	}
	if input.Slug == "" {
		return ErrEmptyProviderSlug
	}
	if utf8.RuneCountInString(input.Slug) > maxCustomProviderSlugLength || !customProviderSlugPattern.MatchString(input.Slug) {
		return ErrInvalidProviderSlug
	}
	if err := validateCustomProviderConfiguration(input.Name, input.Description, input.AllowedActions, input.AllowedMetrics); err != nil {
		return err
	}
	if input.IngestSecret == "" {
		return ErrEmptyIngestSecret
	}
	if utf8.RuneCountInString(input.IngestSecret) < minCustomIngestSecretLength {
		return ErrIngestSecretTooShort
	}
	return nil
}

func validateCustomProviderConfiguration(name, description string, allowedActions, allowedMetrics []string) error {
	if strings.TrimSpace(name) == "" {
		return ErrEmptyProviderName
	}
	if utf8.RuneCountInString(name) > maxCustomProviderNameLength {
		return ErrProviderNameTooLong
	}
	if utf8.RuneCountInString(description) > maxCustomProviderDescription {
		return ErrProviderDescriptionTooLong
	}
	if err := validateAllowedValues(allowedActions, maxCustomAllowedValues, ErrTooManyAllowedActions, ErrInvalidAllowedAction, ErrDuplicateAllowedAction); err != nil {
		return err
	}
	return validateAllowedValues(allowedMetrics, maxCustomAllowedValues, ErrTooManyAllowedMetrics, ErrInvalidAllowedMetric, ErrDuplicateAllowedMetric)
}

func validateAllowedValues(values []string, maximum int, tooMany, invalid, duplicate error) error {
	if len(values) > maximum {
		return tooMany
	}
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		length := utf8.RuneCountInString(raw)
		if value == "" || length > maxCustomAllowedValueLength {
			return invalid
		}
		if _, exists := seen[value]; exists {
			return duplicate
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateCustomProviderStatus(status CustomProviderStatus) error {
	if status != CustomProviderActive && status != CustomProviderDisabled {
		return ErrInvalidConnectionStatus
	}
	return nil
}

func validateCustomActivity(provider CustomProvider, item CustomActivity, now time.Time) error {
	if item.ExternalID == "" {
		return ErrEmptyExternalID
	}
	if utf8.RuneCountInString(item.ExternalID) > maxCustomExternalIDLength {
		return ErrExternalIDTooLong
	}
	if item.Date == "" {
		return ErrEmptyActivityDate
	}
	parsed, err := time.Parse("2006-01-02", item.Date)
	if err != nil || parsed.Format("2006-01-02") != item.Date {
		return ErrInvalidActivityDate
	}
	today := now.UTC()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if parsed.Before(today.AddDate(-maxCustomActivityBackfillYears, 0, 0)) || parsed.After(today.AddDate(0, 0, maxCustomActivityFutureDays)) {
		return ErrActivityDateOutOfRange
	}
	if item.Action == "" {
		return ErrEmptyAction
	}
	if utf8.RuneCountInString(item.Action) > maxCustomAllowedValueLength {
		return ErrActionTooLong
	}
	if !contains(provider.AllowedActions, item.Action) {
		return ErrActionNotAllowed
	}
	if item.Metric == "" {
		return ErrEmptyMetric
	}
	if utf8.RuneCountInString(item.Metric) > maxCustomAllowedValueLength {
		return ErrMetricTooLong
	}
	if !contains(provider.AllowedMetrics, item.Metric) {
		return ErrMetricNotAllowed
	}
	if item.Value < 0 {
		return ErrNegativeMetricValue
	}
	if item.Value > MaxCustomMetricValue {
		return ErrMetricValueTooLarge
	}
	if len(item.Metadata) > maxCustomMetadataProperties {
		return ErrTooManyMetadataProperties
	}
	for key, value := range item.Metadata {
		if utf8.RuneCountInString(key) < 1 || utf8.RuneCountInString(key) > 128 || !validMetadataKey(key) {
			return ErrInvalidMetadataKey
		}
		if utf8.RuneCountInString(value) > maxCustomMetadataValueLength {
			return ErrMetadataValueTooLong
		}
	}
	return nil
}

func validMetadataKey(value string) bool {
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case strings.ContainsRune("._:-", character):
		default:
			return false
		}
	}
	return true
}

func authenticateIngestSecret(expectedDigest []byte, candidate string) error {
	candidateBytes := []byte(candidate)
	defer clearBytes(candidateBytes)
	candidateDigest := sha256.Sum256(candidateBytes)
	defer clearBytes(candidateDigest[:])
	if subtle.ConstantTimeCompare(expectedDigest, candidateDigest[:]) != 1 {
		return ErrUnauthorized
	}
	return nil
}

func hashIngestSecret(plaintext string) []byte {
	value := []byte(plaintext)
	defer clearBytes(value)
	digest := sha256.Sum256(value)
	return append([]byte(nil), digest[:]...)
}

func normalizeAllowed(values []string, defaults []string) []string {
	if values == nil {
		return copyStrings(defaults)
	}
	values = normalizeStrings(values)
	return values
}

func customProviderEnvironment(provider CustomProvider) activity.Environment {
	owner := activity.SubjectID(provider.SubjectID)
	return activity.Environment{
		ID:           activity.EnvironmentID(provider.EnvironmentID),
		Key:          provider.Slug,
		Name:         provider.Name,
		Scope:        activity.EnvironmentScopeSubject,
		OwnerSubject: &owner,
		Metadata: map[string]string{
			"category":           "custom",
			"custom_provider_id": provider.ID,
		},
	}
}

func customActivityFact(provider CustomProvider, item IngestedActivity) activity.Fact {
	metadata := copyStringMap(item.Metadata)
	if metadata == nil {
		metadata = make(map[string]string, 3)
	}
	metadata["custom_provider_id"] = provider.ID
	metadata["provider_event_id"] = item.ExternalID
	if item.ObservedAt != nil {
		metadata["observed_at"] = item.ObservedAt.Format(time.RFC3339Nano)
	}
	return activity.Fact{
		Subject:       activity.SubjectID(item.SubjectID),
		Date:          activity.Date(item.Date),
		EnvironmentID: activity.EnvironmentID(provider.EnvironmentID),
		Action:        activity.ActionID(item.Action),
		Metric:        activity.Metric{Name: activity.MetricName(item.Metric), Value: item.Value},
		Metadata:      metadata,
	}
}

func (service *CustomProviderService) canonicalRequestedActivities(ctx context.Context, providerID string, requested []IngestedActivity) ([]IngestedActivity, error) {
	ledger, err := service.store.ListIngestedActivities(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("load canonical custom activities: %w", err)
	}
	byExternalID := make(map[string]IngestedActivity, len(ledger))
	for _, item := range ledger {
		byExternalID[item.ExternalID] = item
	}
	seen := make(map[string]struct{}, len(requested))
	canonical := make([]IngestedActivity, 0, len(requested))
	for _, item := range requested {
		if _, duplicate := seen[item.ExternalID]; duplicate {
			continue
		}
		stored, ok := byExternalID[item.ExternalID]
		if !ok {
			return nil, fmt.Errorf("custom ingest ledger omitted event %q", item.ExternalID)
		}
		seen[item.ExternalID] = struct{}{}
		canonical = append(canonical, stored)
	}
	return canonical, nil
}

func customIngestFingerprint(activities []CustomActivity) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(activities)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func customIngestIdempotencyKey(providerID, requestKey string) string {
	return providerID + "\x00" + requestKey
}

func (service *CustomProviderService) expireIdempotencyRecords(now time.Time) {
	for key, record := range service.idempotency {
		if now.After(record.expiresAt) {
			delete(service.idempotency, key)
		}
	}
}

func cloneCustomIngestResult(result IngestCustomActivitiesResult) IngestCustomActivitiesResult {
	result.Rejections = append([]IngestRejection(nil), result.Rejections...)
	if result.Rejections == nil {
		result.Rejections = []IngestRejection{}
	}
	return result
}

func customIngestRejectionCode(err error) string {
	switch {
	case errors.Is(err, ErrEmptyExternalID):
		return "event_id_required"
	case errors.Is(err, ErrExternalIDTooLong):
		return "event_id_too_long"
	case errors.Is(err, ErrEmptyActivityDate):
		return "date_required"
	case errors.Is(err, ErrInvalidActivityDate):
		return "invalid_date"
	case errors.Is(err, ErrActivityDateOutOfRange):
		return "date_out_of_range"
	case errors.Is(err, ErrEmptyAction):
		return "action_required"
	case errors.Is(err, ErrActionTooLong):
		return "action_too_long"
	case errors.Is(err, ErrActionNotAllowed):
		return "action_not_allowed"
	case errors.Is(err, ErrEmptyMetric):
		return "metric_required"
	case errors.Is(err, ErrMetricTooLong):
		return "metric_too_long"
	case errors.Is(err, ErrMetricNotAllowed):
		return "metric_not_allowed"
	case errors.Is(err, ErrNegativeMetricValue):
		return "negative_metric_value"
	case errors.Is(err, ErrMetricValueTooLarge):
		return "metric_value_too_large"
	case errors.Is(err, ErrTooManyMetadataProperties):
		return "too_many_metadata_properties"
	case errors.Is(err, ErrMetadataValueTooLong):
		return "metadata_value_too_long"
	case errors.Is(err, ErrInvalidMetadataKey):
		return "invalid_metadata_key"
	default:
		return "invalid_event"
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func cloneCustomProvider(provider CustomProvider) CustomProvider {
	provider.AllowedActions = copyStrings(provider.AllowedActions)
	provider.AllowedMetrics = copyStrings(provider.AllowedMetrics)
	return provider
}
