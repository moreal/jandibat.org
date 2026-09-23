package graphql

// THIS CODE WILL BE UPDATED WITH SCHEMA CHANGES. PREVIOUS IMPLEMENTATION FOR SCHEMA CHANGES WILL BE KEPT IN THE COMMENT SECTION. IMPLEMENTATION FOR UNCHANGED SCHEMA WILL BE KEPT.

import (
	"context"

	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
)

type Resolver struct{}

// Contract is the resolver for the _contract field.
func (r *mutationResolver) Contract(ctx context.Context) (bool, error) {
	return true, nil
}

// RequestMagicLink is the resolver for the requestMagicLink field.
func (r *mutationResolver) RequestMagicLink(ctx context.Context, input model.RequestMagicLinkInput) (*model.RequestMagicLinkPayload, error) {
	return resolveRequestMagicLink(ctx, input.Email, input.RedirectURI)
}

// BeginPasskeyRegistration is the resolver for the beginPasskeyRegistration field.
func (r *mutationResolver) BeginPasskeyRegistration(ctx context.Context) (*model.BeginPasskeyRegistrationPayload, error) {
	return r.resolveBeginPasskeyRegistration(ctx)
}

// FinishPasskeyRegistration is the resolver for the finishPasskeyRegistration field.
func (r *mutationResolver) FinishPasskeyRegistration(ctx context.Context, input model.FinishPasskeyRegistrationInput) (*model.FinishPasskeyRegistrationPayload, error) {
	return r.resolveFinishPasskeyRegistration(ctx, input)
}

// BeginPasskeySignIn is the resolver for the beginPasskeySignIn field.
func (r *mutationResolver) BeginPasskeySignIn(ctx context.Context) (*model.BeginPasskeySignInPayload, error) {
	return r.resolveBeginPasskeySignIn(ctx)
}

// FinishPasskeySignIn is the resolver for the finishPasskeySignIn field.
func (r *mutationResolver) FinishPasskeySignIn(ctx context.Context, input model.FinishPasskeySignInInput) (*model.FinishPasskeySignInPayload, error) {
	return r.resolveFinishPasskeySignIn(ctx, input)
}

// SignOut is the resolver for the signOut field.
func (r *mutationResolver) SignOut(ctx context.Context) (*model.SignOutPayload, error) {
	return resolveSignOut(ctx)
}

// RevokeSession is the resolver for the revokeSession field.
func (r *mutationResolver) RevokeSession(ctx context.Context, input model.RevokeSessionInput) (*model.RevokeSessionPayload, error) {
	return resolveRevokeSession(ctx, input.ID)
}

// RevokeOtherSessions is the resolver for the revokeOtherSessions field.
func (r *mutationResolver) RevokeOtherSessions(ctx context.Context) (*model.RevokeOtherSessionsPayload, error) {
	return resolveRevokeOtherSessions(ctx)
}

// SyncJobs is the resolver for the syncJobs field.
func (r *providerConnectionResolver) SyncJobs(ctx context.Context, obj *model.ProviderConnection, first *int, after *scalar.Cursor) (*model.SyncJobConnection, error) {
	return resolveSyncJobs(ctx, obj, first, after)
}

// Contract is the resolver for the _contract field.
func (r *queryResolver) Contract(ctx context.Context) (bool, error) {
	return true, nil
}

// Node is the resolver for the node field.
func (r *queryResolver) Node(ctx context.Context, id string) (model.Node, error) {
	return r.resolveNode(ctx, id)
}

// Subject is the resolver for the subject field.
func (r *queryResolver) Subject(ctx context.Context, handleOrID string) (*model.Subject, error) {
	return r.resolveSubject(ctx, handleOrID)
}

// Viewer is the resolver for the viewer field.
func (r *queryResolver) Viewer(ctx context.Context) (*model.Viewer, error) {
	return r.resolveViewer(ctx)
}

// ActivitySnapshot is the resolver for the activitySnapshot field.
func (r *subjectResolver) ActivitySnapshot(ctx context.Context, obj *model.Subject, rangeArg model.DateRangeInput, timezone scalar.TimeZone, environmentIDs []string) (*model.ActivitySnapshot, error) {
	return r.resolveActivitySnapshot(ctx, obj, rangeArg, timezone, environmentIDs)
}

// Sessions is the resolver for the sessions field.
func (r *viewerResolver) Sessions(ctx context.Context, obj *model.Viewer, first *int, after *scalar.Cursor) (*model.SessionConnection, error) {
	return r.resolveSessions(ctx, obj, first, after)
}

// Mutation returns generated.MutationResolver implementation.
func (r *Resolver) Mutation() generated.MutationResolver { return &mutationResolver{r} }

// ProviderConnection returns generated.ProviderConnectionResolver implementation.
func (r *Resolver) ProviderConnection() generated.ProviderConnectionResolver {
	return &providerConnectionResolver{r}
}

// Query returns generated.QueryResolver implementation.
func (r *Resolver) Query() generated.QueryResolver { return &queryResolver{r} }

// Subject returns generated.SubjectResolver implementation.
func (r *Resolver) Subject() generated.SubjectResolver { return &subjectResolver{r} }

// Viewer returns generated.ViewerResolver implementation.
func (r *Resolver) Viewer() generated.ViewerResolver { return &viewerResolver{r} }

type (
	mutationResolver           struct{ *Resolver }
	providerConnectionResolver struct{ *Resolver }
	queryResolver              struct{ *Resolver }
	subjectResolver            struct{ *Resolver }
	viewerResolver             struct{ *Resolver }
)

// !!! WARNING !!!
// The code below was going to be deleted when updating resolvers. It has been copied here so you have
// one last chance to move it out of harms way if you want. There are two reasons this happens:
//  - When renaming or deleting a resolver the old code will be put in here. You can safely delete
//    it when you're done.
//  - You have helper methods in this file. Move them out to keep these resolver files clean.
/*
	type Resolver struct{}
*/
