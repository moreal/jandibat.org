package graphql

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

// NodeServices are application-layer ports, not persistence adapters. Their
// existing visibility and tombstone rules remain authoritative.
type NodeServices struct {
	Subjects interface {
		GetSubject(context.Context, string, string) (subjects.Subject, error)
		OwnsSubject(context.Context, string, string) (bool, error)
	}
	Connections interface {
		Get(context.Context, string) (integrations.ProviderConnection, error)
	}
	CustomProviders interface {
		Get(context.Context, string) (integrations.CustomProvider, error)
	}
	SyncJobs interface {
		GetJob(context.Context, string) (integrations.SyncJob, error)
	}
	Sessions interface {
		GetSessionByID(context.Context, string, string) (auth.Session, error)
	}
}

type nodeServicesContextKey struct{}

// ContextWithNodeServices injects application ports at the trusted GraphQL
// transport boundary. It is not populated from client-controlled values.
func ContextWithNodeServices(ctx context.Context, services NodeServices) context.Context {
	return context.WithValue(ctx, nodeServicesContextKey{}, services)
}

type viewerContextKey struct{}
type viewerIdentity struct {
	userID string
	failed bool
}

// ContextWithVerifiedViewer accepts only the user ID produced by a successful
// authentication boundary. Never put a bearer token or session key in context.
func ContextWithVerifiedViewer(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ContextWithFailedAuthentication(ctx)
	}
	return context.WithValue(ctx, viewerContextKey{}, viewerIdentity{userID: userID})
}

// ContextWithFailedAuthentication prevents a rejected credential from being
// silently treated as an anonymous public lookup.
func ContextWithFailedAuthentication(ctx context.Context) context.Context {
	return context.WithValue(ctx, viewerContextKey{}, viewerIdentity{failed: true})
}

var (
	errInvalidNodeID      = errors.New("invalid global ID")
	errNodeLookup         = errors.New("node lookup failed")
	errNodeAuthentication = errors.New("authentication failed")
)

func (r *queryResolver) resolveNode(ctx context.Context, id string) (model.Node, error) {
	services, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return nil, errNodeAuthentication
	}
	kind, raw, err := relayid.Decode(id)
	if err != nil {
		return nil, errInvalidNodeID
	}
	switch kind {
	case relayid.Subject:
		if services.Subjects == nil {
			return nil, errNodeLookup
		}
		subject, err := services.Subjects.GetSubject(ctx, identity.userID, raw)
		if err != nil {
			return nil, redactNodeError(err)
		}
		if subject.ID != raw {
			return nil, errNodeLookup
		}
		return &model.Subject{ID: relayid.Encode(relayid.Subject, subject.ID)}, nil
	case relayid.ProviderConnection:
		if identity.userID == "" {
			return nil, nil
		}
		if services.Connections == nil || services.Subjects == nil {
			return nil, errNodeLookup
		}
		connection, err := services.Connections.Get(ctx, raw)
		if err != nil {
			return nil, redactNodeError(err)
		}
		if connection.ID != raw || connection.Status == integrations.ConnectionRevoked {
			return nil, nil
		}
		visible, err := ownsSubject(ctx, services.Subjects, identity.userID, connection.SubjectID)
		if err != nil || !visible {
			return nil, err
		}
		return &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, connection.ID)}, nil
	case relayid.CustomProvider:
		if identity.userID == "" {
			return nil, nil
		}
		if services.CustomProviders == nil || services.Subjects == nil {
			return nil, errNodeLookup
		}
		provider, err := services.CustomProviders.Get(ctx, raw)
		if err != nil {
			return nil, redactNodeError(err)
		}
		if provider.ID != raw {
			return nil, errNodeLookup
		}
		visible, err := ownsSubject(ctx, services.Subjects, identity.userID, provider.SubjectID)
		if err != nil || !visible {
			return nil, err
		}
		return &model.CustomProvider{ID: relayid.Encode(relayid.CustomProvider, provider.ID)}, nil
	case relayid.SyncJob:
		if identity.userID == "" {
			return nil, nil
		}
		if services.SyncJobs == nil || services.Connections == nil || services.Subjects == nil {
			return nil, errNodeLookup
		}
		job, err := services.SyncJobs.GetJob(ctx, raw)
		if err != nil {
			return nil, redactNodeError(err)
		}
		if job.ID != raw {
			return nil, errNodeLookup
		}
		connection, err := services.Connections.Get(ctx, job.ConnectionID)
		if err != nil {
			return nil, redactNodeError(err)
		}
		if connection.ID != job.ConnectionID || connection.Status == integrations.ConnectionRevoked {
			return nil, nil
		}
		visible, err := ownsSubject(ctx, services.Subjects, identity.userID, connection.SubjectID)
		if err != nil || !visible {
			return nil, err
		}
		return &model.SyncJob{ID: relayid.Encode(relayid.SyncJob, job.ID)}, nil
	case relayid.Session:
		if identity.userID == "" {
			return nil, nil
		}
		if services.Sessions == nil {
			return nil, errNodeLookup
		}
		requestedID, err := uuid.Parse(raw)
		if err != nil {
			return nil, errInvalidNodeID
		}
		session, err := services.Sessions.GetSessionByID(ctx, identity.userID, raw)
		if err != nil {
			return nil, redactNodeError(err)
		}
		actualID, err := uuid.Parse(session.ID)
		if err != nil {
			return nil, errNodeLookup
		}
		if actualID != requestedID || session.UserID != identity.userID {
			return nil, nil
		}
		return &model.Session{ID: relayid.Encode(relayid.Session, actualID.String())}, nil
	default:
		return nil, errInvalidNodeID
	}
}

func ownsSubject(ctx context.Context, service interface {
	OwnsSubject(context.Context, string, string) (bool, error)
}, userID, subjectID string) (bool, error) {
	owned, err := service.OwnsSubject(ctx, userID, subjectID)
	if err != nil {
		return false, redactNodeError(err)
	}
	return owned, nil
}

func redactNodeError(err error) error {
	if errors.Is(err, subjects.ErrNotFound) || errors.Is(err, subjects.ErrForbidden) ||
		errors.Is(err, subjects.ErrUnauthenticated) || errors.Is(err, integrations.ErrNotFound) ||
		errors.Is(err, auth.ErrNotFound) {
		return nil
	}
	return errNodeLookup
}
