package cockroach

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const lockSyncConnectionQuery = `
SELECT subject_id, environment_id
FROM provider_connections
WHERE id = $1::UUID
  AND COALESCE(sync_cursor->>'connection_status', status) IN ('active', 'error')
  AND sync_cursor->>'sync_execution_claim_token' = $2
FOR UPDATE`

var _ integrations.FencedActivitySink = (*Store)(nil)

// SaveConnectionActivity serializes activity publication with disconnect and
// subject deletion on the provider_connections row. If disconnect wins, the
// row is revoked/deleted and this transaction cannot write. If this
// transaction wins, disconnect waits and then removes its committed facts.
// The execution claim prevents an expired worker from writing under a newer
// lease (ABA).
func (s *Store) SaveConnectionActivity(
	ctx context.Context,
	connectionID string,
	claimToken string,
	environments activity.SaveEnvironmentsInput,
	facts activity.SaveFactsInput,
) error {
	if connectionID == "" || claimToken == "" {
		return integrations.ErrInvalidConnectionStatus
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cockroach: begin fenced activity transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var subjectID, environmentID string
	if err := tx.QueryRowContext(ctx, lockSyncConnectionQuery, connectionID, claimToken).Scan(&subjectID, &environmentID); err != nil {
		if err == sql.ErrNoRows {
			return integrations.ErrInvalidConnectionStatus
		}
		return fmt.Errorf("cockroach: fence connection activity: %w", err)
	}
	if facts.Subject != activity.SubjectID(subjectID) {
		return activity.ErrMismatchedFactSubject
	}
	for _, environment := range environments.Environments {
		if environment.ID != activity.EnvironmentID(environmentID) {
			return ErrEnvironmentOutside
		}
		if err := upsertEnvironment(ctx, tx, environment); err != nil {
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
		if fact.EnvironmentID != activity.EnvironmentID(environmentID) {
			return ErrEnvironmentOutside
		}
		if err := upsertFact(ctx, tx, fact, connectionID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cockroach: commit fenced activity transaction: %w", err)
	}
	return nil
}
