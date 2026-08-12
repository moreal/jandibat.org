package cockroach

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	application "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

var (
	ErrFactOutsideRange   = errors.New("cockroach: fact is outside replacement range")
	ErrEnvironmentOutside = errors.New("cockroach: fact environment is outside replacement set")
)

const (
	loadCachedAtQuery = `
SELECT fetched_at
FROM activity_refresh_cache
WHERE subject_id = $1 AND environment_id = $2 AND activity_date = $3`

	upsertCachedAtQuery = `
INSERT INTO activity_refresh_cache (
  subject_id, environment_id, activity_date, fetched_at, updated_at
) VALUES ($1, $2, $3, $4, now())
ON CONFLICT (subject_id, environment_id, activity_date) DO UPDATE SET
  fetched_at = excluded.fetched_at,
  updated_at = excluded.updated_at`
)

var _ application.Store = (*Store)(nil)

// ReplaceFacts atomically replaces only the selected date range and
// environments. An empty environment list selects all environments.
func (s *Store) ReplaceFacts(
	ctx context.Context,
	filter domain.LoadFactsInput,
	environmentIDs []domain.EnvironmentID,
	facts []domain.Fact,
) error {
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

	ctx, scope, err := s.beginMutation(ctx)
	if err != nil {
		return fmt.Errorf("cockroach: begin replacement transaction: %w", err)
	}
	tx := scope.Tx
	defer func() { _ = scope.Rollback() }()
	if len(facts) != 0 {
		if err := ensurePublicSubject(ctx, tx, filter.Subject); err != nil {
			return err
		}
	}

	deleteQuery, deleteArgs := buildDeleteFactsQuery(filter, environmentIDs)
	if _, err := tx.ExecContext(ctx, deleteQuery, deleteArgs...); err != nil {
		return fmt.Errorf("cockroach: delete replaced facts: %w", err)
	}
	for _, fact := range facts {
		if err := upsertFact(ctx, tx, fact, nil); err != nil {
			return err
		}
	}

	if err := scope.Commit(); err != nil {
		return fmt.Errorf("cockroach: commit replacement transaction: %w", err)
	}
	return nil
}

func (s *Store) LoadCachedAt(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	date domain.Date,
) (*time.Time, error) {
	var fetchedAt time.Time
	err := s.db.QueryRowContext(ctx, loadCachedAtQuery, subject, environmentID, date).Scan(&fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cockroach: load refresh timestamp: %w", err)
	}
	return &fetchedAt, nil
}

func (s *Store) SaveCachedAt(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	dates []domain.Date,
	at time.Time,
) error {
	if len(dates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cockroach: begin refresh-cache transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, date := range dates {
		if _, err := tx.ExecContext(ctx, upsertCachedAtQuery, subject, environmentID, date, at); err != nil {
			return fmt.Errorf("cockroach: save refresh timestamp: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cockroach: commit refresh-cache transaction: %w", err)
	}
	return nil
}

func (s *Store) DeleteCachedAt(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	dates []domain.Date,
) error {
	if len(dates) == 0 {
		return nil
	}
	placeholders := make([]string, len(dates))
	args := make([]any, 0, len(dates)+2)
	args = append(args, subject, environmentID)
	for i, date := range dates {
		args = append(args, date)
		placeholders[i] = fmt.Sprintf("$%d", i+3)
	}
	query := `DELETE FROM activity_refresh_cache
WHERE subject_id = $1 AND environment_id = $2 AND activity_date IN (` + strings.Join(placeholders, ", ") + ")"
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("cockroach: delete refresh timestamps: %w", err)
	}
	return nil
}

func buildDeleteFactsQuery(filter domain.LoadFactsInput, environmentIDs []domain.EnvironmentID) (string, []any) {
	var query strings.Builder
	query.WriteString(deleteFactsBySubjectQuery)
	args := []any{filter.Subject}
	if filter.From != nil {
		args = append(args, *filter.From)
		fmt.Fprintf(&query, "\n  AND activity_date >= $%d", len(args))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		fmt.Fprintf(&query, "\n  AND activity_date <= $%d", len(args))
	}
	if len(environmentIDs) > 0 {
		placeholders := make([]string, len(environmentIDs))
		for i, id := range environmentIDs {
			args = append(args, id)
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		query.WriteString("\n  AND environment_id IN (" + strings.Join(placeholders, ", ") + ")")
	}
	return query.String(), args
}

func dateInRange(date domain.Date, from, to *domain.Date) bool {
	return (from == nil || date >= *from) && (to == nil || date <= *to)
}
