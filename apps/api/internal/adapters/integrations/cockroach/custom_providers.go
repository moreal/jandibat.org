package cockroach

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func (s *Store) SaveCustomProvider(ctx context.Context, record integrations.CustomProviderRecord) error {
	if s.pool == nil {
		return ErrNilDB
	}
	return s.saveCustomProviderPGX(ctx, record, providerWriteUpsert)
}

type providerWriteMode uint8

const (
	providerWriteCreate providerWriteMode = iota
	providerWriteUpdate
	providerWriteUpsert
)

func (s *Store) CreateCustomProvider(ctx context.Context, record integrations.CustomProviderRecord) error {
	if s.pool == nil {
		return ErrNilDB
	}
	return s.saveCustomProviderPGX(ctx, record, providerWriteCreate)
}

func (s *Store) UpdateCustomProvider(ctx context.Context, record integrations.CustomProviderRecord) error {
	if s.pool == nil {
		return ErrNilDB
	}
	return s.saveCustomProviderPGX(ctx, record, providerWriteUpdate)
}

func (s *Store) saveCustomProviderPGX(ctx context.Context, record integrations.CustomProviderRecord, mode providerWriteMode) error {
	provider := record.Provider
	if provider.ID == "" {
		return integrations.ErrInvalidProvider
	}
	id, err := uuid.Parse(provider.ID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	actions, err := json.Marshal(provider.AllowedActions)
	if err != nil {
		return fmt.Errorf("encode allowed actions: %w", err)
	}
	metrics, err := json.Marshal(provider.AllowedMetrics)
	if err != nil {
		return fmt.Errorf("encode allowed metrics: %w", err)
	}
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		var count int64
		var err error
		switch mode {
		case providerWriteCreate:
			count, err = generated.InsertCustomProvider(txctx, tx, id, provider.SubjectID,
				provider.EnvironmentID, provider.Slug, provider.Name, provider.Description,
				string(provider.Status), actions, metrics, provider.CreatedAt, provider.UpdatedAt)
		case providerWriteUpdate:
			count, err = generated.UpdateCustomProvider(txctx, tx, id, provider.SubjectID,
				provider.EnvironmentID, provider.Slug, provider.Name, provider.Description,
				string(provider.Status), actions, metrics, provider.CreatedAt, provider.UpdatedAt)
		case providerWriteUpsert:
			count, err = generated.UpsertCustomProvider(txctx, tx, id, provider.SubjectID,
				provider.EnvironmentID, provider.Slug, provider.Name, provider.Description,
				string(provider.Status), actions, metrics, provider.CreatedAt, provider.UpdatedAt)
		default:
			return integrations.ErrInvalidProvider
		}
		if err != nil {
			return persistenceError(err, integrations.ErrDuplicateProviderSlug)
		}
		if count == 0 {
			if mode != providerWriteUpdate {
				return notFound("owned subject", provider.SubjectID)
			}
			return notFound("custom provider", provider.ID)
		}
		return persistenceError(generated.UpsertCustomProviderSecret(txctx, tx, id, record.EncryptedIngestSecret), integrations.ErrDuplicateProviderSlug)
	})
}

func (s *Store) DeleteCustomProviderAggregate(ctx context.Context, id string) error {
	if s.pool == nil {
		return ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		row, err := generated.LockCustomProviderAggregate(txctx, tx, parsed)
		if err != nil {
			return fmt.Errorf("delete custom provider aggregate: load: %w", err)
		}
		if row == nil {
			return notFound("custom provider", id)
		}
		if err := generated.DeleteCustomProviderRefreshCache(txctx, tx, row.SubjectId, row.EnvironmentId); err != nil {
			return fmt.Errorf("delete custom provider aggregate: refresh cache: %w", err)
		}
		if err := generated.DeleteCustomProviderFacts(txctx, tx, parsed, row.SubjectId, row.EnvironmentId); err != nil {
			return fmt.Errorf("delete custom provider aggregate: facts: %w", err)
		}
		if _, err := generated.DeleteCustomProviderById(txctx, tx, parsed); err != nil {
			return fmt.Errorf("delete custom provider aggregate: provider: %w", err)
		}
		// API cannot DELETE environments; maintenance reaps the sanitized tombstone.
		return nil
	})
}

func (s *Store) GetCustomProvider(ctx context.Context, id string) (integrations.CustomProviderRecord, error) {
	if s.pool == nil {
		return integrations.CustomProviderRecord{}, ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.CustomProviderRecord{}, integrations.ErrInvalidIdentifier
	}
	row, err := generated.GetCustomProviderById(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed)
	if err != nil {
		return integrations.CustomProviderRecord{}, fmt.Errorf("get custom provider: %w", err)
	}
	if row == nil {
		return integrations.CustomProviderRecord{}, notFound("custom provider", id)
	}
	return customProviderFromGenerated(*row)
}

func (s *Store) ListCustomProviders(ctx context.Context, subjectID string) ([]integrations.CustomProviderRecord, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	rows, err := generated.ListCustomProviders(ctx, appdb.PGXExecutorFor(ctx, s.pool), subjectID)
	if err != nil {
		return nil, fmt.Errorf("list custom providers: %w", err)
	}
	records := make([]integrations.CustomProviderRecord, 0, len(rows))
	for _, row := range rows {
		record, err := customProviderFromGenerated(generated.GetCustomProviderByIdRow(row))
		if err != nil {
			return nil, fmt.Errorf("list custom providers: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

func customProviderFromGenerated(row generated.GetCustomProviderByIdRow) (integrations.CustomProviderRecord, error) {
	record := integrations.CustomProviderRecord{Provider: integrations.CustomProvider{
		ID: row.Id, SubjectID: row.SubjectId, EnvironmentID: row.EnvironmentId,
		Slug: row.Slug, Name: row.Name, Description: row.Description,
		Status: integrations.CustomProviderStatus(row.Status), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}}
	var configuration customProviderConfiguration
	if err := json.Unmarshal(row.Configuration, &configuration); err != nil {
		return integrations.CustomProviderRecord{}, fmt.Errorf("decode custom provider configuration: %w", err)
	}
	record.Provider.AllowedActions = nonNilStrings(configuration.AllowedActions)
	record.Provider.AllowedMetrics = nonNilStrings(configuration.AllowedMetrics)
	record.EncryptedIngestSecret = append([]byte(nil), row.IngestTokenHash...)
	return record, nil
}

func (s *Store) DeleteCustomProvider(ctx context.Context, id string) error {
	if s.pool == nil {
		return ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	count, err := generated.DeleteCustomProviderById(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed)
	if err != nil {
		return fmt.Errorf("delete custom provider: %w", err)
	}
	if count == 0 {
		return notFound("custom provider", id)
	}
	return nil
}

type customProviderConfiguration struct {
	AllowedActions []string `json:"allowed_actions"`
	AllowedMetrics []string `json:"allowed_metrics"`
}

// SaveIngestedActivities atomically accepts the first event for each
// (provider_id, external_id) pair. The returned values are scanned from the
// inserted rows so callers receive the database's canonical representation.
func (s *Store) SaveIngestedActivities(ctx context.Context, activities []integrations.IngestedActivity) ([]integrations.IngestedActivity, error) {
	if len(activities) == 0 {
		return []integrations.IngestedActivity{}, nil
	}
	if s.pool == nil {
		return nil, ErrNilDB
	}
	providerID := activities[0].ProviderID
	for _, item := range activities {
		if item.ProviderID != providerID {
			return nil, fmt.Errorf("save ingested activities: mixed provider IDs")
		}
	}
	id, err := uuid.Parse(providerID)
	if err != nil {
		return nil, integrations.ErrInvalidIdentifier
	}
	var accepted []integrations.IngestedActivity
	err = appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		locked, err := generated.LockProviderForIngest(txctx, tx, id)
		if err != nil {
			return fmt.Errorf("save ingested activities: get provider: %w", err)
		}
		if locked == nil {
			return notFound("custom provider", providerID)
		}
		if locked.Status != string(integrations.CustomProviderActive) {
			return integrations.ErrProviderDisabled
		}
		accepted = make([]integrations.IngestedActivity, 0, len(activities))
		for _, item := range activities {
			metadata := item.Metadata
			if metadata == nil {
				metadata = map[string]string{}
			}
			encoded, err := json.Marshal(metadata)
			if err != nil {
				return fmt.Errorf("save ingested activities: encode metadata: %w", err)
			}
			row, err := generated.InsertCustomActivity(txctx, tx, id, item.ExternalID,
				item.Date, item.Action, item.Metric, int64(item.Value), encoded,
				optionalTimeText(item.ObservedAt), item.IngestedAt)
			if err != nil {
				return fmt.Errorf("save ingested activities: insert event %q: %w", item.ExternalID, err)
			}
			if row == nil {
				continue
			}
			stored, err := ingestedActivityFromGenerated(*row)
			if err != nil {
				return err
			}
			accepted = append(accepted, stored)
		}
		return generated.TouchCustomProviderIngested(txctx, tx, id)
	})
	return accepted, err
}

func ingestedActivityFromGenerated(row generated.InsertCustomActivityRow) (integrations.IngestedActivity, error) {
	item := integrations.IngestedActivity{
		ProviderID: row.ProviderId, SubjectID: row.SubjectId, ExternalID: row.ExternalId,
		Date: row.ActivityDate, Action: row.Action, Metric: row.MetricName,
		Value: int(row.MetricValue), IngestedAt: row.IngestedAt,
	}
	if int64(item.Value) != row.MetricValue {
		return integrations.IngestedActivity{}, integrations.ErrInvalidProvider
	}
	if err := json.Unmarshal(row.Metadata, &item.Metadata); err != nil {
		return integrations.IngestedActivity{}, fmt.Errorf("decode custom activity metadata: %w", err)
	}
	item.Metadata = cloneMetadata(item.Metadata)
	if row.ObservedAt != nil {
		value := *row.ObservedAt
		item.ObservedAt = &value
	}
	return item, nil
}

func (s *Store) ListIngestedActivities(ctx context.Context, providerID string) ([]integrations.IngestedActivity, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	id, err := uuid.Parse(providerID)
	if err != nil {
		return nil, integrations.ErrInvalidIdentifier
	}
	executor := appdb.PGXExecutorFor(ctx, s.pool)
	exists, err := generated.GetCustomProviderExists(ctx, executor, id)
	if err != nil {
		return nil, fmt.Errorf("list ingested activities: get provider: %w", err)
	}
	if !exists.Exists {
		return nil, notFound("custom provider", providerID)
	}
	rows, err := generated.ListCustomActivities(ctx, executor, id)
	if err != nil {
		return nil, fmt.Errorf("list ingested activities: query: %w", err)
	}
	items := make([]integrations.IngestedActivity, 0, len(rows))
	for _, row := range rows {
		item, err := ingestedActivityFromGenerated(generated.InsertCustomActivityRow(row))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// CreateIngestIdempotencyKey atomically reserves a provider-scoped key before
// any event or fact mutation. Callers use ResponseStatus zero and an empty JSON
// body for a pending reservation. It returns false while an unexpired record
// already owns the key.
func (s *Store) CreateIngestIdempotencyKey(ctx context.Context, record integrations.IngestIdempotencyRecord) (bool, error) {
	if s.pool == nil {
		return false, ErrNilDB
	}
	id, err := uuid.Parse(record.ProviderID)
	if err != nil {
		return false, integrations.ErrInvalidIdentifier
	}
	reservationToken, err := uuid.Parse(record.ReservationToken)
	if err != nil {
		return false, integrations.ErrInvalidIdentifier
	}
	if record.ResponseStatus < 0 || record.ResponseStatus > math.MaxInt32 {
		return false, integrations.ErrInvalidProvider
	}
	count, err := generated.ReserveIngestKey(ctx, appdb.PGXExecutorFor(ctx, s.pool), id,
		record.KeyHash, record.RequestHash, int32(record.ResponseStatus), record.ResponseBody,
		record.CreatedAt, record.ExpiresAt, reservationToken)
	if err != nil {
		return false, fmt.Errorf("create ingest idempotency key: %w", err)
	}
	return count == 1, nil
}

// CompleteIngestIdempotencyKey transitions this request's pending reservation
// to its replayable response. The request hash, reservation token, and pending
// status form a CAS, including after an expired key is re-reserved.
func (s *Store) CompleteIngestIdempotencyKey(ctx context.Context, record integrations.IngestIdempotencyRecord) (bool, error) {
	if s.pool == nil {
		return false, ErrNilDB
	}
	id, err := uuid.Parse(record.ProviderID)
	if err != nil {
		return false, integrations.ErrInvalidIdentifier
	}
	reservationToken, err := uuid.Parse(record.ReservationToken)
	if err != nil {
		return false, integrations.ErrInvalidIdentifier
	}
	if record.ResponseStatus < 0 || record.ResponseStatus > math.MaxInt32 {
		return false, integrations.ErrInvalidProvider
	}
	count, err := generated.CompleteIngestKey(ctx, appdb.PGXExecutorFor(ctx, s.pool), id,
		record.KeyHash, record.RequestHash, reservationToken, int32(record.ResponseStatus), record.ResponseBody,
		record.ExpiresAt)
	if err != nil {
		return false, fmt.Errorf("complete ingest idempotency key: %w", err)
	}
	return count == 1, nil
}

// ReleaseIngestIdempotencyKey removes only this request's pending reservation,
// allowing a retry after validation, event persistence, or projection fails.
// Completed responses and reservations for another request are untouched.
func (s *Store) ReleaseIngestIdempotencyKey(ctx context.Context, providerID string, keyHash, requestHash []byte, reservationToken string) error {
	if s.pool == nil {
		return ErrNilDB
	}
	id, err := uuid.Parse(providerID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	token, err := uuid.Parse(reservationToken)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	if err := generated.ReleaseIngestKey(ctx, appdb.PGXExecutorFor(ctx, s.pool), id, keyHash, requestHash, token); err != nil {
		return fmt.Errorf("release ingest idempotency key: %w", err)
	}
	return nil
}

// GetIngestIdempotencyKey loads an unexpired provider-scoped key. Expired or
// unknown keys return found=false and may be reclaimed after retention cleanup.
func (s *Store) GetIngestIdempotencyKey(ctx context.Context, providerID string, keyHash []byte) (record integrations.IngestIdempotencyRecord, found bool, err error) {
	if s.pool == nil {
		return integrations.IngestIdempotencyRecord{}, false, ErrNilDB
	}
	id, err := uuid.Parse(providerID)
	if err != nil {
		return integrations.IngestIdempotencyRecord{}, false, integrations.ErrInvalidIdentifier
	}
	row, err := generated.GetActiveIngestKey(ctx, appdb.PGXExecutorFor(ctx, s.pool), id, keyHash)
	if err != nil {
		return integrations.IngestIdempotencyRecord{}, false, fmt.Errorf("get ingest idempotency key: %w", err)
	}
	if row == nil {
		return integrations.IngestIdempotencyRecord{}, false, nil
	}
	return integrations.IngestIdempotencyRecord{
		ProviderID: row.ProviderId, KeyHash: append([]byte(nil), row.KeyHash...),
		RequestHash: append([]byte(nil), row.RequestHash...), ResponseStatus: int(row.ResponseStatus),
		ReservationToken: row.ReservationToken,
		ResponseBody:     append(json.RawMessage(nil), row.ResponseBody...),
		CreatedAt:        row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}, true, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func cloneMetadata(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
