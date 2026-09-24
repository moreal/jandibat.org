package cockroach

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func (s *Store) SaveConnection(ctx context.Context, record integrations.ConnectionRecord) error {
	connection := record.Connection
	if connection.ID == "" {
		return integrations.ErrEmptyConnectionID
	}
	if s.pool == nil {
		return ErrNilDB
	}
	if !connection.PrivateDataEnabled {
		// Defense in depth: an opt-out record cannot persist credentials even if
		// a caller accidentally supplies them. Absence of consent is the durable
		// authority, not the authentication method.
		record.Credentials = integrations.EncryptedCredentials{}
		connection.TokenExpiresAt = nil
	}
	accessKeyID, err := encryptedCredentialKeyID(record.Credentials.AccessToken)
	if err != nil {
		return fmt.Errorf("access token key ID: %w", err)
	}
	refreshKeyID, err := encryptedCredentialKeyID(record.Credentials.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh token key ID: %w", err)
	}
	id, err := uuid.Parse(connection.ID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	if connection.LastSyncAttempt < math.MinInt32 || connection.LastSyncAttempt > math.MaxInt32 ||
		connection.ConsecutiveFailures < math.MinInt32 || connection.ConsecutiveFailures > math.MaxInt32 {
		return integrations.ErrInvalidConnectionStatus
	}
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		var scopes *[]string
		if connection.Scopes != nil {
			value := append([]string(nil), connection.Scopes...)
			scopes = &value
		}
		count, err := generated.UpsertConnection(txctx, tx,
			id, connection.SubjectID, connection.EnvironmentID, string(connection.AuthMethod),
			&connection.ExternalAccountID, string(databaseConnectionStatus(connection.Status)),
			scopes, optionalBytes(record.Credentials.AccessToken), optionalString(accessKeyID),
			optionalBytes(record.Credentials.RefreshToken), optionalString(refreshKeyID),
			connection.TokenExpiresAt, connection.LastSyncedAt, &connection.LastError,
			connection.CreatedAt, connection.UpdatedAt, connection.ProviderID,
			string(connection.Status), optionalTimeText(connection.LastSyncAttemptAt),
			optionalTimeText(connection.NextSyncAttemptAt), int32(connection.LastSyncAttempt), // #nosec G115 -- entry guard checks MinInt32..MaxInt32.
			int32(connection.ConsecutiveFailures), connection.ExternalAccountLogin) // #nosec G115 -- entry guard checks MinInt32..MaxInt32.
		if err != nil {
			return persistenceError(err, integrations.ErrConflict)
		}
		if count != 1 {
			return integrations.ErrInvalidConnectionStatus
		}
		if connection.PrivateDataEnabled {
			err = generated.EnableConnectionPrivateConsent(txctx, tx, id, connection.UpdatedAt)
		} else {
			err = generated.DisableConnectionPrivateConsent(txctx, tx, id)
		}
		return persistenceError(err, integrations.ErrConflict)
	})
}

func optionalBytes(value []byte) *[]byte {
	if len(value) == 0 {
		return nil
	}
	copyValue := append([]byte(nil), value...)
	return &copyValue
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalTimeText(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// UpdateConnectionAfterSync is intentionally UPDATE-only so the worker role
// needs no INSERT privilege on provider_connections. The execution claim and
// status predicate also prevent a stale worker from recreating or reviving a
// disconnected connection.
func (s *Store) UpdateConnectionAfterSync(ctx context.Context, record integrations.ConnectionRecord, claimToken string) error {
	connection := record.Connection
	if connection.ID == "" || claimToken == "" {
		return integrations.ErrInvalidConnectionStatus
	}
	if s.pool == nil {
		return ErrNilDB
	}
	id, err := uuid.Parse(connection.ID)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	if connection.LastSyncAttempt < math.MinInt32 || connection.LastSyncAttempt > math.MaxInt32 ||
		connection.ConsecutiveFailures < math.MinInt32 || connection.ConsecutiveFailures > math.MaxInt32 {
		return integrations.ErrInvalidConnectionStatus
	}
	count, err := generated.UpdateConnectionAfterSync(ctx, appdb.PGXExecutorFor(ctx, s.pool),
		id, string(databaseConnectionStatus(connection.Status)), optionalTimeText(connection.LastSyncedAt),
		connection.LastError, connection.UpdatedAt, string(connection.Status),
		optionalTimeText(connection.LastSyncAttemptAt), optionalTimeText(connection.NextSyncAttemptAt),
		int32(connection.LastSyncAttempt), int32(connection.ConsecutiveFailures), claimToken)
	if err != nil {
		return persistenceError(err, integrations.ErrConflict)
	}
	if count != 1 {
		return integrations.ErrInvalidConnectionStatus
	}
	return nil
}

func encryptedCredentialKeyID(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	keyID, enveloped, err := operations.CiphertextKeyID(ciphertext)
	if err != nil {
		return "", err
	}
	if !enveloped {
		return "", nil
	}
	return keyID, nil
}

func databaseConnectionStatus(status integrations.ConnectionStatus) integrations.ConnectionStatus {
	// Migration 0001 predates the OAuth pending state. Keep its constraint
	// satisfied while preserving the richer state in sync_cursor.
	if status == integrations.ConnectionPending {
		return integrations.ConnectionActive
	}
	return status
}

func (s *Store) GetConnection(ctx context.Context, id string) (integrations.ConnectionRecord, error) {
	if s.pool == nil {
		return integrations.ConnectionRecord{}, ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.ConnectionRecord{}, integrations.ErrInvalidIdentifier
	}
	row, err := generated.GetConnectionById(ctx, appdb.PGXExecutorFor(ctx, s.pool), parsed)
	if err != nil {
		return integrations.ConnectionRecord{}, fmt.Errorf("get connection: %w", err)
	}
	if row == nil {
		return integrations.ConnectionRecord{}, notFound("connection", id)
	}
	return connectionFromGenerated(*row)
}

func connectionFromGenerated(row generated.GetConnectionByIdRow) (integrations.ConnectionRecord, error) {
	record := integrations.ConnectionRecord{Connection: integrations.ProviderConnection{
		ID: row.Id, SubjectID: row.SubjectId, ProviderID: row.ProviderId,
		EnvironmentID: row.EnvironmentId, AuthMethod: integrations.AuthMethod(row.AuthMethod),
		ExternalAccountID: row.ExternalAccountId, ExternalAccountLogin: row.ExternalAccountLogin,
		Status:             integrations.ConnectionStatus(row.ConnectionStatus),
		PrivateDataEnabled: row.PrivateDataEnabled, LastSyncAttempt: int(row.LastSyncAttempt),
		ConsecutiveFailures: int(row.ConsecutiveFailures), LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}}
	connection := &record.Connection
	if err := json.Unmarshal(row.ScopesJson, &connection.Scopes); err != nil {
		return integrations.ConnectionRecord{}, fmt.Errorf("decode scopes: %w", err)
	}
	if connection.Scopes == nil {
		connection.Scopes = []string{}
	}
	connection.TokenExpiresAt = row.TokenExpiresAt
	connection.LastSyncedAt = row.LastSyncedAt
	connection.LastSyncAttemptAt = row.LastSyncAttemptAt
	connection.NextSyncAttemptAt = row.NextSyncAttemptAt
	if row.AccessTokenCiphertext != nil {
		record.Credentials.AccessToken = append([]byte(nil), (*row.AccessTokenCiphertext)...)
	}
	if row.RefreshTokenCiphertext != nil {
		record.Credentials.RefreshToken = append([]byte(nil), (*row.RefreshTokenCiphertext)...)
	}
	return record, nil
}

func (s *Store) ListConnections(ctx context.Context, subjectID string) ([]integrations.ConnectionRecord, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	rows, err := generated.ListConnections(ctx, appdb.PGXExecutorFor(ctx, s.pool), subjectID)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	records := make([]integrations.ConnectionRecord, 0, len(rows))
	for _, row := range rows {
		record, err := connectionFromGenerated(generated.GetConnectionByIdRow(row))
		if err != nil {
			return nil, fmt.Errorf("list connections: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

// PurgeConnectionData removes facts and sync jobs in one transaction while
// retaining the sanitized revoked tombstone for bounded operational retention.
// Public connections share a global environment and must not purge its facts.
func (s *Store) PurgeConnectionData(ctx context.Context, id string) error {
	if id == "" {
		return integrations.ErrEmptyConnectionID
	}
	if s.pool == nil {
		return ErrNilDB
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return integrations.ErrInvalidIdentifier
	}
	return appdb.InTx(ctx, s.pool, appdb.RetryOptions{}, func(txctx context.Context, tx pgx.Tx) error {
		row, err := generated.LockConnectionForRevocation(txctx, tx, parsed)
		if err != nil {
			return fmt.Errorf("purge connection data: load aggregate: %w", err)
		}
		if row == nil {
			return notFound("connection", id)
		}
		if integrations.ConnectionStatus(row.ConnectionStatus) != integrations.ConnectionRevoked {
			return integrations.ErrInvalidConnectionStatus
		}
		if integrations.AuthMethod(row.AuthMethod) != integrations.AuthNone {
			changes, err := generated.ListConnectionFactChanges(txctx, tx, row.SubjectId, row.EnvironmentId)
			if err != nil {
				return fmt.Errorf("purge connection data: load fact changes: %w", err)
			}
			if err := generated.PurgeConnectionFacts(txctx, tx, row.SubjectId, row.EnvironmentId); err != nil {
				return fmt.Errorf("purge connection data: purge facts: %w", err)
			}
			for _, change := range changes {
				if err := touchActivitySnapshotChange(txctx, tx, change.SubjectId, change.EnvironmentId, change.ActivityDate, change.VisibilityScope); err != nil {
					return fmt.Errorf("purge connection data: mark changed facts: %w", err)
				}
			}
		}
		if err := generated.PurgeConnectionSyncJobs(txctx, tx, parsed); err != nil {
			return fmt.Errorf("purge connection data: purge sync jobs: %w", err)
		}
		return nil
	})
}
