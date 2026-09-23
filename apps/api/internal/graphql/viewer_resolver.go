package graphql

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func (r *queryResolver) resolveViewer(ctx context.Context) (*model.Viewer, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return nil, errNodeAuthentication
	}
	if identity.userID == "" {
		return nil, nil
	}
	services, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	if services.ViewerUsers == nil {
		return nil, errNodeLookup
	}
	user, err := services.ViewerUsers.GetCurrentUser(ctx, identity.userID)
	if errors.Is(err, subjects.ErrUnauthenticated) || errors.Is(err, subjects.ErrForbidden) {
		return nil, errNodeAuthentication
	}
	if err != nil || user.ID != identity.userID {
		return nil, errNodeLookup
	}
	return &model.Viewer{User: &model.UserProfile{
		ID: user.ID, PrimaryEmail: user.PrimaryEmail, Status: string(user.Status),
		EmailVerifiedAt: optionalDateTime(user.EmailVerifiedAt),
		CreatedAt:       scalar.DateTime(user.CreatedAt), UpdatedAt: scalar.DateTime(user.UpdatedAt),
	}}, nil
}

func optionalDateTime(value *time.Time) *scalar.DateTime {
	if value == nil {
		return nil
	}
	converted := scalar.DateTime(*value)
	return &converted
}

func (r *viewerResolver) resolveSessions(ctx context.Context, viewer *model.Viewer, first *int, after *scalar.Cursor) (*model.SessionConnection, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" || viewer == nil || viewer.User == nil || viewer.User.ID != identity.userID {
		return nil, errNodeAuthentication
	}
	limit := 25
	if first != nil {
		limit = *first
	}
	if limit < 1 || limit > 100 {
		return nil, snapshotUserError("BAD_USER_INPUT", "first must be between 1 and 100")
	}
	var pageAfter *auth.SessionCursor
	if after != nil {
		position, err := cursor.DecodeAs(cursor.Session, string(*after))
		if err != nil {
			return nil, snapshotUserError("BAD_USER_INPUT", "invalid session cursor")
		}
		pageAfter = &auth.SessionCursor{CreatedAt: position.Timestamp, ID: position.ID}
	}
	services, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	if services.SessionPages == nil {
		return nil, errNodeLookup
	}
	items, err := services.SessionPages.ListSessionsPage(ctx, identity.userID, pageAfter, limit)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			return nil, snapshotUserError("BAD_USER_INPUT", "invalid session page")
		}
		if errors.Is(err, auth.ErrUserDisabled) || errors.Is(err, auth.ErrNotFound) {
			return nil, errNodeAuthentication
		}
		return nil, errNodeLookup
	}
	if len(items) > limit+1 {
		return nil, errNodeLookup
	}
	hasNext := len(items) > limit
	if hasNext {
		items = items[:limit]
	}
	edges := make([]*model.SessionEdge, 0, len(items))
	for _, session := range items {
		if session.UserID != identity.userID {
			return nil, errNodeLookup
		}
		id, err := uuid.Parse(session.ID)
		if err != nil || id.String() != session.ID {
			return nil, errNodeLookup
		}
		encoded, err := cursor.Encode(cursor.Session, cursor.Position{Timestamp: session.CreatedAt, ID: id.String()})
		if err != nil {
			return nil, errNodeLookup
		}
		edges = append(edges, &model.SessionEdge{Cursor: scalar.Cursor(encoded), Node: projectSession(session)})
	}
	pageInfo := &model.PageInfo{HasNextPage: hasNext, HasPreviousPage: pageAfter != nil}
	if len(edges) > 0 {
		start, end := edges[0].Cursor, edges[len(edges)-1].Cursor
		pageInfo.StartCursor, pageInfo.EndCursor = &start, &end
	}
	return &model.SessionConnection{Edges: edges, PageInfo: pageInfo}, nil
}

func projectSession(session auth.Session) *model.Session {
	projected := &model.Session{
		ID:        relayid.Encode(relayid.Session, session.ID),
		CreatedAt: scalar.DateTime(session.CreatedAt), ExpiresAt: scalar.DateTime(session.ExpiresAt),
		RevokedAt: optionalDateTime(session.RevokedAt), LastSeenAt: optionalDateTime(session.LastSeenAt),
	}
	if session.IPAddress != "" {
		projected.IPAddress = &session.IPAddress
	}
	if session.UserAgent != "" {
		projected.UserAgent = &session.UserAgent
	}
	return projected
}
