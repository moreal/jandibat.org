package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

type ceremonyPayload struct {
	Challenge       string          `json:"challenge"`
	VerifierSession json.RawMessage `json:"verifier_session"`
}

const ceremonyColumns = `id, kind, COALESCE(user_id, ''), payload, created_at, expires_at, consumed_at`

func (store *Store) SaveCeremony(ctx context.Context, ceremony coreauth.PasskeyCeremony) error {
	if ceremony.ID == "" || ceremony.Challenge == "" || !validJSONObject(ceremony.VerifierSession) ||
		!validCeremonyKind(ceremony.Kind) || !ceremony.ExpiresAt.After(ceremony.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	payload, err := json.Marshal(ceremonyPayload{Challenge: ceremony.Challenge, VerifierSession: ceremony.VerifierSession})
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	digest := sha256.Sum256([]byte(ceremony.Challenge))
	if store.pool != nil {
		id, err := uuid.Parse(ceremony.ID)
		if err != nil {
			return coreauth.ErrInvalidInput
		}
		userID := ceremony.UserID
		var consumed *string
		if ceremony.ConsumedAt != nil {
			value := nullableTimeText(ceremony.ConsumedAt)
			consumed = &value
		}
		return persistenceError(generated.InsertCeremony(ctx, appdb.PGXExecutorFor(ctx, store.pool), id, &userID, databaseCeremonyKind(ceremony.Kind), digest[:], payload, ceremony.ExpiresAt, consumed, ceremony.CreatedAt))
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO auth_challenges
  (id, user_id, kind, challenge_hash, payload, expires_at, consumed_at, created_at)
VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8)`,
		ceremony.ID, ceremony.UserID, databaseCeremonyKind(ceremony.Kind), digest[:], payload,
		ceremony.ExpiresAt, ceremony.ConsumedAt, ceremony.CreatedAt)
	return persistenceError(err)
}

func (store *Store) ConsumeCeremony(ctx context.Context, id string, kind coreauth.CeremonyKind, now time.Time) (coreauth.PasskeyCeremony, error) {
	if id == "" || !validCeremonyKind(kind) {
		return coreauth.PasskeyCeremony{}, coreauth.ErrInvalidInput
	}
	if store.pool != nil {
		parsedID, err := uuid.Parse(id)
		if err != nil {
			return coreauth.PasskeyCeremony{}, coreauth.ErrInvalidInput
		}
		row, err := generated.ConsumeCeremony(ctx, appdb.PGXExecutorFor(ctx, store.pool), parsedID, databaseCeremonyKind(kind), now)
		if err != nil {
			return coreauth.PasskeyCeremony{}, persistenceError(err)
		}
		if row != nil {
			return ceremonyFromGenerated(*row)
		}
		return coreauth.PasskeyCeremony{}, store.ceremonyStatePGX(ctx, parsedID, kind, now)
	}
	// Known missing/expired/replayed ceremonies do not need a transaction. This
	// lets the HTTP audit boundary distinguish a deliberate post-claim verifier
	// rejection from a replay that changed no durable state. The conditional
	// UPDATE below remains the authority for concurrent consumers.
	if _, active := appdb.Transaction(ctx, store.db); !active && appdb.HasLazyTransaction(ctx, store.db) {
		if stateErr := store.ceremonyState(ctx, id, kind, now); !errors.Is(stateErr, coreauth.ErrConflict) {
			return coreauth.PasskeyCeremony{}, stateErr
		}
	}
	executor, err := store.mutationExecutor(ctx)
	if err != nil {
		return coreauth.PasskeyCeremony{}, err
	}
	row := executor.QueryRowContext(ctx, `
UPDATE auth_challenges
SET consumed_at = $3
WHERE id = $1 AND kind = $2 AND consumed_at IS NULL AND expires_at > $3
RETURNING `+ceremonyColumns, id, databaseCeremonyKind(kind), now)
	ceremony, err := scanCeremony(row)
	if !errors.Is(err, coreauth.ErrNotFound) {
		return ceremony, err
	}
	return coreauth.PasskeyCeremony{}, store.ceremonyState(ctx, id, kind, now)
}

func (store *Store) ceremonyStatePGX(ctx context.Context, id uuid.UUID, kind coreauth.CeremonyKind, now time.Time) error {
	row, err := generated.GetCeremonyState(ctx, appdb.PGXExecutorFor(ctx, store.pool), id)
	if err != nil {
		return persistenceError(err)
	}
	if row == nil || row.Kind != databaseCeremonyKind(kind) {
		return coreauth.ErrNotFound
	}
	if row.ConsumedAt != nil {
		return coreauth.ErrConsumed
	}
	if !now.Before(row.ExpiresAt) {
		return coreauth.ErrExpired
	}
	return coreauth.ErrConflict
}

func ceremonyFromGenerated(row generated.ConsumeCeremonyRow) (coreauth.PasskeyCeremony, error) {
	kind := applicationCeremonyKind(row.Kind)
	var payload ceremonyPayload
	if kind == "" || json.Unmarshal(row.Payload, &payload) != nil || payload.Challenge == "" || !validJSONObject(payload.VerifierSession) {
		return coreauth.PasskeyCeremony{}, coreauth.ErrInvalidInput
	}
	ceremony := coreauth.PasskeyCeremony{ID: row.Id, Kind: kind, Challenge: payload.Challenge, UserID: row.UserId, VerifierSession: append(json.RawMessage(nil), payload.VerifierSession...), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
	if row.ConsumedAt != nil {
		value := *row.ConsumedAt
		ceremony.ConsumedAt = &value
	}
	return ceremony, nil
}

func (store *Store) ceremonyState(ctx context.Context, id string, kind coreauth.CeremonyKind, now time.Time) error {
	var actualKind string
	var consumed sql.NullTime
	var expires time.Time
	err := store.db.QueryRowContext(ctx, `
SELECT kind, consumed_at, expires_at FROM auth_challenges WHERE id = $1`, id).Scan(&actualKind, &consumed, &expires)
	if err == sql.ErrNoRows || (err == nil && actualKind != databaseCeremonyKind(kind)) {
		return coreauth.ErrNotFound
	}
	if err != nil {
		return persistenceError(err)
	}
	if consumed.Valid {
		return coreauth.ErrConsumed
	}
	if !now.Before(expires) {
		return coreauth.ErrExpired
	}
	return coreauth.ErrConflict
}

func scanCeremony(row scanner) (coreauth.PasskeyCeremony, error) {
	var ceremony coreauth.PasskeyCeremony
	var databaseKind string
	var payloadBytes []byte
	var consumed sql.NullTime
	if err := row.Scan(&ceremony.ID, &databaseKind, &ceremony.UserID, &payloadBytes,
		&ceremony.CreatedAt, &ceremony.ExpiresAt, &consumed); err != nil {
		if err == sql.ErrNoRows {
			return coreauth.PasskeyCeremony{}, coreauth.ErrNotFound
		}
		return coreauth.PasskeyCeremony{}, persistenceError(err)
	}
	ceremony.Kind = applicationCeremonyKind(databaseKind)
	var payload ceremonyPayload
	if ceremony.Kind == "" || json.Unmarshal(payloadBytes, &payload) != nil || payload.Challenge == "" || !validJSONObject(payload.VerifierSession) {
		return coreauth.PasskeyCeremony{}, coreauth.ErrInvalidInput
	}
	ceremony.Challenge = payload.Challenge
	ceremony.VerifierSession = append(json.RawMessage(nil), payload.VerifierSession...)
	if consumed.Valid {
		value := consumed.Time
		ceremony.ConsumedAt = &value
	}
	return ceremony, nil
}

func databaseCeremonyKind(kind coreauth.CeremonyKind) string {
	if kind == coreauth.CeremonyRegistration {
		return "passkey_registration"
	}
	if kind == coreauth.CeremonyAuthentication {
		return "passkey_authentication"
	}
	return ""
}

func applicationCeremonyKind(kind string) coreauth.CeremonyKind {
	if kind == "passkey_registration" {
		return coreauth.CeremonyRegistration
	}
	if kind == "passkey_authentication" {
		return coreauth.CeremonyAuthentication
	}
	return ""
}

func validCeremonyKind(kind coreauth.CeremonyKind) bool {
	return kind == coreauth.CeremonyRegistration || kind == coreauth.CeremonyAuthentication
}

func validJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	trimmed := strings.TrimSpace(string(value))
	return len(trimmed) > 1 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}
