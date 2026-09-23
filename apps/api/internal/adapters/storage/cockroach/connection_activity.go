package cockroach

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var _ integrations.FencedActivitySink = (*Store)(nil)

// SaveConnectionActivity fences publication with disconnect and a current
// execution claim, then writes environments and facts in the same transaction.
func (s *Store) SaveConnectionActivity(ctx context.Context, connectionID, claimToken string, environments activity.SaveEnvironmentsInput, facts activity.SaveFactsInput) error {
	if connectionID == "" || claimToken == "" {
		return integrations.ErrInvalidConnectionStatus
	}
	id, err := uuid.Parse(connectionID)
	if err != nil {
		return integrations.ErrInvalidConnectionStatus
	}
	return s.inTx(ctx, func(txctx context.Context, tx pgx.Tx) error {
		locked, err := generated.LockSyncConnection(txctx, tx, id, claimToken)
		if err != nil {
			return fmt.Errorf("cockroach: fence connection activity: %w", err)
		}
		if locked == nil {
			return integrations.ErrInvalidConnectionStatus
		}
		if facts.Subject != activity.SubjectID(locked.SubjectId) {
			return activity.ErrMismatchedFactSubject
		}
		for _, environment := range environments.Environments {
			if environment.ID != activity.EnvironmentID(locked.EnvironmentId) {
				return ErrEnvironmentOutside
			}
			if err := upsertEnvironmentGenerated(txctx, tx, environment); err != nil {
				return err
			}
		}
		for _, fact := range facts.Facts {
			if fact.Subject == "" {
				fact.Subject = facts.Subject
			}
			if fact.Subject != facts.Subject {
				return activity.ErrMismatchedFactSubject
			}
			if fact.EnvironmentID != activity.EnvironmentID(locked.EnvironmentId) {
				return ErrEnvironmentOutside
			}
			if err := upsertFactGenerated(txctx, tx, fact, connectionID); err != nil {
				return err
			}
		}
		return nil
	})
}
