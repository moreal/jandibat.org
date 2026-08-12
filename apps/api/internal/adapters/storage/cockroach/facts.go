package cockroach

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	deleteFactsBySubjectQuery = `DELETE FROM activity_facts WHERE subject_id = $1`
	ensurePublicSubjectQuery  = `
INSERT INTO subjects (
  id, owner_user_id, handle, display_name, timezone, is_public
) VALUES ($1, NULL, $1, NULL, 'UTC', true)
ON CONFLICT DO NOTHING`

	upsertFactQuery = `
INSERT INTO activity_facts (
  subject_id,
  environment_id,
  activity_date,
  action,
  metric_name,
  metric_value,
  metadata,
  provider_connection_id,
  custom_provider_id,
  observed_at,
  ingested_at
) VALUES ($1, $2, $3, $4, $5, $6, $7::JSONB, $8::UUID, $9::UUID, now(), now())
ON CONFLICT DO NOTHING`

	updateFactQuery = `
UPDATE activity_facts SET
  metric_value = $6,
  provider_connection_id = $8::UUID,
  custom_provider_id = $9::UUID,
  observed_at = now(),
  ingested_at = now()
WHERE subject_id = $1
  AND environment_id = $2
  AND activity_date = $3
  AND action = $4
  AND metric_name = $5
  AND metadata = $7::JSONB`

	loadFactsBaseQuery = `
SELECT
  subject_id,
  activity_date,
  environment_id,
  action,
  metric_name,
  metric_value,
  metadata
FROM activity_facts
WHERE subject_id = $1`

	subjectExistsQuery = `SELECT EXISTS (SELECT 1 FROM subjects WHERE id = $1)`
)

// SaveFacts upserts facts using the schema's dedupe constraint. Range-scoped
// deletion and replacement are exposed separately through ReplaceFacts.
func (s *Store) SaveFacts(ctx context.Context, input activity.SaveFactsInput) error {
	if input.Subject == "" {
		return activity.ErrEmptySubject
	}

	if len(input.Facts) == 0 {
		return nil
	}

	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return fmt.Errorf("cockroach: begin fact transaction: %w", err)
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	if err := ensurePublicSubject(ctx, tx, input.Subject); err != nil {
		return err
	}

	for _, fact := range input.Facts {
		if fact.Subject == "" {
			fact.Subject = input.Subject
		}
		if fact.Subject != input.Subject {
			return activity.ErrMismatchedFactSubject
		}

		if err := upsertFact(ctx, tx, fact, nil); err != nil {
			return err
		}
	}

	if err := scope.Commit(); err != nil {
		return fmt.Errorf("cockroach: commit fact transaction: %w", err)
	}
	return nil
}

type contextExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ensurePublicSubject materializes an ownerless, public shadow subject for an
// arbitrary forge handle. ON CONFLICT deliberately performs no update, so a
// managed subject's owner, visibility, timezone, and display name can never be
// changed by a public activity refresh. Keeping this in the fact transaction
// makes the subject FK visible before the first activity_facts insert.
func ensurePublicSubject(ctx context.Context, executor contextExecutor, subject activity.SubjectID) error {
	if subject == "" {
		return activity.ErrEmptySubject
	}
	if _, err := executor.ExecContext(ctx, ensurePublicSubjectQuery, subject); err != nil {
		return fmt.Errorf("cockroach: ensure public subject: %w", err)
	}
	return nil
}

// CockroachDB cannot use an expression index as an ON CONFLICT target. The
// initial insert therefore uses target-less DO NOTHING, then the update locates
// the logical identity by JSONB equality. Both statements run in one
// transaction, preserving the existing expression-index dedupe contract.
func upsertFact(ctx context.Context, executor contextExecutor, fact activity.Fact, providerConnectionID any) error {
	metadata, err := encodeMetadata(fact.Metadata)
	if err != nil {
		return fmt.Errorf("cockroach: encode fact metadata: %w", err)
	}
	args := []any{
		fact.Subject,
		fact.EnvironmentID,
		fact.Date,
		fact.Action,
		fact.Metric.Name,
		fact.Metric.Value,
		metadata,
		providerConnectionID,
		nullableCustomProviderID(fact),
	}
	if _, err := executor.ExecContext(ctx, upsertFactQuery, args...); err != nil {
		return fmt.Errorf("cockroach: insert fact: %w", err)
	}
	if _, err := executor.ExecContext(ctx, updateFactQuery, args...); err != nil {
		return fmt.Errorf("cockroach: update fact: %w", err)
	}
	return nil
}

func nullableCustomProviderID(fact activity.Fact) any {
	if id := strings.TrimSpace(fact.Metadata["custom_provider_id"]); id != "" {
		return id
	}
	return nil
}

// LoadFacts returns facts ordered deterministically by date and identity.
func (s *Store) LoadFacts(ctx context.Context, input activity.LoadFactsInput) ([]activity.Fact, error) {
	if input.Subject == "" {
		return nil, activity.ErrEmptySubject
	}

	query, args := buildLoadFactsQuery(input)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cockroach: query facts: %w", err)
	}
	defer rows.Close()

	facts := make([]activity.Fact, 0)
	for rows.Next() {
		fact, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("cockroach: scan fact: %w", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cockroach: iterate facts: %w", err)
	}
	return facts, nil
}

// SubjectExists distinguishes a known subject with an empty activity range
// from an arbitrary public identifier. Callers use this before persisting
// negative provider results, preventing durable ownerless-row amplification.
func (s *Store) SubjectExists(ctx context.Context, subject activity.SubjectID) (bool, error) {
	if subject == "" {
		return false, activity.ErrEmptySubject
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, subjectExistsQuery, subject).Scan(&exists); err != nil {
		return false, fmt.Errorf("cockroach: check subject existence: %w", err)
	}
	return exists, nil
}

func buildLoadFactsQuery(input activity.LoadFactsInput) (string, []any) {
	var query strings.Builder
	query.WriteString(loadFactsBaseQuery)
	args := []any{input.Subject}

	if input.From != nil {
		args = append(args, *input.From)
		fmt.Fprintf(&query, "\n  AND activity_date >= $%d", len(args))
	}
	if input.To != nil {
		args = append(args, *input.To)
		fmt.Fprintf(&query, "\n  AND activity_date <= $%d", len(args))
	}
	query.WriteString("\nORDER BY activity_date, environment_id, action, metric_name, id")

	return query.String(), args
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFact(row rowScanner) (activity.Fact, error) {
	var (
		subject       string
		date          time.Time
		environmentID string
		action        string
		metricName    string
		metricValue   int64
		metadataJSON  []byte
	)
	if err := row.Scan(
		&subject,
		&date,
		&environmentID,
		&action,
		&metricName,
		&metricValue,
		&metadataJSON,
	); err != nil {
		return activity.Fact{}, err
	}

	metadata, err := decodeMetadata(metadataJSON)
	if err != nil {
		return activity.Fact{}, fmt.Errorf("decode metadata: %w", err)
	}

	return activity.Fact{
		Subject:       activity.SubjectID(subject),
		Date:          activity.Date(date.Format(time.DateOnly)),
		EnvironmentID: activity.EnvironmentID(environmentID),
		Action:        activity.ActionID(action),
		Metric: activity.Metric{
			Name:  activity.MetricName(metricName),
			Value: int(metricValue),
		},
		Metadata: metadata,
	}, nil
}

func encodeMetadata(metadata map[string]string) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(metadata)
}

func decodeMetadata(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = map[string]string{}
	}
	return metadata, nil
}

// Keep database/sql imported in this file's surface: *sql.Rows implements the
// scanner used by scanFact, while the narrow interface keeps mapping testable.
var _ rowScanner = (*sql.Rows)(nil)
