package graphql

import (
	"context"
	"errors"

	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

// IntegrationQueryServices are subject-scoped application ports installed by
// the trusted transport, never from client-supplied GraphQL values.
type IntegrationQueryServices struct {
	Connections interface {
		ListPage(context.Context, string, *integrations.ConnectionCursor, int) (integrations.ConnectionPage, error)
	}
	CustomProviders interface {
		ListPage(context.Context, string, *integrations.CustomProviderCursor, int) (integrations.CustomProviderPage, error)
	}
}

type integrationQueryServicesContextKey struct{}

func ContextWithIntegrationQueryServices(ctx context.Context, services IntegrationQueryServices) context.Context {
	return context.WithValue(ctx, integrationQueryServicesContextKey{}, services)
}

func resolveProviderCatalog(ctx context.Context) ([]*model.ProviderCatalogItem, error) {
	items, err := integrations.NewCatalogService(nil).List(ctx, "")
	if err != nil || len(items) != 3 {
		return nil, errNodeLookup
	}
	result := make([]*model.ProviderCatalogItem, 0, len(items))
	for _, item := range items {
		if item.Kind == integrations.ProviderCustom || item.CustomProviderID != "" {
			return nil, errNodeLookup
		}
		result = append(result, &model.ProviderCatalogItem{
			ID: item.ID, Name: item.Name, Description: item.Description,
			Kind: string(item.Kind), Category: string(item.Category),
			SupportsOAuth: item.SupportsOAuth, SupportsToken: item.SupportsToken,
			SupportsPrivateData: item.SupportsPrivateData,
		})
	}
	return result, nil
}

func integrationPageScope(ctx context.Context, parent *model.Subject, first *int, after *scalar.Cursor, kind cursor.Kind) (string, int, *integrations.ConnectionCursor, bool, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return "", 0, nil, false, errNodeAuthentication
	}
	if identity.userID == "" || parent == nil {
		return "", 0, nil, false, nil
	}
	subjectID, err := relayid.DecodeAs(relayid.Subject, parent.ID)
	if err != nil {
		return "", 0, nil, false, errInvalidNodeID
	}
	limit := 25
	if first != nil {
		limit = *first
	}
	if limit < 1 || limit > 100 {
		return "", 0, nil, false, snapshotUserError("BAD_USER_INPUT", "first must be between 1 and 100")
	}
	var position *integrations.ConnectionCursor
	if after != nil {
		decoded, err := cursor.DecodeAs(kind, string(*after))
		if err != nil {
			return "", 0, nil, false, snapshotUserError("BAD_USER_INPUT", "invalid integration cursor")
		}
		position = &integrations.ConnectionCursor{CreatedAt: decoded.Timestamp, ID: decoded.ID}
	}
	nodes, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	if nodes.Subjects == nil {
		return "", 0, nil, false, errNodeLookup
	}
	owned, err := ownsSubject(ctx, nodes.Subjects, identity.userID, subjectID)
	if err != nil {
		return "", 0, nil, false, errNodeLookup
	}
	if !owned {
		return "", 0, nil, false, nil
	}
	return subjectID, limit, position, true, nil
}

func resolveProviderConnections(ctx context.Context, parent *model.Subject, first *int, after *scalar.Cursor) (*model.ProviderConnectionConnection, error) {
	subjectID, limit, position, owned, err := integrationPageScope(ctx, parent, first, after, cursor.ProviderConnection)
	if err != nil || !owned {
		return nil, err
	}
	services, _ := ctx.Value(integrationQueryServicesContextKey{}).(IntegrationQueryServices)
	if services.Connections == nil {
		return nil, errNodeLookup
	}
	page, err := services.Connections.ListPage(ctx, subjectID, position, limit)
	if errors.Is(err, integrations.ErrInvalidIntegrationPageSize) || errors.Is(err, integrations.ErrInvalidIdentifier) {
		return nil, snapshotUserError("BAD_USER_INPUT", "invalid integration page")
	}
	if err != nil || len(page.Connections) > limit {
		return nil, errNodeLookup
	}
	result := &model.ProviderConnectionConnection{Edges: make([]*model.ProviderConnectionEdge, 0, len(page.Connections)), PageInfo: &model.PageInfo{HasNextPage: page.HasNextPage, HasPreviousPage: after != nil}}
	previous := position
	for _, connection := range page.Connections {
		if connection.SubjectID != subjectID || connection.Status == integrations.ConnectionRevoked || connection.CreatedAt.IsZero() || connection.UpdatedAt.IsZero() {
			return nil, errNodeLookup
		}
		current := cursor.Position{Timestamp: connection.CreatedAt, ID: connection.ID}
		if previous != nil && compareSyncJobPosition(current, cursor.Position{Timestamp: previous.CreatedAt, ID: previous.ID}) <= 0 {
			return nil, errNodeLookup
		}
		encoded, err := cursor.Encode(cursor.ProviderConnection, current)
		if err != nil {
			return nil, errNodeLookup
		}
		projected := projectProviderConnection(connection)
		if projected.ID == "" {
			return nil, errNodeLookup
		}
		result.Edges = append(result.Edges, &model.ProviderConnectionEdge{Cursor: scalar.Cursor(encoded), Node: projected})
		previous = &integrations.ConnectionCursor{CreatedAt: connection.CreatedAt, ID: connection.ID}
	}
	if len(result.Edges) != 0 {
		start, end := result.Edges[0].Cursor, result.Edges[len(result.Edges)-1].Cursor
		result.PageInfo.StartCursor, result.PageInfo.EndCursor = &start, &end
	}
	return result, nil
}

func resolveCustomProviders(ctx context.Context, parent *model.Subject, first *int, after *scalar.Cursor) (*model.CustomProviderConnection, error) {
	subjectID, limit, position, owned, err := integrationPageScope(ctx, parent, first, after, cursor.CustomProvider)
	if err != nil || !owned {
		return nil, err
	}
	services, _ := ctx.Value(integrationQueryServicesContextKey{}).(IntegrationQueryServices)
	if services.CustomProviders == nil {
		return nil, errNodeLookup
	}
	page, err := services.CustomProviders.ListPage(ctx, subjectID, position, limit)
	if errors.Is(err, integrations.ErrInvalidIntegrationPageSize) || errors.Is(err, integrations.ErrInvalidIdentifier) {
		return nil, snapshotUserError("BAD_USER_INPUT", "invalid integration page")
	}
	if err != nil || len(page.Providers) > limit {
		return nil, errNodeLookup
	}
	result := &model.CustomProviderConnection{Edges: make([]*model.CustomProviderEdge, 0, len(page.Providers)), PageInfo: &model.PageInfo{HasNextPage: page.HasNextPage, HasPreviousPage: after != nil}}
	previous := position
	for _, provider := range page.Providers {
		if provider.SubjectID != subjectID || provider.CreatedAt.IsZero() || provider.UpdatedAt.IsZero() {
			return nil, errNodeLookup
		}
		current := cursor.Position{Timestamp: provider.CreatedAt, ID: provider.ID}
		if previous != nil && compareSyncJobPosition(current, cursor.Position{Timestamp: previous.CreatedAt, ID: previous.ID}) <= 0 {
			return nil, errNodeLookup
		}
		encoded, err := cursor.Encode(cursor.CustomProvider, current)
		if err != nil {
			return nil, errNodeLookup
		}
		projected := projectCustomProvider(provider)
		if projected.ID == "" {
			return nil, errNodeLookup
		}
		result.Edges = append(result.Edges, &model.CustomProviderEdge{Cursor: scalar.Cursor(encoded), Node: projected})
		previous = &integrations.ConnectionCursor{CreatedAt: provider.CreatedAt, ID: provider.ID}
	}
	if len(result.Edges) != 0 {
		start, end := result.Edges[0].Cursor, result.Edges[len(result.Edges)-1].Cursor
		result.PageInfo.StartCursor, result.PageInfo.EndCursor = &start, &end
	}
	return result, nil
}

func projectProviderConnection(connection integrations.ProviderConnection) *model.ProviderConnection {
	scopes := append([]string{}, connection.Scopes...)
	return &model.ProviderConnection{
		ID:         relayid.Encode(relayid.ProviderConnection, connection.ID),
		ProviderID: connection.ProviderID, EnvironmentID: connection.EnvironmentID,
		AuthMethod: string(connection.AuthMethod), Status: string(connection.Status),
		ExternalAccountID: connection.ExternalAccountID, Scopes: scopes,
		PrivateDataEnabled: connection.PrivateDataEnabled,
		TokenExpiresAt:     optionalDateTime(connection.TokenExpiresAt), LastSyncedAt: optionalDateTime(connection.LastSyncedAt),
		CreatedAt: scalar.DateTime(connection.CreatedAt), UpdatedAt: scalar.DateTime(connection.UpdatedAt),
	}
}

func projectCustomProvider(provider integrations.CustomProvider) *model.CustomProvider {
	return &model.CustomProvider{
		ID:               relayid.Encode(relayid.CustomProvider, provider.ID),
		IngestProviderID: provider.ID,
		EnvironmentID:    provider.EnvironmentID, Slug: provider.Slug, Name: provider.Name,
		Description: provider.Description, Status: string(provider.Status),
		AllowedActions: append([]string{}, provider.AllowedActions...),
		AllowedMetrics: append([]string{}, provider.AllowedMetrics...),
		CreatedAt:      scalar.DateTime(provider.CreatedAt), UpdatedAt: scalar.DateTime(provider.UpdatedAt),
	}
}
