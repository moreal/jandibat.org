package cockroach

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

var errReencryptionRequiresPGXPool = errors.New("operations cockroach: re-encryption requires a pgx pool")

func (store *Store) ListSecretsForReencryption(ctx context.Context, after operations.SecretLocator, limit int) ([]operations.EncryptedSecretRecord, error) {
	if limit <= 0 {
		return nil, operations.ErrInvalidReencryptionConfig
	}
	if store.pool == nil {
		return nil, errReencryptionRequiresPGXPool
	}
	// Do not filter by key_id: version-1 envelopes may already carry the
	// active key ID. The worker inspects each bounded Scythe-generated page.
	rows, err := generated.ListEncryptedSecretsForReencryption(ctx, appdb.PGXExecutorFor(ctx, store.pool),
		string(after.Kind), after.ID, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list secrets for re-encryption: %w", err)
	}
	result := make([]operations.EncryptedSecretRecord, 0, len(rows))
	for _, row := range rows {
		if row.Ciphertext == nil {
			return nil, operations.ErrInvalidSecretRecord
		}
		result = append(result, operations.EncryptedSecretRecord{
			Locator: operations.SecretLocator{Kind: operations.SecretKind(row.Kind), ID: row.Id},
			KeyID:   row.KeyId, Ciphertext: append([]byte(nil), (*row.Ciphertext)...),
		})
	}
	return result, nil
}

func (store *Store) ReplaceEncryptedSecret(ctx context.Context, expected operations.EncryptedSecretRecord, newKeyID string, ciphertext []byte) (bool, error) {
	if newKeyID == "" || len(ciphertext) == 0 {
		return false, operations.ErrInvalidReencryptionConfig
	}
	if store.pool == nil {
		return false, errReencryptionRequiresPGXPool
	}
	id, err := uuid.Parse(expected.Locator.ID)
	if err != nil {
		return false, fmt.Errorf("replace encrypted secret %s/%s: %w", expected.Locator.Kind, expected.Locator.ID, err)
	}
	var count int64
	err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		var queryErr error
		switch expected.Locator.Kind {
		case operations.SecretConnectionAccessToken:
			count, queryErr = generated.ReplaceConnectionAccessToken(txctx, tx, id, expected.Ciphertext, ciphertext, newKeyID, expected.KeyID)
		case operations.SecretConnectionRefreshToken:
			count, queryErr = generated.ReplaceConnectionRefreshToken(txctx, tx, id, expected.Ciphertext, ciphertext, newKeyID, expected.KeyID)
		case operations.SecretOAuthRevocationToken:
			count, queryErr = generated.ReplaceOAuthRevocationToken(txctx, tx, id, expected.Ciphertext, ciphertext, newKeyID, expected.KeyID)
		default:
			return fmt.Errorf("%w: unknown kind %q", operations.ErrInvalidSecretRecord, expected.Locator.Kind)
		}
		return queryErr
	})
	if err != nil {
		return false, fmt.Errorf("replace encrypted secret %s/%s: %w", expected.Locator.Kind, expected.Locator.ID, err)
	}
	return count == 1, nil
}
