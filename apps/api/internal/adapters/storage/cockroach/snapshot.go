package cockroach

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

// LoadSnapshotProjection reads the authorized facts, their environment
// metadata, and audience-scoped change marker from one Cockroach snapshot.
// A nil change time means this audience has never incorporated a change in
// the selected date/environment scope, including deletion tombstones.
func (s *Store) LoadSnapshotProjection(
	ctx context.Context,
	filter activity.LoadFactsInput,
	environmentIDs []activity.EnvironmentID,
	includePrivate bool,
) ([]activity.Fact, []activity.Environment, *time.Time, error) {
	if filter.Subject == "" {
		return nil, nil, nil, activity.ErrEmptySubject
	}
	from, to, err := factDateBounds(filter)
	if err != nil {
		return nil, nil, nil, err
	}
	selected := make(map[activity.EnvironmentID]struct{}, len(environmentIDs))
	ids := make([]string, 0, len(environmentIDs))
	for _, id := range environmentIDs {
		if _, duplicate := selected[id]; duplicate {
			continue
		}
		selected[id] = struct{}{}
		ids = append(ids, string(id))
	}
	var facts []activity.Fact
	var environments []activity.Environment
	var updatedAt *time.Time
	err = appdb.InTx(ctx, s.pool, appdb.RetryOptions{TxOptions: pgx.TxOptions{AccessMode: pgx.ReadOnly}}, func(txctx context.Context, tx pgx.Tx) error {
		loadedFacts, err := s.LoadFacts(txctx, filter)
		if err != nil {
			return err
		}
		if len(selected) != 0 {
			filtered := loadedFacts[:0]
			for _, fact := range loadedFacts {
				if _, ok := selected[fact.EnvironmentID]; ok {
					filtered = append(filtered, fact)
				}
			}
			loadedFacts = filtered
		}
		factEnvironmentIDs := make(map[activity.EnvironmentID]struct{}, len(loadedFacts))
		for _, fact := range loadedFacts {
			factEnvironmentIDs[fact.EnvironmentID] = struct{}{}
		}
		if len(factEnvironmentIDs) != 0 {
			requested := make([]activity.EnvironmentID, 0, len(factEnvironmentIDs))
			for id := range factEnvironmentIDs {
				requested = append(requested, id)
			}
			loadedEnvironments, err := s.LoadEnvironments(txctx, activity.LoadEnvironmentsInput{IDs: requested})
			if err != nil {
				return err
			}
			allowed := make(map[activity.EnvironmentID]struct{}, len(loadedEnvironments))
			for _, environment := range loadedEnvironments {
				visible := environment.Scope == activity.EnvironmentScopeGlobal
				if environment.Scope == activity.EnvironmentScopeSubject && environment.OwnerSubject != nil && *environment.OwnerSubject == filter.Subject {
					visible = environment.Metadata["visibility"] != "private" || includePrivate
				}
				if visible {
					allowed[environment.ID] = struct{}{}
					environments = append(environments, environment)
				}
			}
			for _, fact := range loadedFacts {
				if _, ok := allowed[fact.EnvironmentID]; ok {
					facts = append(facts, fact)
				}
			}
		}
		marker, err := generated.GetMaxSnapshotChangedAt(txctx, tx, string(filter.Subject), from, to, len(ids) == 0, ids, includePrivate)
		if err != nil {
			return fmt.Errorf("cockroach: load snapshot change time: %w", err)
		}
		updatedAt = marker.ChangedAt
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return facts, environments, updatedAt, nil
}
