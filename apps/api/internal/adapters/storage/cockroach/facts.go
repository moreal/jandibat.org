package cockroach

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func (s *Store) SaveFacts(ctx context.Context, input activity.SaveFactsInput) error {
	if input.Subject == "" {
		return activity.ErrEmptySubject
	}
	if len(input.Facts) == 0 {
		return nil
	}
	return s.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		if err := generated.EnsurePublicSubject(txctx, tx, string(input.Subject)); err != nil {
			return fmt.Errorf("cockroach: ensure public subject: %w", err)
		}
		for _, fact := range input.Facts {
			if fact.Subject == "" {
				fact.Subject = input.Subject
			}
			if fact.Subject != input.Subject {
				return activity.ErrMismatchedFactSubject
			}
			if err := upsertFactGenerated(txctx, tx, fact, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

// The insert and update share one transaction because Cockroach's expression
// index cannot be named as an ON CONFLICT arbiter.
func upsertFactGenerated(ctx context.Context, db appdb.DBTX, fact activity.Fact, providerConnectionID string) error {
	metadata, err := encodeMetadata(fact.Metadata)
	if err != nil {
		return fmt.Errorf("cockroach: encode fact metadata: %w", err)
	}
	date, err := time.Parse(time.DateOnly, string(fact.Date))
	if err != nil {
		return fmt.Errorf("cockroach: parse fact date: %w", err)
	}
	customID := strings.TrimSpace(fact.Metadata["custom_provider_id"])
	if providerConnectionID != "" {
		if customID != "" {
			return fmt.Errorf("cockroach: connection fact cannot reference custom provider")
		}
		connectionUUID, err := uuid.Parse(providerConnectionID)
		if err != nil {
			return fmt.Errorf("cockroach: parse provider connection ID: %w", err)
		}
		if err := generated.InsertConnectionFactIfMissing(ctx, db, string(fact.Subject), string(fact.EnvironmentID), date, string(fact.Action), string(fact.Metric.Name), int64(fact.Metric.Value), metadata, &connectionUUID); err != nil {
			return fmt.Errorf("cockroach: insert connection fact: %w", err)
		}
		if err := generated.UpdateConnectionFact(ctx, db, string(fact.Subject), string(fact.EnvironmentID), date, string(fact.Action), string(fact.Metric.Name), int64(fact.Metric.Value), metadata, connectionUUID); err != nil {
			return fmt.Errorf("cockroach: update connection fact: %w", err)
		}
		return nil
	}
	var providerPtr, customPtr *string
	if customID != "" {
		customPtr = &customID
	}
	if err := generated.InsertFactIfMissing(ctx, db, string(fact.Subject), string(fact.EnvironmentID), date, string(fact.Action), string(fact.Metric.Name), int64(fact.Metric.Value), metadata, providerPtr, customPtr); err != nil {
		return fmt.Errorf("cockroach: insert fact: %w", err)
	}
	if err := generated.UpdateMatchingFact(ctx, db, string(fact.Subject), string(fact.EnvironmentID), date, string(fact.Action), string(fact.Metric.Name), int64(fact.Metric.Value), metadata, providerConnectionID, customID); err != nil {
		return fmt.Errorf("cockroach: update fact: %w", err)
	}
	return nil
}

func (s *Store) LoadFacts(ctx context.Context, input activity.LoadFactsInput) ([]activity.Fact, error) {
	if input.Subject == "" {
		return nil, activity.ErrEmptySubject
	}
	var from, to time.Time
	var err error
	if input.From != nil {
		from, err = time.Parse(time.DateOnly, string(*input.From))
		if err != nil {
			return nil, fmt.Errorf("cockroach: parse from date: %w", err)
		}
	}
	if input.To != nil {
		to, err = time.Parse(time.DateOnly, string(*input.To))
		if err != nil {
			return nil, fmt.Errorf("cockroach: parse to date: %w", err)
		}
	}
	var rows []generated.ListFactsAllRow
	subject := string(input.Subject)
	switch {
	case input.From != nil && input.To != nil:
		selected, queryErr := generated.ListFactsBetween(ctx, s.executor(ctx), subject, from, to)
		err = queryErr
		rows = make([]generated.ListFactsAllRow, 0, len(selected))
		for _, row := range selected {
			rows = append(rows, generated.ListFactsAllRow(row))
		}
	case input.From != nil:
		selected, queryErr := generated.ListFactsFrom(ctx, s.executor(ctx), subject, from)
		err = queryErr
		rows = make([]generated.ListFactsAllRow, 0, len(selected))
		for _, row := range selected {
			rows = append(rows, generated.ListFactsAllRow(row))
		}
	case input.To != nil:
		selected, queryErr := generated.ListFactsTo(ctx, s.executor(ctx), subject, to)
		err = queryErr
		rows = make([]generated.ListFactsAllRow, 0, len(selected))
		for _, row := range selected {
			rows = append(rows, generated.ListFactsAllRow(row))
		}
	default:
		rows, err = generated.ListFactsAll(ctx, s.executor(ctx), subject)
	}
	if err != nil {
		return nil, fmt.Errorf("cockroach: query facts: %w", err)
	}
	facts := make([]activity.Fact, 0, len(rows))
	for _, row := range rows {
		metadata, decodeErr := decodeMetadata(row.Metadata)
		if decodeErr != nil {
			return nil, fmt.Errorf("cockroach: scan fact: %w", decodeErr)
		}
		facts = append(facts, activity.Fact{Subject: activity.SubjectID(row.SubjectId), Date: activity.Date(row.ActivityDate.Format(time.DateOnly)), EnvironmentID: activity.EnvironmentID(row.EnvironmentId), Action: activity.ActionID(row.Action), Metric: activity.Metric{Name: activity.MetricName(row.MetricName), Value: int(row.MetricValue)}, Metadata: metadata})
	}
	return facts, nil
}

func (s *Store) SubjectExists(ctx context.Context, subject activity.SubjectID) (bool, error) {
	if subject == "" {
		return false, activity.ErrEmptySubject
	}
	row, err := generated.GetSubjectExists(ctx, s.executor(ctx), string(subject))
	if err != nil {
		return false, fmt.Errorf("cockroach: check subject existence: %w", err)
	}
	return row.Exists, nil
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
