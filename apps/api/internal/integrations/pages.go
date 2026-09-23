package integrations

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ConnectionCursor is an ascending (created_at, id) tuple. The anchor need
// not still exist when the next page is requested.
type ConnectionCursor struct {
	CreatedAt time.Time
	ID        string
}

type CustomProviderCursor = ConnectionCursor

type ConnectionPage struct {
	Connections []ProviderConnection
	HasNextPage bool
}

type CustomProviderPage struct {
	Providers   []CustomProvider
	HasNextPage bool
}

// These page stores return public models only: neither encrypted connection
// credentials nor the custom ingest secret hash may cross this boundary.
type ConnectionPageStore interface {
	ListConnectionsPage(context.Context, string, *ConnectionCursor, int) ([]ProviderConnection, error)
}

type CustomProviderPageStore interface {
	ListCustomProvidersPage(context.Context, string, *CustomProviderCursor, int) ([]CustomProvider, error)
}

func validateIntegrationPage(subjectID string, after *ConnectionCursor, limit, maximum int) error {
	if subjectID == "" {
		return ErrEmptySubjectID
	}
	if limit < 1 || limit > maximum {
		return ErrInvalidIntegrationPageSize
	}
	if after != nil {
		id, err := uuid.Parse(after.ID)
		if after.CreatedAt.IsZero() || err != nil || id.String() != after.ID {
			return ErrInvalidIdentifier
		}
	}
	return nil
}

func (service *ConnectionService) ListPage(ctx context.Context, subjectID string, after *ConnectionCursor, first int) (ConnectionPage, error) {
	if err := validateIntegrationPage(subjectID, after, first, 100); err != nil {
		return ConnectionPage{}, err
	}
	store, ok := service.store.(ConnectionPageStore)
	if !ok {
		return ConnectionPage{}, ErrPaginationUnsupported
	}
	connections, err := store.ListConnectionsPage(ctx, subjectID, after, first+1)
	if err != nil {
		return ConnectionPage{}, err
	}
	if len(connections) > first+1 {
		return ConnectionPage{}, fmt.Errorf("list connections page: store returned more than requested")
	}
	page := ConnectionPage{Connections: connections, HasNextPage: len(connections) > first}
	if page.HasNextPage {
		page.Connections = connections[:first]
	}
	return page, nil
}

func (service *CustomProviderService) ListPage(ctx context.Context, subjectID string, after *CustomProviderCursor, first int) (CustomProviderPage, error) {
	if err := validateIntegrationPage(subjectID, after, first, 100); err != nil {
		return CustomProviderPage{}, err
	}
	store, ok := service.store.(CustomProviderPageStore)
	if !ok {
		return CustomProviderPage{}, ErrPaginationUnsupported
	}
	providers, err := store.ListCustomProvidersPage(ctx, subjectID, after, first+1)
	if err != nil {
		return CustomProviderPage{}, err
	}
	if len(providers) > first+1 {
		return CustomProviderPage{}, fmt.Errorf("list custom providers page: store returned more than requested")
	}
	page := CustomProviderPage{Providers: providers, HasNextPage: len(providers) > first}
	if page.HasNextPage {
		page.Providers = providers[:first]
	}
	return page, nil
}
