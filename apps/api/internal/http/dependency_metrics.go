package apihttp

import (
	"context"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
)

type observedCustomProviders struct {
	next    integrationsCustomProviderService
	metrics *observability.Registry
}

// Keep the transport decorator independent of handlers' private interface
// declaration while asserting the exact same contract at assignment time.
type integrationsCustomProviderService interface {
	Create(context.Context, integrations.CreateCustomProviderInput) (integrations.CustomProvider, error)
	Update(context.Context, integrations.UpdateCustomProviderInput) (integrations.CustomProvider, error)
	RotateIngestSecret(context.Context, string, string) error
	Get(context.Context, string) (integrations.CustomProvider, error)
	List(context.Context, string) ([]integrations.CustomProvider, error)
	Delete(context.Context, string) error
	Ingest(context.Context, integrations.IngestCustomActivitiesInput) (integrations.IngestCustomActivitiesResult, error)
}

func (service observedCustomProviders) Create(ctx context.Context, input integrations.CreateCustomProviderInput) (integrations.CustomProvider, error) {
	return service.next.Create(ctx, input)
}

func (service observedCustomProviders) Update(ctx context.Context, input integrations.UpdateCustomProviderInput) (integrations.CustomProvider, error) {
	return service.next.Update(ctx, input)
}

func (service observedCustomProviders) RotateIngestSecret(ctx context.Context, providerID, ownerID string) error {
	return service.next.RotateIngestSecret(ctx, providerID, ownerID)
}

func (service observedCustomProviders) Get(ctx context.Context, providerID string) (integrations.CustomProvider, error) {
	return service.next.Get(ctx, providerID)
}

func (service observedCustomProviders) List(ctx context.Context, subjectID string) ([]integrations.CustomProvider, error) {
	return service.next.List(ctx, subjectID)
}

func (service observedCustomProviders) Delete(ctx context.Context, providerID string) error {
	return service.next.Delete(ctx, providerID)
}

func (service observedCustomProviders) Ingest(ctx context.Context, input integrations.IngestCustomActivitiesInput) (integrations.IngestCustomActivitiesResult, error) {
	result, err := service.next.Ingest(ctx, input)
	service.metrics.ObserveCustomIngest("accepted", uint64(max(0, result.Accepted)))
	service.metrics.ObserveCustomIngest("duplicate", uint64(max(0, result.Duplicate)))
	service.metrics.ObserveCustomIngest("rejected", uint64(max(0, result.Rejected)))
	return result, err
}
