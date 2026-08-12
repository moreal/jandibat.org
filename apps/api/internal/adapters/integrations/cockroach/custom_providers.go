package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const customProviderColumns = `
id::STRING, subject_id, environment_id, slug, name, COALESCE(description, ''),
status, configuration, ingest_token_hash, created_at, updated_at`

const upsertCustomProviderQuery = `
INSERT INTO custom_providers (
  id, owner_user_id, subject_id, environment_id, slug, name, description,
  status, ingest_token_hash, configuration, created_at, updated_at
)
SELECT $1::UUID, owner_user_id, $2, $3, $4, $5, NULLIF($6, ''), $7, $8,
  jsonb_build_object(
    'allowed_actions', $9::JSONB,
    'allowed_metrics', $10::JSONB
  ), $11, $12
FROM subjects WHERE id = $2 AND owner_user_id IS NOT NULL
ON CONFLICT (id) DO UPDATE SET
  environment_id = excluded.environment_id,
  slug = excluded.slug,
  name = excluded.name,
  description = excluded.description,
  status = excluded.status,
  ingest_token_hash = excluded.ingest_token_hash,
  configuration = COALESCE(custom_providers.configuration, '{}'::JSONB)
    || excluded.configuration,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at`

const createCustomProviderQuery = `
INSERT INTO custom_providers (
  id, owner_user_id, subject_id, environment_id, slug, name, description,
  status, ingest_token_hash, configuration, created_at, updated_at
)
SELECT $1::UUID, owner_user_id, $2, $3, $4, $5, NULLIF($6, ''), $7, $8,
  jsonb_build_object('allowed_actions', $9::JSONB, 'allowed_metrics', $10::JSONB), $11, $12
FROM subjects WHERE id = $2 AND owner_user_id IS NOT NULL`

const updateCustomProviderQuery = `
UPDATE custom_providers
SET environment_id = $3, slug = $4, name = $5, description = NULLIF($6, ''),
    status = $7, ingest_token_hash = $8,
    configuration = COALESCE(configuration, '{}'::JSONB)
      || jsonb_build_object('allowed_actions', $9::JSONB, 'allowed_metrics', $10::JSONB),
	created_at = $11,
    updated_at = $12
WHERE id = $1::UUID AND subject_id = $2`

func (s *Store) SaveCustomProvider(ctx context.Context, record integrations.CustomProviderRecord) error {
	return s.saveCustomProvider(ctx, upsertCustomProviderQuery, record)
}

func (s *Store) CreateCustomProvider(ctx context.Context, record integrations.CustomProviderRecord) error {
	return s.saveCustomProvider(ctx, createCustomProviderQuery, record)
}

func (s *Store) UpdateCustomProvider(ctx context.Context, record integrations.CustomProviderRecord) error {
	return s.saveCustomProvider(ctx, updateCustomProviderQuery, record)
}

func (s *Store) saveCustomProvider(ctx context.Context, query string, record integrations.CustomProviderRecord) error {
	provider := record.Provider
	if provider.ID == "" {
		return integrations.ErrInvalidProvider
	}
	actions, err := json.Marshal(provider.AllowedActions)
	if err != nil {
		return fmt.Errorf("encode allowed actions: %w", err)
	}
	metrics, err := json.Marshal(provider.AllowedMetrics)
	if err != nil {
		return fmt.Errorf("encode allowed metrics: %w", err)
	}
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, query,
		provider.ID, provider.SubjectID, provider.EnvironmentID, provider.Slug,
		provider.Name, provider.Description, provider.Status,
		record.EncryptedIngestSecret, actions, metrics,
		provider.CreatedAt, provider.UpdatedAt,
	)
	if err != nil {
		return persistenceError(err, integrations.ErrDuplicateProviderSlug)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save custom provider: rows affected: %w", err)
	}
	if count == 0 {
		return notFound("owned subject", provider.SubjectID)
	}
	return nil
}

func (s *Store) DeleteCustomProviderAggregate(ctx context.Context, id string) error {
	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return fmt.Errorf("delete custom provider aggregate: begin: %w", err)
	}
	tx := scope.Tx
	defer scope.Rollback()
	var subjectID, environmentID string
	if err := tx.QueryRowContext(ctx, `SELECT subject_id, environment_id FROM custom_providers WHERE id = $1::UUID FOR UPDATE`, id).Scan(&subjectID, &environmentID); err != nil {
		if err == sql.ErrNoRows {
			return notFound("custom provider", id)
		}
		return fmt.Errorf("delete custom provider aggregate: load: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_refresh_cache WHERE subject_id = $1 AND environment_id = $2`, subjectID, environmentID); err != nil {
		return fmt.Errorf("delete custom provider aggregate: refresh cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_facts WHERE custom_provider_id = $1::UUID OR (subject_id = $2 AND environment_id = $3)`, id, subjectID, environmentID); err != nil {
		return fmt.Errorf("delete custom provider aggregate: facts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM custom_providers WHERE id = $1::UUID`, id); err != nil {
		return fmt.Errorf("delete custom provider aggregate: provider: %w", err)
	}
	// The API role deliberately has no DELETE privilege on environments. Keep
	// the now-unreferenced, subject-scoped environment as a sanitized tombstone;
	// bounded maintenance retention removes these orphans later. Expanding the
	// public API role to delete a shared foundational table would weaken the
	// least-privilege boundary for every provider mutation.
	if err := scope.Commit(); err != nil {
		return fmt.Errorf("delete custom provider aggregate: commit: %w", err)
	}
	return nil
}

func (s *Store) GetCustomProvider(ctx context.Context, id string) (integrations.CustomProviderRecord, error) {
	query := `SELECT ` + customProviderColumns + ` FROM custom_providers WHERE id = $1::UUID`
	record, err := scanCustomProvider(appdb.ExecutorFor(ctx, s.db).QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return integrations.CustomProviderRecord{}, notFound("custom provider", id)
	}
	if err != nil {
		return integrations.CustomProviderRecord{}, fmt.Errorf("get custom provider: %w", err)
	}
	return record, nil
}

func (s *Store) ListCustomProviders(ctx context.Context, subjectID string) ([]integrations.CustomProviderRecord, error) {
	query, args := buildListCustomProvidersQuery(subjectID)
	rows, err := appdb.ExecutorFor(ctx, s.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list custom providers: %w", err)
	}
	defer rows.Close()
	records := make([]integrations.CustomProviderRecord, 0)
	for rows.Next() {
		record, scanErr := scanCustomProvider(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list custom providers: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list custom providers: %w", err)
	}
	return records, nil
}

func buildListCustomProvidersQuery(subjectID string) (string, []any) {
	query := `SELECT ` + customProviderColumns + ` FROM custom_providers`
	args := []any{}
	if subjectID != "" {
		query += ` WHERE subject_id = $1`
		args = append(args, subjectID)
	}
	query += ` ORDER BY subject_id, slug, id`
	return query, args
}

func (s *Store) DeleteCustomProvider(ctx context.Context, id string) error {
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `DELETE FROM custom_providers WHERE id = $1::UUID`, id)
	if err != nil {
		return fmt.Errorf("delete custom provider: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete custom provider: rows affected: %w", err)
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

func unmarshalConfiguration(data []byte, value *customProviderConfiguration) error {
	return json.Unmarshal(data, value)
}

func scanCustomProvider(row scanner) (integrations.CustomProviderRecord, error) {
	var record integrations.CustomProviderRecord
	var configuration []byte
	p := &record.Provider
	if err := row.Scan(
		&p.ID, &p.SubjectID, &p.EnvironmentID, &p.Slug, &p.Name,
		&p.Description, &p.Status, &configuration, &record.EncryptedIngestSecret,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return integrations.CustomProviderRecord{}, err
	}
	var decoded customProviderConfiguration
	if err := unmarshalConfiguration(configuration, &decoded); err != nil {
		return integrations.CustomProviderRecord{}, fmt.Errorf("decode custom provider configuration: %w", err)
	}
	p.AllowedActions = nonNilStrings(decoded.AllowedActions)
	p.AllowedMetrics = nonNilStrings(decoded.AllowedMetrics)
	record.EncryptedIngestSecret = append([]byte(nil), record.EncryptedIngestSecret...)
	return record, nil
}

const customActivityColumns = `
custom_provider_id::STRING, subject_id, event_id, activity_date::STRING,
action, metric_name, metric_value, metadata, observed_at, ingested_at`

const insertCustomActivityQuery = `
INSERT INTO custom_activity_events (
  custom_provider_id, subject_id, environment_id, event_id, activity_date,
  action, metric_name, metric_value, metadata, observed_at, ingested_at
)
SELECT provider.id, provider.subject_id, provider.environment_id,
  $2, $3::DATE, $4, $5, $6, $7::JSONB, $8, $9
FROM custom_providers AS provider
WHERE provider.id = $1::UUID
ON CONFLICT (custom_provider_id, event_id) DO NOTHING
RETURNING ` + customActivityColumns

// SaveIngestedActivities atomically accepts the first event for each
// (provider_id, external_id) pair. The returned values are scanned from the
// inserted rows so callers receive the database's canonical representation.
func (s *Store) SaveIngestedActivities(ctx context.Context, activities []integrations.IngestedActivity) ([]integrations.IngestedActivity, error) {
	if len(activities) == 0 {
		return []integrations.IngestedActivity{}, nil
	}
	providerID := activities[0].ProviderID
	for _, item := range activities {
		if item.ProviderID != providerID {
			return nil, fmt.Errorf("save ingested activities: mixed provider IDs")
		}
	}
	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return nil, fmt.Errorf("save ingested activities: begin: %w", err)
	}
	tx := scope.Tx
	defer scope.Rollback()
	var canonicalProviderID string
	if err := tx.QueryRowContext(ctx,
		`SELECT id::STRING FROM custom_providers WHERE id = $1::UUID FOR UPDATE`,
		providerID,
	).Scan(&canonicalProviderID); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFound("custom provider", providerID)
		}
		return nil, fmt.Errorf("save ingested activities: get provider: %w", err)
	}
	accepted := make([]integrations.IngestedActivity, 0, len(activities))
	for _, item := range activities {
		metadata := item.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		encodedMetadata, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("save ingested activities: encode metadata: %w", err)
		}
		stored, err := scanIngestedActivity(tx.QueryRowContext(ctx, insertCustomActivityQuery,
			canonicalProviderID, item.ExternalID, item.Date, item.Action, item.Metric,
			item.Value, encodedMetadata, item.ObservedAt, item.IngestedAt,
		))
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("save ingested activities: insert event %q: %w", item.ExternalID, err)
		}
		accepted = append(accepted, stored)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE custom_providers
SET last_ingested_at = now(), updated_at = now()
WHERE id = $1::UUID`, canonicalProviderID); err != nil {
		return nil, fmt.Errorf("save ingested activities: update provider: %w", err)
	}
	if err := scope.Commit(); err != nil {
		return nil, fmt.Errorf("save ingested activities: commit: %w", err)
	}
	return accepted, nil
}

func (s *Store) ListIngestedActivities(ctx context.Context, providerID string) ([]integrations.IngestedActivity, error) {
	executor := appdb.ExecutorFor(ctx, s.db)
	var canonicalProviderID string
	err := executor.QueryRowContext(ctx,
		`SELECT id::STRING FROM custom_providers WHERE id = $1::UUID`, providerID,
	).Scan(&canonicalProviderID)
	if err == sql.ErrNoRows {
		return nil, notFound("custom provider", providerID)
	}
	if err != nil {
		return nil, fmt.Errorf("list ingested activities: get provider: %w", err)
	}
	query := `SELECT ` + customActivityColumns + `
FROM custom_activity_events
WHERE custom_provider_id = $1::UUID
ORDER BY activity_date, event_id`
	rows, err := executor.QueryContext(ctx, query, canonicalProviderID)
	if err != nil {
		return nil, fmt.Errorf("list ingested activities: query: %w", err)
	}
	defer rows.Close()
	items := make([]integrations.IngestedActivity, 0)
	for rows.Next() {
		item, scanErr := scanIngestedActivity(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list ingested activities: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ingested activities: %w", err)
	}
	return items, nil
}

func scanIngestedActivity(row scanner) (integrations.IngestedActivity, error) {
	var item integrations.IngestedActivity
	var metadata []byte
	var observedAt sql.NullTime
	if err := row.Scan(
		&item.ProviderID, &item.SubjectID, &item.ExternalID, &item.Date,
		&item.Action, &item.Metric, &item.Value, &metadata, &observedAt,
		&item.IngestedAt,
	); err != nil {
		return integrations.IngestedActivity{}, err
	}
	if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
		return integrations.IngestedActivity{}, fmt.Errorf("decode custom activity metadata: %w", err)
	}
	item.Metadata = cloneMetadata(item.Metadata)
	if observedAt.Valid {
		value := observedAt.Time
		item.ObservedAt = &value
	}
	return item, nil
}

// CreateIngestIdempotencyKey atomically reserves a provider-scoped key before
// any event or fact mutation. Callers use ResponseStatus zero and an empty JSON
// body for a pending reservation. It returns false while an unexpired record
// already owns the key.
func (s *Store) CreateIngestIdempotencyKey(ctx context.Context, record integrations.IngestIdempotencyRecord) (bool, error) {
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return false, err
	}
	result, err := executor.ExecContext(ctx, `
INSERT INTO ingest_idempotency_keys (
  custom_provider_id, key_hash, request_hash, response_status, response_body,
  created_at, expires_at
)
VALUES ($1::UUID, $2, $3, $4, $5::JSONB, $6, $7)
ON CONFLICT (custom_provider_id, key_hash) DO UPDATE SET
  request_hash = excluded.request_hash,
  response_status = excluded.response_status,
  response_body = excluded.response_body,
  created_at = excluded.created_at,
  expires_at = excluded.expires_at
WHERE ingest_idempotency_keys.expires_at <= now()`,
		record.ProviderID, record.KeyHash, record.RequestHash, record.ResponseStatus,
		[]byte(record.ResponseBody), record.CreatedAt, record.ExpiresAt,
	)
	if err != nil {
		return false, fmt.Errorf("create ingest idempotency key: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("create ingest idempotency key: rows affected: %w", err)
	}
	return count == 1, nil
}

// CompleteIngestIdempotencyKey transitions this request's pending reservation
// to its replayable response. The request hash and pending status form a CAS so
// a different request can never complete the reservation.
func (s *Store) CompleteIngestIdempotencyKey(ctx context.Context, record integrations.IngestIdempotencyRecord) (bool, error) {
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return false, err
	}
	result, err := executor.ExecContext(ctx, `
UPDATE ingest_idempotency_keys
SET response_status = $4, response_body = $5::JSONB, expires_at = $6
WHERE custom_provider_id = $1::UUID
  AND key_hash = $2
  AND request_hash = $3
  AND response_status = 0
  AND expires_at > now()`,
		record.ProviderID, record.KeyHash, record.RequestHash, record.ResponseStatus,
		[]byte(record.ResponseBody), record.ExpiresAt,
	)
	if err != nil {
		return false, fmt.Errorf("complete ingest idempotency key: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("complete ingest idempotency key: rows affected: %w", err)
	}
	return count == 1, nil
}

// ReleaseIngestIdempotencyKey removes only this request's pending reservation,
// allowing a retry after validation, event persistence, or projection fails.
// Completed responses and reservations for another request are untouched.
func (s *Store) ReleaseIngestIdempotencyKey(ctx context.Context, providerID string, keyHash, requestHash []byte) error {
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	if _, err := executor.ExecContext(ctx, `
DELETE FROM ingest_idempotency_keys
WHERE custom_provider_id = $1::UUID
  AND key_hash = $2
  AND request_hash = $3
  AND response_status = 0`, providerID, keyHash, requestHash); err != nil {
		return fmt.Errorf("release ingest idempotency key: %w", err)
	}
	return nil
}

// GetIngestIdempotencyKey loads an unexpired provider-scoped key. Expired or
// unknown keys return found=false and may be reclaimed after retention cleanup.
func (s *Store) GetIngestIdempotencyKey(ctx context.Context, providerID string, keyHash []byte) (record integrations.IngestIdempotencyRecord, found bool, err error) {
	executor, err := s.mutationExecutor(ctx)
	if err != nil {
		return integrations.IngestIdempotencyRecord{}, false, err
	}
	var responseBody []byte
	err = executor.QueryRowContext(ctx, `
SELECT custom_provider_id::STRING, key_hash, request_hash, response_status,
  response_body, created_at, expires_at
FROM ingest_idempotency_keys
WHERE custom_provider_id = $1::UUID AND key_hash = $2 AND expires_at > now()`,
		providerID, keyHash,
	).Scan(
		&record.ProviderID, &record.KeyHash, &record.RequestHash,
		&record.ResponseStatus, &responseBody, &record.CreatedAt, &record.ExpiresAt,
	)
	if err == sql.ErrNoRows {
		return integrations.IngestIdempotencyRecord{}, false, nil
	}
	if err != nil {
		return integrations.IngestIdempotencyRecord{}, false, fmt.Errorf("get ingest idempotency key: %w", err)
	}
	record.KeyHash = append([]byte(nil), record.KeyHash...)
	record.RequestHash = append([]byte(nil), record.RequestHash...)
	record.ResponseBody = append(json.RawMessage(nil), responseBody...)
	return record, true, nil
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
