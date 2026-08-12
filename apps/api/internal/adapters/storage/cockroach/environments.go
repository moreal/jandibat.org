package cockroach

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	upsertEnvironmentQuery = `
INSERT INTO environments (
  id,
  key,
  name,
  scope,
  owner_subject_id,
  metadata,
  updated_at
) VALUES ($1, $2, $3, $4, $5, $6::JSONB, now())
ON CONFLICT (id) DO UPDATE SET
  key = excluded.key,
  name = excluded.name,
  scope = excluded.scope,
  owner_subject_id = excluded.owner_subject_id,
  metadata = excluded.metadata,
  updated_at = excluded.updated_at
WHERE environments.scope = excluded.scope
  AND environments.owner_subject_id IS NOT DISTINCT FROM excluded.owner_subject_id`

	loadEnvironmentsBaseQuery = `
SELECT
  id,
  key,
  name,
  scope,
  owner_subject_id,
  metadata
FROM environments`
)

func (s *Store) SaveEnvironments(ctx context.Context, input activity.SaveEnvironmentsInput) error {
	if len(input.Environments) == 0 {
		return nil
	}

	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return fmt.Errorf("cockroach: begin environment transaction: %w", err)
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()

	for _, environment := range input.Environments {
		if err := upsertEnvironment(ctx, tx, environment); err != nil {
			return err
		}
	}

	if err := scope.Commit(); err != nil {
		return fmt.Errorf("cockroach: commit environment transaction: %w", err)
	}
	return nil
}

func upsertEnvironment(ctx context.Context, executor contextExecutor, environment activity.Environment) error {
	metadata, err := encodeMetadata(environment.Metadata)
	if err != nil {
		return fmt.Errorf("cockroach: encode environment metadata: %w", err)
	}
	var owner any
	if environment.OwnerSubject != nil {
		owner = *environment.OwnerSubject
	}
	result, err := executor.ExecContext(
		ctx,
		upsertEnvironmentQuery,
		environment.ID,
		environment.Key,
		environment.Name,
		environment.Scope,
		owner,
		metadata,
	)
	if err != nil {
		return fmt.Errorf("cockroach: upsert environment: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("cockroach: environment ownership conflict")
	}
	return nil
}

func (s *Store) LoadEnvironments(ctx context.Context, input activity.LoadEnvironmentsInput) ([]activity.Environment, error) {
	query, args := buildLoadEnvironmentsQuery(input.IDs)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cockroach: query environments: %w", err)
	}
	defer rows.Close()

	environments := make([]activity.Environment, 0, len(input.IDs))
	for rows.Next() {
		environment, err := scanEnvironment(rows)
		if err != nil {
			return nil, fmt.Errorf("cockroach: scan environment: %w", err)
		}
		environments = append(environments, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cockroach: iterate environments: %w", err)
	}
	return environments, nil
}

func buildLoadEnvironmentsQuery(ids []activity.EnvironmentID) (string, []any) {
	if len(ids) == 0 {
		return loadEnvironmentsBaseQuery + "\nORDER BY id", nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	return loadEnvironmentsBaseQuery + "\nWHERE id IN (" + strings.Join(placeholders, ", ") + ")\nORDER BY id", args
}

func scanEnvironment(row rowScanner) (activity.Environment, error) {
	var (
		id           string
		key          string
		name         string
		scope        string
		ownerSubject sql.NullString
		metadataJSON []byte
	)
	if err := row.Scan(&id, &key, &name, &scope, &ownerSubject, &metadataJSON); err != nil {
		return activity.Environment{}, err
	}

	metadata, err := decodeMetadata(metadataJSON)
	if err != nil {
		return activity.Environment{}, fmt.Errorf("decode metadata: %w", err)
	}

	var owner *activity.SubjectID
	if ownerSubject.Valid {
		value := activity.SubjectID(ownerSubject.String)
		owner = &value
	}
	return activity.Environment{
		ID:           activity.EnvironmentID(id),
		Key:          key,
		Name:         name,
		Scope:        activity.EnvironmentScope(scope),
		OwnerSubject: owner,
		Metadata:     metadata,
	}, nil
}
