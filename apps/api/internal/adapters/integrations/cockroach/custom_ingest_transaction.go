package cockroach

import (
	"context"
	"crypto/subtle"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var _ integrations.AtomicCustomIngestStore = (*Store)(nil)

// RunAuthenticatedIngest holds the provider and credential rows through the
// reservation, ledger, fact, and completion writes. A credential rotation or
// disable either commits before this check or waits until this ingest commits.
func (s *Store) RunAuthenticatedIngest(
	ctx context.Context,
	providerID string,
	expectedSecretDigest []byte,
	callback func(context.Context, integrations.CustomProviderRecord) error,
) error {
	if s.pool == nil {
		return ErrNilDB
	}
	if callback == nil {
		return appdb.ErrCallbackRequired
	}
	id, err := uuid.Parse(providerID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		provider, err := generated.LockProviderForIngest(txctx, tx, id)
		if err != nil {
			return fmt.Errorf("fence custom ingest provider: %w", err)
		}
		if provider == nil {
			return notFound("custom provider", providerID)
		}
		if provider.Status != string(integrations.CustomProviderActive) {
			return integrations.ErrProviderDisabled
		}
		secret, err := generated.LockCustomProviderSecretForIngest(txctx, tx, id)
		if err != nil {
			return fmt.Errorf("fence custom ingest credential: %w", err)
		}
		if secret == nil || subtle.ConstantTimeCompare(secret.IngestTokenHash, expectedSecretDigest) != 1 {
			return integrations.ErrUnauthorized
		}
		current, err := s.GetCustomProvider(txctx, providerID)
		if err != nil {
			return fmt.Errorf("reload locked custom provider: %w", err)
		}
		return callback(txctx, current)
	})
}
