package cockroach

import (
	"reflect"
	"testing"
	"time"

	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach/generated"
)

func TestGeneratedCustomProviderMappingKeepsRestrictedDigestAndCopiesIt(t *testing.T) {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	digest := []byte{9, 8, 7}
	got, err := customProviderFromGenerated(generated.GetCustomProviderByIdRow{
		Id: "018f0000-0000-7000-8000-000000000002", SubjectId: "subject-1",
		EnvironmentId: "reading-env", Slug: "reading", Name: "Reading",
		Description: "Books read", Status: "active",
		Configuration:   []byte(`{"allowed_actions":["read"],"allowed_metrics":["pages"]}`),
		IngestTokenHash: digest, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Provider.AllowedActions, []string{"read"}) ||
		!reflect.DeepEqual(got.Provider.AllowedMetrics, []string{"pages"}) ||
		!reflect.DeepEqual(got.EncryptedIngestSecret, []byte{9, 8, 7}) {
		t.Fatalf("mapped provider = %#v", got)
	}
	digest[0] = 1
	if got.EncryptedIngestSecret[0] != 9 {
		t.Fatal("mapped provider retained a mutable digest alias")
	}
}

func TestGeneratedCustomActivityMappingUsesDatabaseCanonicalValues(t *testing.T) {
	ingestedAt := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	observedAt := ingestedAt.Add(-time.Minute)
	got, err := ingestedActivityFromGenerated(generated.InsertCustomActivityRow{
		ProviderId: "018f0000-0000-7000-8000-000000000002", SubjectId: "canonical-subject",
		ExternalId: "event-1", ActivityDate: "2026-09-24", Action: "read",
		MetricName: "pages", MetricValue: 42,
		Metadata:   []byte(`{"book":"The Left Hand of Darkness"}`),
		ObservedAt: &observedAt, IngestedAt: ingestedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SubjectID != "canonical-subject" || got.Date != "2026-09-24" ||
		got.Value != 42 || got.Metadata["book"] != "The Left Hand of Darkness" ||
		got.ObservedAt == nil || !got.ObservedAt.Equal(observedAt) {
		t.Fatalf("mapped canonical activity = %#v", got)
	}
}
