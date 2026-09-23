package cockroach

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach/generated"
	application "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

var (
	ErrFactOutsideRange   = errors.New("cockroach: fact is outside replacement range")
	ErrEnvironmentOutside = errors.New("cockroach: fact environment is outside replacement set")
)

var _ application.Store = (*Store)(nil)

// ReplaceFacts replaces a range and an optional environment set atomically.
func (s *Store) ReplaceFacts(ctx context.Context, filter domain.LoadFactsInput, environmentIDs []domain.EnvironmentID, facts []domain.Fact) error {
	if filter.Subject == "" {
		return domain.ErrEmptySubject
	}
	environmentSet := make(map[domain.EnvironmentID]struct{}, len(environmentIDs))
	for _, id := range environmentIDs {
		environmentSet[id] = struct{}{}
	}
	for _, fact := range facts {
		if fact.Subject != filter.Subject {
			return domain.ErrMismatchedFactSubject
		}
		if !dateInRange(fact.Date, filter.From, filter.To) {
			return ErrFactOutsideRange
		}
		if len(environmentSet) > 0 {
			if _, ok := environmentSet[fact.EnvironmentID]; !ok {
				return ErrEnvironmentOutside
			}
		}
	}
	from, to, err := factDateBounds(filter)
	if err != nil {
		return err
	}
	ids := make([]string, len(environmentIDs))
	for index, id := range environmentIDs {
		ids[index] = string(id)
	}
	return s.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		if len(facts) != 0 {
			if err := generated.EnsurePublicSubject(txctx, tx, string(filter.Subject)); err != nil {
				return fmt.Errorf("cockroach: ensure public subject: %w", err)
			}
		}
		if err := generated.DeleteFactsInRange(txctx, tx, string(filter.Subject), from, to, len(ids) == 0, ids); err != nil {
			return fmt.Errorf("cockroach: delete replaced facts: %w", err)
		}
		for _, fact := range facts {
			if err := upsertFactGenerated(txctx, tx, fact, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

func factDateBounds(filter domain.LoadFactsInput) (time.Time, time.Time, error) {
	from := time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
	var err error
	if filter.From != nil {
		from, err = time.Parse(time.DateOnly, string(*filter.From))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("cockroach: parse from date: %w", err)
		}
	}
	if filter.To != nil {
		to, err = time.Parse(time.DateOnly, string(*filter.To))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("cockroach: parse to date: %w", err)
		}
	}
	return from, to, nil
}

func (s *Store) LoadCachedAt(ctx context.Context, subject domain.SubjectID, environmentID domain.EnvironmentID, date domain.Date) (*time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, string(date))
	if err != nil {
		return nil, fmt.Errorf("cockroach: parse cache date: %w", err)
	}
	row, err := generated.GetCachedAt(ctx, s.executor(ctx), string(subject), string(environmentID), parsed)
	if err != nil {
		return nil, fmt.Errorf("cockroach: load refresh timestamp: %w", err)
	}
	if row == nil {
		return nil, nil
	}
	return &row.FetchedAt, nil
}

func (s *Store) SaveCachedAt(ctx context.Context, subject domain.SubjectID, environmentID domain.EnvironmentID, dates []domain.Date, at time.Time) error {
	if len(dates) == 0 {
		return nil
	}
	return s.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		for _, date := range dates {
			parsed, err := time.Parse(time.DateOnly, string(date))
			if err != nil {
				return fmt.Errorf("cockroach: parse cache date: %w", err)
			}
			if err := generated.UpsertCachedAt(txctx, tx, string(subject), string(environmentID), parsed, at); err != nil {
				return fmt.Errorf("cockroach: save refresh timestamp: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) DeleteCachedAt(ctx context.Context, subject domain.SubjectID, environmentID domain.EnvironmentID, dates []domain.Date) error {
	if len(dates) == 0 {
		return nil
	}
	parsed := make([]time.Time, len(dates))
	for index, date := range dates {
		value, err := time.Parse(time.DateOnly, string(date))
		if err != nil {
			return fmt.Errorf("cockroach: parse cache date: %w", err)
		}
		parsed[index] = value
	}
	if err := generated.DeleteCachedAt(ctx, s.executor(ctx), string(subject), string(environmentID), parsed); err != nil {
		return fmt.Errorf("cockroach: delete refresh timestamps: %w", err)
	}
	return nil
}

func dateInRange(date domain.Date, from, to *domain.Date) bool {
	return (from == nil || date >= *from) && (to == nil || date <= *to)
}
