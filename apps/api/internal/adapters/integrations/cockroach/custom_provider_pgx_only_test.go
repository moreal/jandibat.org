package cockroach

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestCustomProviderOperationsRequirePGXPool(t *testing.T) {
	store := &Store{}
	ctx := context.Background()
	id := "018f0000-0000-7000-8000-000000000002"
	now := time.Now().UTC()
	record := integrations.CustomProviderRecord{Provider: integrations.CustomProvider{
		ID: id, SubjectID: "subject-1", EnvironmentID: "environment-1",
		Slug: "reading", Name: "Reading", Status: integrations.CustomProviderActive,
		CreatedAt: now, UpdatedAt: now,
	}}
	activity := integrations.IngestedActivity{
		ProviderID: id, ExternalID: "event-1", Date: "2026-09-24", Action: "read",
		Metric: "count", Value: 1, IngestedAt: now,
	}
	key := integrations.IngestIdempotencyRecord{
		ProviderID: id, KeyHash: []byte{1}, RequestHash: []byte{2},
		ReservationToken: "018f0000-0000-7000-8000-000000000003",
		ResponseBody:     []byte(`{}`), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{"save provider", func() error { return store.SaveCustomProvider(ctx, record) }},
		{"create provider", func() error { return store.CreateCustomProvider(ctx, record) }},
		{"update provider", func() error { return store.UpdateCustomProvider(ctx, record) }},
		{"delete provider aggregate", func() error { return store.DeleteCustomProviderAggregate(ctx, id) }},
		{"get provider", func() error { _, err := store.GetCustomProvider(ctx, id); return err }},
		{"list providers", func() error { _, err := store.ListCustomProviders(ctx, "subject-1"); return err }},
		{"page providers", func() error { _, err := store.ListCustomProvidersPage(ctx, "subject-1", nil, 1); return err }},
		{"delete provider", func() error { return store.DeleteCustomProvider(ctx, id) }},
		{"save activities", func() error {
			_, err := store.SaveIngestedActivities(ctx, []integrations.IngestedActivity{activity})
			return err
		}},
		{"list activities", func() error { _, err := store.ListIngestedActivities(ctx, id); return err }},
		{"reserve key", func() error { _, err := store.CreateIngestIdempotencyKey(ctx, key); return err }},
		{"complete key", func() error { _, err := store.CompleteIngestIdempotencyKey(ctx, key); return err }},
		{"release key", func() error {
			return store.ReleaseIngestIdempotencyKey(ctx, id, key.KeyHash, key.RequestHash, key.ReservationToken)
		}},
		{"get key", func() error { _, _, err := store.GetIngestIdempotencyKey(ctx, id, key.KeyHash); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, ErrNilDB) {
				t.Fatalf("custom provider operation without pool error = %v, want ErrNilDB", err)
			}
		})
	}
}
