package cockroach

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach/generated"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

func (store *Store) SaveCredential(ctx context.Context, credential coreauth.PasskeyCredential) error {
	if credential.ID == "" || credential.UserID == "" || len(credential.CredentialID) == 0 ||
		len(credential.PublicKey) == 0 || !validJSONObject(credential.VerifierCredential) {
		return coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return ErrNilDB
	}
	id, err := uuid.Parse(credential.ID)
	if err != nil {
		return coreauth.ErrInvalidInput
	}
	var aaguid *[]byte
	if len(credential.AAGUID) != 0 {
		value := append([]byte(nil), credential.AAGUID...)
		aaguid = &value
	}
	var transports *[]string
	if credential.Transports != nil {
		value := append([]string(nil), credential.Transports...)
		transports = &value
	}
	label := credential.Label
	return persistenceError(generated.InsertCredential(ctx, appdb.PGXExecutorFor(ctx, store.pool), id, credential.UserID, credential.CredentialID, credential.PublicKey, aaguid, int64(credential.SignCount), transports, credential.VerifierCredential, &label, credential.CreatedAt, credential.LastUsedAt))
}

func (store *Store) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (coreauth.PasskeyCredential, error) {
	if len(credentialID) == 0 {
		return coreauth.PasskeyCredential{}, coreauth.ErrInvalidInput
	}
	if store.pool == nil {
		return coreauth.PasskeyCredential{}, ErrNilDB
	}
	row, err := generated.GetCredentialByCredentialId(ctx, appdb.PGXExecutorFor(ctx, store.pool), credentialID)
	if err != nil {
		return coreauth.PasskeyCredential{}, persistenceError(err)
	}
	if row == nil {
		return coreauth.PasskeyCredential{}, coreauth.ErrNotFound
	}
	return credentialFromGenerated(*row)
}

func (store *Store) ListCredentialsByUser(ctx context.Context, userID string) ([]coreauth.PasskeyCredential, error) {
	if store.pool == nil {
		return nil, ErrNilDB
	}
	rows, err := generated.ListCredentialsByUser(ctx, appdb.PGXExecutorFor(ctx, store.pool), userID)
	if err != nil {
		return nil, persistenceError(err)
	}
	items := make([]coreauth.PasskeyCredential, 0, len(rows))
	for _, row := range rows {
		item, mapErr := credentialFromGenerated(generated.GetCredentialByCredentialIdRow(row))
		if mapErr != nil {
			return nil, mapErr
		}
		items = append(items, item)
	}
	return items, nil
}

func (store *Store) UseCredential(ctx context.Context, credentialID []byte, previous, next uint32, verifierCredential json.RawMessage, now time.Time) error {
	if len(credentialID) == 0 || !validJSONObject(verifierCredential) {
		return coreauth.ErrInvalidInput
	}
	if counterDidNotAdvance(previous, next) {
		return coreauth.ErrConflict
	}
	if store.pool == nil {
		return ErrNilDB
	}
	executor := appdb.PGXExecutorFor(ctx, store.pool)
	count, err := generated.UpdateCredentialUse(ctx, executor, credentialID, int64(previous), int64(next), verifierCredential, now)
	if err != nil {
		return persistenceError(err)
	}
	if count != 0 {
		return nil
	}
	exists, err := generated.GetCredentialExists(ctx, executor, credentialID)
	if err != nil {
		return persistenceError(err)
	}
	if !exists.Exists {
		return coreauth.ErrNotFound
	}
	return coreauth.ErrConflict
}

func credentialFromGenerated(row generated.GetCredentialByCredentialIdRow) (coreauth.PasskeyCredential, error) {
	if row.SignCount < 0 || row.SignCount > math.MaxUint32 || !validJSONObject(row.VerifierCredential) {
		return coreauth.PasskeyCredential{}, coreauth.ErrInvalidInput
	}
	credential := coreauth.PasskeyCredential{ID: row.Id, UserID: row.UserId, CredentialID: append([]byte(nil), row.CredentialId...), PublicKey: append([]byte(nil), row.PublicKey...), SignCount: uint32(row.SignCount), VerifierCredential: append(json.RawMessage(nil), row.VerifierCredential...), Label: row.Label, CreatedAt: row.CreatedAt}
	if row.Aaguid != nil {
		credential.AAGUID = append([]byte(nil), (*row.Aaguid)...)
	}
	if row.Transports != nil {
		credential.Transports = append([]string(nil), (*row.Transports)...)
	}
	if row.LastUsedAt != nil {
		value := *row.LastUsedAt
		credential.LastUsedAt = &value
	}
	return credential, nil
}

func counterDidNotAdvance(previous, next uint32) bool {
	return (previous != 0 || next != 0) && next <= previous
}
