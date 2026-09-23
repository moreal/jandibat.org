package cockroach

import (
	"context"

	"github.com/jackc/pgx/v5"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var _ integrations.TransactionalActivitySink = (*Store)(nil)

// SaveFactsInCurrentTransaction refuses to publish facts unless the caller's
// active pgx transaction belongs to this exact pool. A different sink or pool
// cannot silently commit facts outside the custom-ingest transaction.
func (s *Store) SaveFactsInCurrentTransaction(ctx context.Context, input activity.SaveFactsInput) error {
	if s == nil || s.pool == nil {
		return ErrNilDB
	}
	if _, ok := appdb.PGXExecutorFor(ctx, s.pool).(pgx.Tx); !ok {
		return integrations.ErrAtomicIngestUnavailable
	}
	return s.SaveFacts(ctx, input)
}
