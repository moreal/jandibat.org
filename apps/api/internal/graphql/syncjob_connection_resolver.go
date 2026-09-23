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

// SyncJobPageService is an application port. The GraphQL boundary installs it
// from trusted runtime wiring, never from request data.
type SyncJobPageService interface {
	ListJobsPage(context.Context, string, *integrations.SyncJobCursor, int) (integrations.SyncJobPage, error)
}

type syncJobPageServiceContextKey struct{}

func ContextWithSyncJobPageService(ctx context.Context, service SyncJobPageService) context.Context {
	return context.WithValue(ctx, syncJobPageServiceContextKey{}, service)
}

var errInvalidSyncJobPage = errors.New("invalid SyncJob connection request")

// resolveSyncJobs reauthorizes the parent on every field read, including when
// the parent object came from a prior resolver or a future loader/cache.
func resolveSyncJobs(ctx context.Context, parent *model.ProviderConnection, first *int, after *scalar.Cursor) (*model.SyncJobConnection, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" {
		return nil, errNodeAuthentication
	}
	if parent == nil {
		return nil, errInvalidNodeID
	}
	connectionID, err := relayid.DecodeAs(relayid.ProviderConnection, parent.ID)
	if err != nil {
		return nil, errInvalidNodeID
	}
	limit := 25
	if first != nil {
		limit = *first
	}
	if limit < 1 || limit > 100 {
		return nil, errInvalidSyncJobPage
	}
	var position *integrations.SyncJobCursor
	if after != nil {
		decoded, err := cursor.DecodeAs(cursor.SyncJob, string(*after))
		if err != nil {
			return nil, errInvalidSyncJobPage
		}
		position = &integrations.SyncJobCursor{CreatedAt: decoded.Timestamp, ID: decoded.ID}
	}
	services, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	pager, _ := ctx.Value(syncJobPageServiceContextKey{}).(SyncJobPageService)
	if services.Connections == nil || services.Subjects == nil || pager == nil {
		return nil, errNodeLookup
	}
	connection, err := services.Connections.Get(ctx, connectionID)
	if err != nil {
		if errors.Is(err, integrations.ErrNotFound) {
			return nil, nil
		}
		return nil, errNodeLookup
	}
	if connection.ID != connectionID || connection.Status == integrations.ConnectionRevoked || connection.SubjectID == "" {
		return nil, nil
	}
	owned, err := ownsSubject(ctx, services.Subjects, identity.userID, connection.SubjectID)
	if err != nil {
		return nil, errNodeLookup
	}
	if !owned {
		return nil, nil
	}
	page, err := pager.ListJobsPage(ctx, connectionID, position, limit)
	if err != nil || len(page.Jobs) > limit {
		return nil, errNodeLookup
	}
	result := &model.SyncJobConnection{
		Edges: make([]*model.SyncJobEdge, 0, len(page.Jobs)),
		PageInfo: &model.PageInfo{
			HasNextPage: page.HasNextPage, HasPreviousPage: after != nil,
		},
	}
	var previous cursor.Position
	for _, job := range page.Jobs {
		if job.ConnectionID != connectionID || job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.Attempt < 0 {
			return nil, errNodeLookup
		}
		current := cursor.Position{Timestamp: job.CreatedAt, ID: job.ID}
		if position != nil && compareSyncJobPosition(current, cursor.Position{Timestamp: position.CreatedAt, ID: position.ID}) <= 0 ||
			!previous.Timestamp.IsZero() && compareSyncJobPosition(current, previous) <= 0 {
			return nil, errNodeLookup
		}
		encoded, err := cursor.Encode(cursor.SyncJob, current)
		if err != nil {
			return nil, errNodeLookup
		}
		globalID := relayid.Encode(relayid.SyncJob, job.ID)
		if globalID == "" {
			return nil, errNodeLookup
		}
		result.Edges = append(result.Edges, &model.SyncJobEdge{
			Cursor: scalar.Cursor(encoded),
			Node: &model.SyncJob{
				ID: globalID, Status: string(job.Status), Attempt: job.Attempt,
				CreatedAt: scalar.DateTime(job.CreatedAt), UpdatedAt: scalar.DateTime(job.UpdatedAt),
			},
		})
		previous = current
	}
	if len(result.Edges) != 0 {
		start := result.Edges[0].Cursor
		end := result.Edges[len(result.Edges)-1].Cursor
		result.PageInfo.StartCursor = &start
		result.PageInfo.EndCursor = &end
	}
	return result, nil
}

func compareSyncJobPosition(a, b cursor.Position) int {
	if a.Timestamp.Before(b.Timestamp) {
		return -1
	}
	if a.Timestamp.After(b.Timestamp) {
		return 1
	}
	if a.ID < b.ID {
		return -1
	}
	if a.ID > b.ID {
		return 1
	}
	return 0
}
