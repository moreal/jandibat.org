package cockroach

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

var (
	_ coreauth.MagicLinkDeliveryIssuer     = (*Store)(nil)
	_ coreauth.MagicLinkDeliveryRepository = (*Store)(nil)
)

// CheckMagicLinkDeliverySchema is a read-only startup probe for the worker's
// exact, FK-free outbox contract. In particular, there is no bearer-token or
// decryptable-token column and the worker needs no users/token-table access.
func (store *Store) CheckMagicLinkDeliverySchema(ctx context.Context) error {
	if store.pool == nil {
		return ErrNilDB
	}
	_, err := generated.ProbeMagicLinkDeliverySchema(ctx, appdb.PGXExecutorFor(ctx, store.pool))
	if err != nil {
		return fmt.Errorf("auth cockroach: Magic Link delivery schema is incomplete: %w", err)
	}
	return nil
}

// SaveMagicLinkDeliveryIntent stores no bearer material. In the same
// transaction it invalidates already-sent links and supersedes pending or
// in-flight jobs for the normalized identity before inserting the new intent.
func (store *Store) SaveMagicLinkDeliveryIntent(ctx context.Context, delivery coreauth.MagicLinkDelivery) error {
	if delivery.ID == "" || delivery.RecipientEmail == "" || !delivery.Purpose.Valid() ||
		delivery.Status != coreauth.MagicLinkDeliveryPending || delivery.AvailableAt.IsZero() || delivery.CreatedAt.IsZero() {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
	id, err := uuid.Parse(delivery.ID)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := generated.SupersedeLegacyMagicLinks(txctx, tx, delivery.RecipientEmail, string(delivery.Purpose), delivery.CreatedAt); err != nil {
			return persistenceError(err)
		}
		if err := generated.ConsumeSentDeliveryLinks(txctx, tx, delivery.RecipientEmail, string(delivery.Purpose), delivery.CreatedAt); err != nil {
			return persistenceError(err)
		}
		if err := generated.SupersedePendingDeliveries(txctx, tx, delivery.RecipientEmail, string(delivery.Purpose), delivery.CreatedAt); err != nil {
			return persistenceError(err)
		}
		return persistenceError(generated.InsertDeliveryIntent(txctx, tx, id, delivery.RecipientEmail, delivery.RedirectURI, string(delivery.Purpose), delivery.AvailableAt, delivery.CreatedAt))
	})
}

func (store *Store) ClaimMagicLinkDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit, maxAttempts int) ([]coreauth.MagicLinkDelivery, error) {
	if now.IsZero() || lease <= 0 || limit <= 0 || maxAttempts <= 0 || maxAttempts > 5 {
		return nil, coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return nil, ErrNilDB
	}
	claimToken, err := deliveryClaimUUID()
	if err != nil {
		return nil, err
	}
	claimID, err := uuid.Parse(claimToken)
	if err != nil {
		return nil, err
	}
	var jobs []coreauth.MagicLinkDelivery
	err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		if err := generated.RecoverConsumedDelivery(txctx, tx, now); err != nil {
			return persistenceError(err)
		}
		if err := generated.DeadExpiredDelivery(txctx, tx, now, int32(maxAttempts)); err != nil { // #nosec G115 -- maxAttempts is checked to be 1..5 before this transaction.
			return persistenceError(err)
		}
		if err := generated.ClaimDeliveries(txctx, tx, now, now.Add(lease), claimID, int32(maxAttempts), int64(limit)); err != nil { // #nosec G115 -- maxAttempts is checked to be 1..5 before this transaction.
			return persistenceError(err)
		}
		rows, err := generated.ListClaimedDeliveries(txctx, tx, claimID)
		if err != nil {
			return persistenceError(err)
		}
		jobs = make([]coreauth.MagicLinkDelivery, 0, len(rows))
		for _, row := range rows {
			job, mapErr := deliveryFromGenerated(row)
			if mapErr != nil {
				return mapErr
			}
			jobs = append(jobs, job)
		}
		return nil
	})
	return jobs, err
}

func (store *Store) ActivateMagicLinkDelivery(ctx context.Context, id, claimToken string, link coreauth.MagicLink) error {
	if id == "" || claimToken == "" || link.Email == "" || !link.Purpose.Valid() || !link.ExpiresAt.After(link.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
	deliveryID, err := uuid.Parse(id)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	claimID, err := uuid.Parse(claimToken)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	return appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		locked, err := generated.LockClaimedDelivery(txctx, tx, deliveryID, claimID)
		if err != nil {
			return persistenceError(err)
		}
		if locked == nil || locked.RecipientEmail != link.Email || locked.Purpose != string(link.Purpose) {
			return coreauth.ErrConflict
		}
		count, err := generated.ActivateDeliveryToken(txctx, tx, deliveryID, claimID, link.TokenHash[:], link.ExpiresAt, link.CreatedAt)
		if err != nil {
			return persistenceError(err)
		}
		if count != 1 {
			return coreauth.ErrConflict
		}
		return nil
	})
}

func (store *Store) CompleteMagicLinkDelivery(ctx context.Context, id, claimToken string, now time.Time) error {
	if id == "" || claimToken == "" || now.IsZero() {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
	deliveryID, err := uuid.Parse(id)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	claimID, err := uuid.Parse(claimToken)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	count, err := generated.CompleteDelivery(ctx, appdb.PGXExecutorFor(ctx, store.pool), deliveryID, claimID, now)
	if err != nil {
		return persistenceError(err)
	}
	if count != 1 {
		return coreauth.ErrConflict
	}
	return nil
}

func (store *Store) RetryMagicLinkDelivery(ctx context.Context, id, claimToken string, now, next time.Time, maxAttempts int) (coreauth.MagicLinkDeliveryStatus, error) {
	if id == "" || claimToken == "" || now.IsZero() || !next.After(now) || maxAttempts <= 0 || maxAttempts > 5 {
		return "", coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return "", ErrNilDB
	}
	deliveryID, err := uuid.Parse(id)
	if err != nil {
		return "", coreauth.ErrInvalidInput
	}
	claimID, err := uuid.Parse(claimToken)
	if err != nil {
		return "", coreauth.ErrInvalidInput
	}
	var status coreauth.MagicLinkDeliveryStatus
	err = appdb.InTx(ctx, store.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		row, err := generated.RetryDelivery(txctx, tx, deliveryID, claimID, now, next, int32(maxAttempts)) // #nosec G115 -- maxAttempts is checked to be 1..5 before this transaction.
		if err != nil {
			return persistenceError(err)
		}
		if row == nil {
			return coreauth.ErrConflict
		}
		status = coreauth.MagicLinkDeliveryStatus(row.Status)
		return nil
	})
	return status, err
}

func deliveryFromGenerated(row generated.ListClaimedDeliveriesRow) (coreauth.MagicLinkDelivery, error) {
	purpose := coreauth.MagicLinkPurpose(row.Purpose)
	if !purpose.Valid() || row.Attempts < 0 || row.Attempts > 5 {
		return coreauth.MagicLinkDelivery{}, coreauth.ErrInvalidInput
	}
	job := coreauth.MagicLinkDelivery{ID: row.Id, RecipientEmail: row.RecipientEmail, RedirectURI: row.RedirectUri, Purpose: purpose, Status: coreauth.MagicLinkDeliveryStatus(row.Status), Attempts: int(row.Attempts), AvailableAt: row.AvailableAt, ClaimToken: row.ClaimToken, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, TerminalReason: row.TerminalReason}
	if row.LeaseUntil != nil {
		value := *row.LeaseUntil
		job.LeaseUntil = &value
	}
	if row.TerminalAt != nil {
		value := *row.TerminalAt
		job.TerminalAt = &value
	}
	return job, nil
}

func deliveryClaimUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
