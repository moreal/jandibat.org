package cockroach

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func pageTuple(subjectID string, after *integrations.ConnectionCursor, limit int) (time.Time, uuid.UUID, error) {
	if subjectID == "" {
		return time.Time{}, uuid.Nil, integrations.ErrEmptySubjectID
	}
	if limit < 1 || limit > 101 {
		return time.Time{}, uuid.Nil, integrations.ErrInvalidIntegrationPageSize
	}
	if after == nil {
		return time.Unix(0, 0).UTC(), uuid.Nil, nil
	}
	id, err := uuid.Parse(after.ID)
	if after.CreatedAt.IsZero() || err != nil || id.String() != after.ID {
		return time.Time{}, uuid.Nil, integrations.ErrInvalidIdentifier
	}
	return after.CreatedAt, id, nil
}

func (s *Store) ListConnectionsPage(ctx context.Context, subjectID string, after *integrations.ConnectionCursor, limit int) ([]integrations.ProviderConnection, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	createdAt, afterID, err := pageTuple(subjectID, after, limit)
	if err != nil {
		return nil, err
	}
	rows, err := generated.ListConnectionsPage(ctx, appdb.PGXExecutorFor(ctx, s.pool), subjectID, after != nil, createdAt, afterID, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list connections page: %w", err)
	}
	connections := make([]integrations.ProviderConnection, 0, len(rows))
	for _, row := range rows {
		connection := integrations.ProviderConnection{
			ID: row.Id, SubjectID: row.SubjectId, ProviderID: row.ProviderId,
			EnvironmentID: row.EnvironmentId, AuthMethod: integrations.AuthMethod(row.AuthMethod),
			ExternalAccountID: row.ExternalAccountId, ExternalAccountLogin: row.ExternalAccountLogin,
			Status:             integrations.ConnectionStatus(row.ConnectionStatus),
			PrivateDataEnabled: row.PrivateDataEnabled, TokenExpiresAt: row.TokenExpiresAt,
			LastSyncedAt: row.LastSyncedAt, LastSyncAttemptAt: row.LastSyncAttemptAt,
			NextSyncAttemptAt: row.NextSyncAttemptAt, LastSyncAttempt: int(row.LastSyncAttempt),
			ConsecutiveFailures: int(row.ConsecutiveFailures), LastError: row.LastError,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		if err := json.Unmarshal(row.ScopesJson, &connection.Scopes); err != nil {
			return nil, fmt.Errorf("decode connection page scopes: %w", err)
		}
		if connection.Scopes == nil {
			connection.Scopes = []string{}
		}
		connections = append(connections, connection)
	}
	return connections, nil
}

func (s *Store) ListCustomProvidersPage(ctx context.Context, subjectID string, after *integrations.CustomProviderCursor, limit int) ([]integrations.CustomProvider, error) {
	if s.pool == nil {
		return nil, ErrNilDB
	}
	createdAt, afterID, err := pageTuple(subjectID, after, limit)
	if err != nil {
		return nil, err
	}
	rows, err := generated.ListCustomProvidersPage(ctx, appdb.PGXExecutorFor(ctx, s.pool), subjectID, after != nil, createdAt, afterID, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list custom providers page: %w", err)
	}
	providers := make([]integrations.CustomProvider, 0, len(rows))
	for _, row := range rows {
		provider := integrations.CustomProvider{
			ID: row.Id, SubjectID: row.SubjectId, EnvironmentID: row.EnvironmentId,
			Slug: row.Slug, Name: row.Name, Description: row.Description,
			Status: integrations.CustomProviderStatus(row.Status), CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		}
		var configuration customProviderConfiguration
		if err := json.Unmarshal(row.Configuration, &configuration); err != nil {
			return nil, fmt.Errorf("decode custom provider page configuration: %w", err)
		}
		provider.AllowedActions = nonNilStrings(configuration.AllowedActions)
		provider.AllowedMetrics = nonNilStrings(configuration.AllowedMetrics)
		providers = append(providers, provider)
	}
	return providers, nil
}
