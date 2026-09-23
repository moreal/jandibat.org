package cockroach

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func (s *Store) SaveEnvironments(ctx context.Context, input activity.SaveEnvironmentsInput) error {
	if len(input.Environments) == 0 {
		return nil
	}
	return s.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		for _, environment := range input.Environments {
			if err := upsertEnvironmentGenerated(txctx, tx, environment); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertEnvironmentGenerated(ctx context.Context, db appdb.DBTX, environment activity.Environment) error {
	metadata, err := encodeMetadata(environment.Metadata)
	if err != nil {
		return fmt.Errorf("cockroach: encode environment metadata: %w", err)
	}
	var owner *string
	if environment.OwnerSubject != nil {
		value := string(*environment.OwnerSubject)
		owner = &value
	}
	count, err := generated.UpsertEnvironment(ctx, db, string(environment.ID), environment.Key, environment.Name, string(environment.Scope), owner, metadata)
	if err != nil {
		return fmt.Errorf("cockroach: upsert environment: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("cockroach: environment ownership conflict")
	}
	return nil
}

func (s *Store) LoadEnvironments(ctx context.Context, input activity.LoadEnvironmentsInput) ([]activity.Environment, error) {
	var rows []generated.ListEnvironmentsAllRow
	var err error
	if len(input.IDs) == 0 {
		rows, err = generated.ListEnvironmentsAll(ctx, s.executor(ctx))
	} else {
		ids := make([]string, len(input.IDs))
		for index, id := range input.IDs {
			ids[index] = string(id)
		}
		selected, queryErr := generated.ListEnvironmentsByIds(ctx, s.executor(ctx), ids)
		err = queryErr
		rows = make([]generated.ListEnvironmentsAllRow, 0, len(selected))
		for _, row := range selected {
			rows = append(rows, generated.ListEnvironmentsAllRow(row))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("cockroach: query environments: %w", err)
	}
	environments := make([]activity.Environment, 0, len(rows))
	for _, row := range rows {
		metadata, decodeErr := decodeMetadata(row.Metadata)
		if decodeErr != nil {
			return nil, fmt.Errorf("cockroach: scan environment: %w", decodeErr)
		}
		var owner *activity.SubjectID
		if row.OwnerSubjectId != nil {
			value := activity.SubjectID(*row.OwnerSubjectId)
			owner = &value
		}
		environments = append(environments, activity.Environment{ID: activity.EnvironmentID(row.Id), Key: row.Key, Name: row.Name, Scope: activity.EnvironmentScope(row.Scope), OwnerSubject: owner, Metadata: metadata})
	}
	return environments, nil
}
