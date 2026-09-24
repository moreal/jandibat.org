package graphql

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

// CustomProviderMutationServices is installed only by a trusted transport.
// KeyReader is injectable for a failing entropy source in tests; nil uses the
// operating system CSPRNG. Neither the key nor the reader enters GraphQL data.
type CustomProviderMutationServices struct {
	Providers interface {
		Create(context.Context, integrations.CreateCustomProviderInput) (integrations.CustomProvider, error)
		Get(context.Context, string) (integrations.CustomProvider, error)
		Update(context.Context, integrations.UpdateCustomProviderInput) (integrations.CustomProvider, error)
		RotateIngestSecret(context.Context, string, string) error
		Delete(context.Context, string) error
	}
	KeyReader io.Reader
}

type customProviderMutationContextKey struct{}

func ContextWithCustomProviderMutationServices(ctx context.Context, services CustomProviderMutationServices) context.Context {
	return context.WithValue(ctx, customProviderMutationContextKey{}, services)
}

func trustedCustomProviderMutation(ctx context.Context) (string, CustomProviderMutationServices, NodeServices, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" {
		return "", CustomProviderMutationServices{}, NodeServices{}, errNodeAuthentication
	}
	services, _ := ctx.Value(customProviderMutationContextKey{}).(CustomProviderMutationServices)
	nodes, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	if services.Providers == nil || nodes.Subjects == nil {
		return "", CustomProviderMutationServices{}, NodeServices{}, errNodeLookup
	}
	return identity.userID, services, nodes, nil
}

func customProviderMutationError(err error, field string) ([]*model.MutationError, error) {
	switch {
	case errors.Is(err, integrations.ErrNotFound), errors.Is(err, integrations.ErrUnauthorized):
		return authMutationError("NOT_FOUND", "Custom provider not found.", field), nil
	case errors.Is(err, integrations.ErrConflict), errors.Is(err, integrations.ErrDuplicateProviderSlug):
		return authMutationError("CONFLICT", "Custom provider conflicts with an existing record.", field), nil
	case errors.Is(err, integrations.ErrInvalidProvider), errors.Is(err, integrations.ErrInvalidIdentifier), errors.Is(err, integrations.ErrEmptySubjectID), errors.Is(err, integrations.ErrEmptyProviderSlug), errors.Is(err, integrations.ErrInvalidProviderSlug), errors.Is(err, integrations.ErrEmptyProviderName), errors.Is(err, integrations.ErrProviderNameTooLong), errors.Is(err, integrations.ErrProviderDescriptionTooLong), errors.Is(err, integrations.ErrTooManyAllowedActions), errors.Is(err, integrations.ErrInvalidAllowedAction), errors.Is(err, integrations.ErrDuplicateAllowedAction), errors.Is(err, integrations.ErrTooManyAllowedMetrics), errors.Is(err, integrations.ErrInvalidAllowedMetric), errors.Is(err, integrations.ErrDuplicateAllowedMetric):
		return authMutationError("BAD_USER_INPUT", "Invalid custom provider input.", field), nil
	default:
		return nil, errNodeLookup
	}
}

func decodeCustomProviderMutationID(global string) (string, []*model.MutationError) {
	raw, err := relayid.DecodeAs(relayid.CustomProvider, global)
	if err != nil {
		return "", authMutationError("BAD_USER_INPUT", "Invalid custom provider ID.", "id")
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return "", authMutationError("BAD_USER_INPUT", "Invalid custom provider ID.", "id")
	}
	return raw, nil
}

func ownedCustomProviderForMutation(ctx context.Context, actor, raw string, services CustomProviderMutationServices, nodes NodeServices) (integrations.CustomProvider, []*model.MutationError, error) {
	provider, err := services.Providers.Get(ctx, raw)
	if err != nil {
		validation, publicErr := customProviderMutationError(err, "id")
		return integrations.CustomProvider{}, validation, publicErr
	}
	if provider.ID != raw || provider.SubjectID == "" {
		return integrations.CustomProvider{}, authMutationError("NOT_FOUND", "Custom provider not found.", "id"), nil
	}
	owned, err := ownsSubject(ctx, nodes.Subjects, actor, provider.SubjectID)
	if err != nil {
		return integrations.CustomProvider{}, nil, errNodeLookup
	}
	if !owned {
		return integrations.CustomProvider{}, authMutationError("NOT_FOUND", "Custom provider not found.", "id"), nil
	}
	return provider, nil, nil
}

func newCustomProviderKey(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return "", errNodeLookup
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

func resolveCreateCustomProvider(ctx context.Context, input model.CreateCustomProviderInput) (*model.CreateCustomProviderPayload, error) {
	actor, services, nodes, err := trustedCustomProviderMutation(ctx)
	if err != nil {
		return nil, err
	}
	subjectID, err := relayid.DecodeAs(relayid.Subject, input.SubjectID)
	if err != nil {
		return &model.CreateCustomProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid subject ID.", "subjectID")}, nil
	}
	owned, err := ownsSubject(ctx, nodes.Subjects, actor, subjectID)
	if err != nil {
		return nil, errNodeLookup
	}
	if !owned {
		return &model.CreateCustomProviderPayload{Errors: authMutationError("NOT_FOUND", "Subject not found.", "subjectID")}, nil
	}
	key, err := newCustomProviderKey(services.KeyReader)
	if err != nil {
		return nil, err
	}
	description := ""
	if input.Description != nil {
		description = *input.Description
	}
	provider, err := services.Providers.Create(ctx, integrations.CreateCustomProviderInput{SubjectID: subjectID, Slug: input.Slug, Name: input.Name, Description: description, AllowedActions: input.AllowedActions, IngestSecret: key})
	if err != nil {
		validation, publicErr := customProviderMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.CreateCustomProviderPayload{Errors: validation}, nil
	}
	if provider.SubjectID != subjectID || !validCustomProviderMutationResult(provider) {
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "custom_provider", provider.ID)
	return &model.CreateCustomProviderPayload{Errors: []*model.MutationError{}, Provider: projectCustomProvider(provider), IngestionKey: &key}, nil
}

func validCustomProviderMutationResult(provider integrations.CustomProvider) bool {
	parsed, err := uuid.Parse(provider.ID)
	return err == nil && parsed.String() == provider.ID && !provider.CreatedAt.IsZero() && !provider.UpdatedAt.IsZero()
}

func resolveUpdateCustomProvider(ctx context.Context, input model.UpdateCustomProviderInput) (*model.UpdateCustomProviderPayload, error) {
	actor, services, nodes, err := trustedCustomProviderMutation(ctx)
	if err != nil {
		return nil, err
	}
	raw, invalid := decodeCustomProviderMutationID(input.ID)
	if invalid != nil {
		return &model.UpdateCustomProviderPayload{Errors: invalid}, nil
	}
	if input.Name == nil && input.Description == nil && (input.ClearDescription == nil || !*input.ClearDescription) && input.Status == nil && input.AllowedActions == nil {
		return &model.UpdateCustomProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "At least one custom provider field is required.", "input")}, nil
	}
	previous, denied, err := ownedCustomProviderForMutation(ctx, actor, raw, services, nodes)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return &model.UpdateCustomProviderPayload{Errors: denied}, nil
	}
	if input.ClearDescription != nil && *input.ClearDescription && input.Description != nil {
		return &model.UpdateCustomProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "description and clearDescription are mutually exclusive.", "clearDescription")}, nil
	}
	update := integrations.UpdateCustomProviderInput{ID: raw, Name: previous.Name, Description: previous.Description, Status: previous.Status, AllowedActions: previous.AllowedActions, AllowedMetrics: previous.AllowedMetrics}
	if input.Name != nil {
		update.Name = *input.Name
	}
	if input.Description != nil {
		update.Description = *input.Description
	}
	if input.ClearDescription != nil && *input.ClearDescription {
		update.Description = ""
	}
	if input.Status != nil {
		switch *input.Status {
		case model.CustomProviderStatusActive:
			update.Status = integrations.CustomProviderActive
		case model.CustomProviderStatusDisabled:
			update.Status = integrations.CustomProviderDisabled
		default:
			return &model.UpdateCustomProviderPayload{Errors: authMutationError("BAD_USER_INPUT", "Invalid custom provider status.", "status")}, nil
		}
	}
	if input.AllowedActions != nil {
		update.AllowedActions = input.AllowedActions
	}
	provider, err := services.Providers.Update(ctx, update)
	if err != nil {
		validation, publicErr := customProviderMutationError(err, "input")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.UpdateCustomProviderPayload{Errors: validation}, nil
	}
	if provider.ID != raw || provider.SubjectID != previous.SubjectID || !validCustomProviderMutationResult(provider) {
		return nil, errNodeLookup
	}
	PublishMutationAuditTarget(ctx, "custom_provider", provider.ID)
	return &model.UpdateCustomProviderPayload{Errors: []*model.MutationError{}, Provider: projectCustomProvider(provider)}, nil
}

func resolveRotateCustomProviderKey(ctx context.Context, input model.RotateCustomProviderKeyInput) (*model.RotateCustomProviderKeyPayload, error) {
	actor, services, nodes, err := trustedCustomProviderMutation(ctx)
	if err != nil {
		return nil, err
	}
	raw, invalid := decodeCustomProviderMutationID(input.ID)
	if invalid != nil {
		return &model.RotateCustomProviderKeyPayload{Errors: invalid}, nil
	}
	provider, denied, err := ownedCustomProviderForMutation(ctx, actor, raw, services, nodes)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return &model.RotateCustomProviderKeyPayload{Errors: denied}, nil
	}
	key, err := newCustomProviderKey(services.KeyReader)
	if err != nil {
		return nil, err
	}
	if err := services.Providers.RotateIngestSecret(ctx, raw, key); err != nil {
		validation, publicErr := customProviderMutationError(err, "id")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.RotateCustomProviderKeyPayload{Errors: validation}, nil
	}
	// A post-rotation read could fail after persistence and strand the caller
	// without the only copy of the new key. createdAt is intentionally nullable
	// until the domain service returns the commit timestamp atomically.
	PublishMutationAuditTarget(ctx, "custom_provider", provider.ID)
	return &model.RotateCustomProviderKeyPayload{Errors: []*model.MutationError{}, IngestionKey: &key}, nil
}

func resolveDeleteCustomProvider(ctx context.Context, input model.DeleteCustomProviderInput) (*model.DeleteCustomProviderPayload, error) {
	actor, services, nodes, err := trustedCustomProviderMutation(ctx)
	if err != nil {
		return nil, err
	}
	raw, invalid := decodeCustomProviderMutationID(input.ID)
	if invalid != nil {
		return &model.DeleteCustomProviderPayload{Errors: invalid}, nil
	}
	provider, denied, err := ownedCustomProviderForMutation(ctx, actor, raw, services, nodes)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return &model.DeleteCustomProviderPayload{Errors: denied}, nil
	}
	if err := services.Providers.Delete(ctx, raw); err != nil {
		validation, publicErr := customProviderMutationError(err, "id")
		if publicErr != nil {
			return nil, publicErr
		}
		return &model.DeleteCustomProviderPayload{Errors: validation}, nil
	}
	PublishMutationAuditTarget(ctx, "custom_provider", provider.ID)
	return &model.DeleteCustomProviderPayload{Errors: []*model.MutationError{}, DeletedProviderID: &input.ID}, nil
}
