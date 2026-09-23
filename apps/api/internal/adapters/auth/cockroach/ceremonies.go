package cockroach

import (
	"context"
	"crypto/sha256"
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

func (store *Store) SaveCeremony(ctx context.Context, ceremony coreauth.PasskeyCeremony) error {
	if ceremony.ID == "" || ceremony.Challenge == "" || !validJSONObject(ceremony.VerifierSession) ||
		!validCeremonyKind(ceremony.Kind) || !ceremony.ExpiresAt.After(ceremony.CreatedAt) {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
	payload, digest, err := encodeCeremony(ceremony)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
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

func encodeCeremony(ceremony coreauth.PasskeyCeremony) ([]byte, [sha256.Size]byte, error) {
	payload, err := json.Marshal(ceremonyPayload{Challenge: ceremony.Challenge, VerifierSession: ceremony.VerifierSession})
	return payload, sha256.Sum256([]byte(ceremony.Challenge)), err
}

func (store *Store) ConsumeCeremony(ctx context.Context, id string, kind coreauth.CeremonyKind, now time.Time) (coreauth.PasskeyCeremony, error) {
	if id == "" || !validCeremonyKind(kind) {
		return coreauth.PasskeyCeremony{}, coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return coreauth.PasskeyCeremony{}, ErrNilDB
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return coreauth.PasskeyCeremony{}, coreauth.ErrInvalidInput
	}
	if appdb.HasPendingLazyPGXTransaction(ctx, store.pool) {
		if stateErr := store.ceremonyStatePGX(ctx, parsedID, kind, now); !errors.Is(stateErr, coreauth.ErrConflict) {
			return coreauth.PasskeyCeremony{}, stateErr
		}
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
