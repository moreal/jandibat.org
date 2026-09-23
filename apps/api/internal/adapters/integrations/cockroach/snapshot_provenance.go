package cockroach

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
)

// touchActivitySnapshotChange runs in the same transaction as the fact purge.
// Callers only invoke it for keys observed among rows that were actually
// removed, so a repeated revoke or purge cannot advance dataUpdatedAt.
func touchActivitySnapshotChange(ctx context.Context, tx pgx.Tx, subjectID, environmentID string, date time.Time, scope string) error {
	return generated.TouchActivitySnapshotChange(ctx, tx, subjectID, environmentID, date, scope)
}
