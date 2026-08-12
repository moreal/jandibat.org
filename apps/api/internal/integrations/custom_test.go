package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	memorystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/memory"
	activity "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

var testIngestSecret = strings.Repeat("fixture-", 4)

func TestCustomProviderEnvironmentIsNamespacedByProviderIdentity(t *testing.T) {
	store := NewMemoryStore()
	activityStore := memorystore.New()
	service, err := NewCustomProviderService(store, mustTestCipher(), activityStore, &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(context.Background(), CreateCustomProviderInput{SubjectID: "subject-a", Slug: "shared", Name: "First", IngestSecret: testIngestSecret})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), CreateCustomProviderInput{SubjectID: "subject-b", Slug: "shared", Name: "Second", IngestSecret: testIngestSecret})
	if err != nil {
		t.Fatal(err)
	}
	if first.EnvironmentID == second.EnvironmentID || first.EnvironmentID != "custom-provider:"+first.ID || second.EnvironmentID != "custom-provider:"+second.ID {
		t.Fatalf("environment IDs collide: first=%#v second=%#v", first, second)
	}
	environments, err := activityStore.LoadEnvironments(context.Background(), activity.LoadEnvironmentsInput{})
	if err != nil || len(environments) != 2 || environments[0].OwnerSubject == nil || environments[1].OwnerSubject == nil || *environments[0].OwnerSubject == *environments[1].OwnerSubject {
		t.Fatalf("tenant environments = %#v, %v", environments, err)
	}
}

func TestCustomProviderIngestProjectsPartialBatchIntoTimeline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	integrationStore := NewMemoryStore()
	activityStore := memorystore.New()
	observedAt := time.Date(2026, 8, 12, 8, 30, 0, 0, time.FixedZone("KST", 9*60*60))
	service, err := NewCustomProviderService(
		integrationStore,
		mustTestCipher(),
		activityStore,
		&fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		&sequentialIDs{},
	)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := service.Create(ctx, CreateCustomProviderInput{
		SubjectID: "subject-1", EnvironmentID: "custom:reading_log", Slug: "reading_log", Name: "Reading",
		AllowedActions: []string{"read"}, AllowedMetrics: []string{"pages"}, IngestSecret: testIngestSecret,
	})
	if err != nil {
		t.Fatal(err)
	}

	record, err := integrationStore.GetCustomProvider(ctx, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record.EncryptedIngestSecret, []byte(testIngestSecret)) {
		t.Fatal("store received plaintext ingest secret")
	}
	wantDigest := sha256.Sum256([]byte(testIngestSecret))
	if !bytes.Equal(record.EncryptedIngestSecret, wantDigest[:]) {
		t.Fatalf("persisted key payload is not its digest: %x", record.EncryptedIngestSecret)
	}

	catalog, err := NewCatalogService(integrationStore).List(ctx, "subject-1")
	if err != nil || len(catalog) != 4 || catalog[0].CustomProviderID != provider.ID {
		t.Fatalf("catalog = %#v, error = %v", catalog, err)
	}

	input := IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret,
		Activities: []CustomActivity{
			{ExternalID: "book-1", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 3, Metadata: map[string]string{"book": "A"}, ObservedAt: &observedAt},
			{ExternalID: "bad-action", Date: "2026-08-12", Action: "watch", Metric: "pages", Value: 20},
			{ExternalID: "bad-value", Date: "2026-08-12", Action: "read", Metric: "pages", Value: -1},
			{ExternalID: "book-2", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 4, Metadata: map[string]string{"book": "B"}},
		},
	}
	result, err := service.Ingest(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 2 || result.Duplicate != 0 || result.Rejected != 2 {
		t.Fatalf("Ingest() = %#v", result)
	}
	wantRejections := []IngestRejection{
		{EventID: "bad-action", Code: "action_not_allowed", Detail: "activity 1: integrations: action is not allowed"},
		{EventID: "bad-value", Code: "negative_metric_value", Detail: "activity 2: integrations: metric value must be non-negative"},
	}
	if !reflect.DeepEqual(result.Rejections, wantRejections) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, wantRejections)
	}

	environments, err := activityStore.LoadEnvironments(ctx, activity.LoadEnvironmentsInput{})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := activityStore.LoadFacts(ctx, activity.LoadFactsInput{Subject: "subject-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(environments) != 1 || environments[0].ID != "custom-provider:id-1" || environments[0].OwnerSubject == nil || *environments[0].OwnerSubject != "subject-1" {
		t.Fatalf("custom environment = %#v", environments)
	}
	if len(facts) != 2 {
		t.Fatalf("projected facts = %#v", facts)
	}
	if facts[0].Metadata["provider_event_id"] != "book-1" || facts[0].Metadata["observed_at"] != observedAt.Format(time.RFC3339Nano) {
		t.Fatalf("projected metadata = %#v", facts[0].Metadata)
	}
	timeline, err := activity.BuildTimelineFromFacts("subject-1", "Asia/Seoul", environments, facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Days) != 1 || timeline.Days[0].Count != 7 || len(timeline.Days[0].Entries) != 2 {
		t.Fatalf("timeline = %#v", timeline)
	}

	duplicate, err := service.Ingest(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Accepted != 0 || duplicate.Duplicate != 2 || duplicate.Rejected != 2 {
		t.Fatalf("duplicate Ingest() = %#v", duplicate)
	}
	facts, err = activityStore.LoadFacts(ctx, activity.LoadFactsInput{Subject: "subject-1"})
	if err != nil || len(facts) != 2 {
		t.Fatalf("facts after duplicate = %#v, %v", facts, err)
	}
	input.Activities[0].Value = 999
	input.Activities[0].Metadata["book"] = "mutated duplicate"
	mutatedDuplicate, err := service.Ingest(ctx, input)
	if err != nil || mutatedDuplicate.Accepted != 0 || mutatedDuplicate.Duplicate != 2 {
		t.Fatalf("mutated duplicate Ingest() = %#v, %v", mutatedDuplicate, err)
	}
	facts, err = activityStore.LoadFacts(ctx, activity.LoadFactsInput{Subject: "subject-1"})
	if err != nil || len(facts) != 2 || facts[0].Metric.Value != 3 || facts[0].Metadata["book"] != "A" {
		t.Fatalf("canonical fact changed after eventId reuse = %#v, %v", facts, err)
	}
}

func TestDeleteCustomProviderPurgesProjectedFactsFromMemoryStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	integrationStore := NewMemoryStore()
	activityStore := memorystore.New()
	service, err := NewCustomProviderService(
		integrationStore, mustTestCipher(), activityStore,
		&fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}, &sequentialIDs{},
	)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := service.Create(ctx, CreateCustomProviderInput{
		SubjectID: "subject-delete", EnvironmentID: "custom:delete", Slug: "delete", Name: "Delete",
		AllowedActions: []string{"custom"}, AllowedMetrics: []string{"count"}, IngestSecret: testIngestSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(ctx, IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret,
		Activities: []CustomActivity{{ExternalID: "event-delete", Date: "2026-08-12", Action: "custom", Metric: "count", Value: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, provider.ID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	facts, err := activityStore.LoadFacts(ctx, activity.LoadFactsInput{Subject: "subject-delete"})
	if err != nil || len(facts) != 0 {
		t.Fatalf("facts after delete = %#v, %v", facts, err)
	}
	if _, err := integrationStore.GetCustomProvider(ctx, provider.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("provider still exists after delete: %v", err)
	}
}

func TestCustomProviderIngestIdempotencyKeyReplaysResultAndDetectsReuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	sink := &recordingActivitySink{}
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	service, err := NewCustomProviderService(store, mustTestCipher(), sink, clock, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)
	input := IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret, IdempotencyKey: "request-0001",
		Activities: []CustomActivity{{ExternalID: "event-1", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 10}},
	}
	first, err := service.Ingest(ctx, input)
	if err != nil || first.Accepted != 1 {
		t.Fatalf("first Ingest() = %#v, %v", first, err)
	}
	second, err := service.Ingest(ctx, input)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("idempotent replay = %#v, %v; want %#v", second, err, first)
	}
	if len(sink.facts) != 1 {
		t.Fatalf("idempotent replay republished facts: %#v", sink.facts)
	}
	clock.set(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	retained, err := service.Ingest(ctx, input)
	if err != nil || !reflect.DeepEqual(retained, first) || len(sink.facts) != 1 {
		t.Fatalf("24-hour replay = %#v, %v; facts=%d", retained, err, len(sink.facts))
	}
	input.Activities[0].Value = 11
	if _, err := service.Ingest(ctx, input); !errors.Is(err, ErrConflict) || !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency key reuse error = %v", err)
	}
	input.IdempotencyKey = "short"
	if _, err := service.Ingest(ctx, input); !errors.Is(err, ErrInvalidIdempotencyKey) {
		t.Fatalf("short idempotency key error = %v", err)
	}
}

func TestCustomProviderIngestRetryRepairsFailedProjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	sink := &recordingActivitySink{failFacts: errors.New("timeline unavailable")}
	service, err := NewCustomProviderService(store, mustTestCipher(), sink, nil, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)
	input := IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret,
		Activities: []CustomActivity{{ExternalID: "event-1", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 10}},
	}
	if _, err := service.Ingest(ctx, input); err == nil || !strings.Contains(err.Error(), "publish custom activity facts") {
		t.Fatalf("failed projection error = %v", err)
	}
	ledger, err := store.ListIngestedActivities(ctx, provider.ID)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger after failure = %#v, %v", ledger, err)
	}
	sink.failFacts = nil
	result, err := service.Ingest(ctx, input)
	if err != nil || result.Accepted != 0 || result.Duplicate != 1 || len(sink.facts) != 1 {
		t.Fatalf("repairing retry = %#v, %v; facts=%#v", result, err, sink.facts)
	}
}

func TestDurableIdempotencyReservesBeforeMutationAcrossServiceInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newDurableCustomStore(true)
	sink := &recordingActivitySink{}
	clock := &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	firstService, err := NewCustomProviderService(store, mustTestCipher(), sink, clock, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	secondService, err := NewCustomProviderService(store, mustTestCipher(), sink, clock, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, firstService)
	firstInput := IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret, IdempotencyKey: "concurrent-request",
		Activities: []CustomActivity{{ExternalID: "event-1", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 10}},
	}
	firstResult := make(chan IngestCustomActivitiesResult, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := firstService.Ingest(ctx, firstInput)
		firstResult <- result
		firstErr <- err
	}()
	<-store.saveStarted

	if _, err := secondService.Ingest(ctx, firstInput); !errors.Is(err, ErrConflict) || !errors.Is(err, ErrIdempotencyInProgress) {
		t.Fatalf("same request while pending error = %v", err)
	}
	conflicting := firstInput
	conflicting.Activities = []CustomActivity{{ExternalID: "event-2", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 11}}
	if _, err := secondService.Ingest(ctx, conflicting); !errors.Is(err, ErrConflict) || !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different request while pending error = %v", err)
	}
	if calls := store.saveCallCount(); calls != 1 {
		t.Fatalf("mutations before releasing reservation owner = %d, want 1", calls)
	}
	close(store.releaseSave)
	if err := <-firstErr; err != nil {
		t.Fatalf("reservation owner ingest error = %v", err)
	}
	want := <-firstResult
	if want.Accepted != 1 {
		t.Fatalf("reservation owner result = %#v", want)
	}
	replayed, err := secondService.Ingest(ctx, firstInput)
	if err != nil || !reflect.DeepEqual(replayed, want) {
		t.Fatalf("completed replay = %#v, %v; want %#v", replayed, err, want)
	}
	if calls := store.saveCallCount(); calls != 1 {
		t.Fatalf("mutation count after replay = %d, want 1", calls)
	}
}

func TestDurableIdempotencyReleasesPendingReservationAfterProjectionFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newDurableCustomStore(false)
	sink := &recordingActivitySink{failFacts: errors.New("timeline unavailable")}
	service, err := NewCustomProviderService(
		store, mustTestCipher(), sink,
		&fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}, &sequentialIDs{},
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)
	input := IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret, IdempotencyKey: "retry-request-1",
		Activities: []CustomActivity{{ExternalID: "event-1", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 10}},
	}
	if _, err := service.Ingest(ctx, input); err == nil || !strings.Contains(err.Error(), "publish custom activity facts") {
		t.Fatalf("failed projection error = %v", err)
	}
	if store.idempotencyCount() != 0 {
		t.Fatal("failed request left a pending idempotency reservation")
	}
	sink.failFacts = nil
	retried, err := service.Ingest(ctx, input)
	if err != nil || retried.Accepted != 0 || retried.Duplicate != 1 {
		t.Fatalf("retry after released reservation = %#v, %v", retried, err)
	}
	replayed, err := service.Ingest(ctx, input)
	if err != nil || !reflect.DeepEqual(replayed, retried) {
		t.Fatalf("completed retry replay = %#v, %v; want %#v", replayed, err, retried)
	}
}

func TestCustomProviderRotationRevokesOldKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service, err := NewCustomProviderService(store, mustTestCipher(), &recordingActivitySink{}, &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)
	newSecret := strings.Repeat("rotated-", 4)
	if err := service.RotateIngestSecret(ctx, provider.ID, newSecret); err != nil {
		t.Fatal(err)
	}
	activityInput := []CustomActivity{{ExternalID: "event-1", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 1}}
	if _, err := service.Ingest(ctx, IngestCustomActivitiesInput{ProviderID: provider.ID, IngestSecret: testIngestSecret, Activities: activityInput}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old key error = %v", err)
	}
	if result, err := service.Ingest(ctx, IngestCustomActivitiesInput{ProviderID: provider.ID, IngestSecret: newSecret, Activities: activityInput}); err != nil || result.Accepted != 1 {
		t.Fatalf("new key ingest = %#v, %v", result, err)
	}
}

func TestCustomProviderValidationMatchesOpenAPILimits(t *testing.T) {
	t.Parallel()
	base := CreateCustomProviderInput{
		SubjectID: "subject-1", EnvironmentID: "custom:test", Slug: "valid_key", Name: "Valid",
		AllowedActions: []string{"read"}, AllowedMetrics: []string{"pages"}, IngestSecret: testIngestSecret,
	}
	tests := []struct {
		name string
		edit func(*CreateCustomProviderInput)
		want error
	}{
		{name: "uppercase slug", edit: func(input *CreateCustomProviderInput) { input.Slug = "Invalid" }, want: ErrInvalidProviderSlug},
		{name: "slug longer than 64", edit: func(input *CreateCustomProviderInput) { input.Slug = strings.Repeat("a", 65) }, want: ErrInvalidProviderSlug},
		{name: "name longer than 100", edit: func(input *CreateCustomProviderInput) { input.Name = strings.Repeat("가", 101) }, want: ErrProviderNameTooLong},
		{name: "description longer than 500", edit: func(input *CreateCustomProviderInput) { input.Description = strings.Repeat("a", 501) }, want: ErrProviderDescriptionTooLong},
		{name: "duplicate action", edit: func(input *CreateCustomProviderInput) { input.AllowedActions = []string{"read", "read"} }, want: ErrDuplicateAllowedAction},
		{name: "empty action", edit: func(input *CreateCustomProviderInput) { input.AllowedActions = []string{""} }, want: ErrInvalidAllowedAction},
		{name: "too many actions", edit: func(input *CreateCustomProviderInput) { input.AllowedActions = numberedStrings(101) }, want: ErrTooManyAllowedActions},
		{name: "short generated key", edit: func(input *CreateCustomProviderInput) { input.IngestSecret = strings.Repeat("x", 31) }, want: ErrIngestSecretTooShort},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if err := validateCustomProviderInput(input); !errors.Is(err, test.want) {
				t.Fatalf("validateCustomProviderInput() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCustomActivityValidationLimitsBecomePartialRejections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service, err := NewCustomProviderService(store, mustTestCipher(), &recordingActivitySink{}, &fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)
	metadata := make(map[string]string, 51)
	for index := 0; index < 51; index++ {
		metadata[string(rune('a'+index))] = "value"
	}
	result, err := service.Ingest(ctx, IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret,
		Activities: []CustomActivity{
			{ExternalID: strings.Repeat("e", 256), Date: "2026-08-12", Action: "read", Metric: "pages", Value: 1},
			{ExternalID: "event-2", Date: "2026-08-12", Action: strings.Repeat("a", 65), Metric: "pages", Value: 1},
			{ExternalID: "event-3", Date: "2026-08-12", Action: "read", Metric: strings.Repeat("m", 65), Value: 1},
			{ExternalID: "event-4", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 1, Metadata: metadata},
			{ExternalID: "event-5", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 1, Metadata: map[string]string{"long": strings.Repeat("x", 1001)}},
			{ExternalID: "event-6", Date: "2015-08-12", Action: "read", Metric: "pages", Value: 1},
			{ExternalID: "event-7", Date: "2026-08-14", Action: "read", Metric: "pages", Value: 1},
			{ExternalID: "event-8", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 1, Metadata: map[string]string{"bad key": "value"}},
			{ExternalID: "event-9", Date: "2026-08-12", Action: "read", Metric: "pages", Value: 1, Metadata: map[string]string{strings.Repeat("k", 129): "value"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 0 || result.Rejected != 9 || result.Duplicate != 0 {
		t.Fatalf("result = %#v", result)
	}
	wantCodes := []string{"event_id_too_long", "action_too_long", "metric_too_long", "too_many_metadata_properties", "metadata_value_too_long", "date_out_of_range", "date_out_of_range", "invalid_metadata_key", "invalid_metadata_key"}
	for index, want := range wantCodes {
		if result.Rejections[index].Code != want {
			t.Errorf("rejection %d code = %q, want %q", index, result.Rejections[index].Code, want)
		}
	}
}

func TestCustomActivityMetricValueCapIsAnEventLevelRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	sink := &recordingActivitySink{}
	service, err := NewCustomProviderService(
		store, mustTestCipher(), sink,
		&fixedClock{now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}, &sequentialIDs{},
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)

	result, err := service.Ingest(ctx, IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: testIngestSecret,
		Activities: []CustomActivity{
			{ExternalID: "at-cap", Date: "2026-08-12", Action: "read", Metric: "pages", Value: MaxCustomMetricValue},
			{ExternalID: "over-cap", Date: "2026-08-12", Action: "read", Metric: "pages", Value: MaxCustomMetricValue + 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || result.Duplicate != 0 || result.Rejected != 1 {
		t.Fatalf("Ingest() = %#v", result)
	}
	wantRejections := []IngestRejection{{
		EventID: "over-cap",
		Code:    "metric_value_too_large",
		Detail:  "activity 1: integrations: metric value exceeds 1000000",
	}}
	if !reflect.DeepEqual(result.Rejections, wantRejections) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, wantRejections)
	}
	if len(sink.facts) != 1 || sink.facts[0].Metric.Value != MaxCustomMetricValue {
		t.Fatalf("published facts = %#v", sink.facts)
	}
}

func TestCustomActivityBatchBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service, err := NewCustomProviderService(store, mustTestCipher(), &recordingActivitySink{}, nil, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	provider := mustCreateCustomProvider(t, service)
	base := IngestCustomActivitiesInput{ProviderID: provider.ID, IngestSecret: testIngestSecret}
	if _, err := service.Ingest(ctx, base); !errors.Is(err, ErrEmptyActivities) {
		t.Fatalf("empty batch error = %v", err)
	}
	base.Activities = make([]CustomActivity, MaxCustomIngestBatch+1)
	if _, err := service.Ingest(ctx, base); !errors.Is(err, ErrTooManyActivities) {
		t.Fatalf("oversized batch error = %v", err)
	}
}

func mustCreateCustomProvider(t *testing.T, service *CustomProviderService) CustomProvider {
	t.Helper()
	provider, err := service.Create(context.Background(), CreateCustomProviderInput{
		SubjectID: "subject-1", EnvironmentID: "custom:reading", Slug: "reading", Name: "Reading",
		AllowedActions: []string{"read"}, AllowedMetrics: []string{"pages"}, IngestSecret: testIngestSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func numberedStrings(count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = time.Unix(int64(index), 0).UTC().Format("150405.000000000")
	}
	return result
}

type durableCustomStore struct {
	*MemoryStore

	mu          sync.Mutex
	idempotency map[string]IngestIdempotencyRecord
	saveCalls   int
	saveStarted chan struct{}
	releaseSave chan struct{}
	blockFirst  bool
}

func newDurableCustomStore(blockFirst bool) *durableCustomStore {
	return &durableCustomStore{
		MemoryStore: NewMemoryStore(), idempotency: make(map[string]IngestIdempotencyRecord),
		saveStarted: make(chan struct{}), releaseSave: make(chan struct{}), blockFirst: blockFirst,
	}
}

func (store *durableCustomStore) SaveIngestedActivities(ctx context.Context, activities []IngestedActivity) ([]IngestedActivity, error) {
	store.mu.Lock()
	store.saveCalls++
	call := store.saveCalls
	store.mu.Unlock()
	if store.blockFirst && call == 1 {
		close(store.saveStarted)
		select {
		case <-store.releaseSave:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return store.MemoryStore.SaveIngestedActivities(ctx, activities)
}

func (store *durableCustomStore) CreateIngestIdempotencyKey(_ context.Context, record IngestIdempotencyRecord) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := durableCustomIdempotencyKey(record.ProviderID, record.KeyHash)
	if _, exists := store.idempotency[key]; exists {
		return false, nil
	}
	store.idempotency[key] = cloneIngestIdempotencyRecord(record)
	return true, nil
}

func (store *durableCustomStore) GetIngestIdempotencyKey(_ context.Context, providerID string, keyHash []byte) (IngestIdempotencyRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.idempotency[durableCustomIdempotencyKey(providerID, keyHash)]
	return cloneIngestIdempotencyRecord(record), found, nil
}

func (store *durableCustomStore) CompleteIngestIdempotencyKey(_ context.Context, record IngestIdempotencyRecord) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := durableCustomIdempotencyKey(record.ProviderID, record.KeyHash)
	pending, found := store.idempotency[key]
	if !found || pending.ResponseStatus != 0 || !bytes.Equal(pending.RequestHash, record.RequestHash) {
		return false, nil
	}
	store.idempotency[key] = cloneIngestIdempotencyRecord(record)
	return true, nil
}

func (store *durableCustomStore) ReleaseIngestIdempotencyKey(_ context.Context, providerID string, keyHash, requestHash []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := durableCustomIdempotencyKey(providerID, keyHash)
	record, found := store.idempotency[key]
	if found && record.ResponseStatus == 0 && bytes.Equal(record.RequestHash, requestHash) {
		delete(store.idempotency, key)
	}
	return nil
}

func (store *durableCustomStore) saveCallCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveCalls
}

func (store *durableCustomStore) idempotencyCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.idempotency)
}

func durableCustomIdempotencyKey(providerID string, keyHash []byte) string {
	return providerID + "\x00" + string(keyHash)
}

func cloneIngestIdempotencyRecord(record IngestIdempotencyRecord) IngestIdempotencyRecord {
	record.KeyHash = append([]byte(nil), record.KeyHash...)
	record.RequestHash = append([]byte(nil), record.RequestHash...)
	record.ResponseBody = append([]byte(nil), record.ResponseBody...)
	return record
}
