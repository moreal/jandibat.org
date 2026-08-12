package activity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/memory"
	app "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type fakeProvider struct {
	environment domain.Environment
	facts       []domain.Fact
	err         error
	calls       int
}

func (provider *fakeProvider) Environment() domain.Environment { return provider.environment }
func (provider *fakeProvider) Fetch(context.Context, app.ProviderFetchInput) ([]domain.Fact, error) {
	provider.calls++
	return provider.facts, provider.err
}

func TestGetTimelineFetchesThenUsesCache(t *testing.T) {
	store := memory.New()
	provider := &fakeProvider{
		environment: domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
		facts:       []domain.Fact{{Subject: "moreal", Date: "2026-03-03", EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 4}}},
	}
	now := time.Date(2026, 3, 3, 2, 0, 0, 0, time.UTC)
	usecase, err := app.NewGetTimeline(store, []app.Provider{provider}, app.GetTimelineOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-03-03")
	input := app.GetTimelineInput{Subject: "moreal", Timezone: "UTC", From: &date, To: &date}
	first, err := usecase.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := usecase.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Refreshed || second.Refreshed || provider.calls != 1 {
		t.Fatalf("unexpected refresh state: first=%v second=%v calls=%d", first.Refreshed, second.Refreshed, provider.calls)
	}
	if len(second.Timeline.Days) != 1 || second.Timeline.Days[0].Count != 4 {
		t.Fatalf("unexpected timeline: %+v", second.Timeline)
	}
}

func TestGetTimelineKeepsStaleFactsOnFetchFailure(t *testing.T) {
	store := memory.New()
	environment := domain.Environment{ID: "gitlab", Key: "gitlab", Name: "GitLab", Scope: domain.EnvironmentScopeGlobal}
	if err := store.SaveEnvironments(context.Background(), domain.SaveEnvironmentsInput{Environments: []domain.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFacts(context.Background(), domain.SaveFactsInput{Subject: "moreal", Facts: []domain.Fact{{Subject: "moreal", Date: "2026-03-03", EnvironmentID: "gitlab", Action: domain.ActionIssue, Metric: domain.Metric{Name: domain.MetricCount, Value: 2}}}}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{environment: environment, err: errors.New("unavailable")}
	usecase, err := app.NewGetTimeline(store, []app.Provider{provider}, app.GetTimelineOptions{Now: func() time.Time { return time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-03-03")
	output, err := usecase.Execute(context.Background(), app.GetTimelineInput{Subject: "moreal", From: &date, To: &date})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Stale || len(output.Warnings) != 1 || output.Timeline.Days[0].Count != 2 {
		t.Fatalf("unexpected stale output: %+v", output)
	}
}

func TestGetTimelineSucceedsWhenOtherProvidersReturnEmptySnapshots(t *testing.T) {
	store := memory.New()
	providers := []*fakeProvider{
		{
			environment: domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
			facts: []domain.Fact{{
				Subject: "octocat", Date: "2026-03-03", EnvironmentID: "github", Action: domain.ActionCommit,
				Metric: domain.Metric{Name: domain.MetricCount, Value: 4},
			}},
		},
		{environment: domain.Environment{ID: "gitlab", Key: "gitlab", Name: "GitLab", Scope: domain.EnvironmentScopeGlobal}, facts: []domain.Fact{}},
		{environment: domain.Environment{ID: "codeberg", Key: "codeberg", Name: "Codeberg", Scope: domain.EnvironmentScopeGlobal}, facts: []domain.Fact{}},
	}
	providerPorts := make([]app.Provider, 0, len(providers))
	for _, provider := range providers {
		providerPorts = append(providerPorts, provider)
	}
	usecase, err := app.NewGetTimeline(store, providerPorts, app.GetTimelineOptions{Now: func() time.Time {
		return time.Date(2026, 3, 3, 2, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-03-03")
	output, err := usecase.Execute(context.Background(), app.GetTimelineInput{
		Subject: "octocat", Timezone: "UTC", From: &date, To: &date,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Warnings) != 0 || !output.Refreshed || len(output.Timeline.Days) != 1 || output.Timeline.Days[0].Count != 4 {
		t.Fatalf("unexpected output: %#v", output)
	}
	if len(output.Timeline.Environments) != 1 || output.Timeline.Environments[0].ID != "github" {
		t.Fatalf("timeline environments = %#v", output.Timeline.Environments)
	}
	for _, provider := range providers {
		if provider.calls != 1 {
			t.Fatalf("provider %s calls=%d", provider.environment.ID, provider.calls)
		}
	}
}

func TestGetTimelineFiltersSubjectScopedFactsUnlessOwnerAccessIsEnabled(t *testing.T) {
	t.Parallel()
	store := memory.New()
	alice := domain.SubjectID("alice")
	bob := domain.SubjectID("bob")
	environments := []domain.Environment{
		{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
		{ID: "custom:reading", Key: "reading", Name: "Reading", Scope: domain.EnvironmentScopeSubject, OwnerSubject: &alice},
		{ID: "connection:alice", Key: "github", Name: "Private GitHub", Scope: domain.EnvironmentScopeSubject, OwnerSubject: &alice, Metadata: map[string]string{"visibility": "private"}},
		{ID: "connection:bob", Key: "gitlab", Name: "Wrong Owner", Scope: domain.EnvironmentScopeSubject, OwnerSubject: &bob, Metadata: map[string]string{"visibility": "private"}},
	}
	if err := store.SaveEnvironments(context.Background(), domain.SaveEnvironmentsInput{Environments: environments}); err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-08-12")
	facts := []domain.Fact{
		{Subject: alice, Date: date, EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}},
		{Subject: alice, Date: date, EnvironmentID: "custom:reading", Action: domain.ActionCustom, Metric: domain.Metric{Name: domain.MetricCount, Value: 8}},
		{Subject: alice, Date: date, EnvironmentID: "connection:alice", Action: domain.ActionIssue, Metric: domain.Metric{Name: domain.MetricCount, Value: 2}},
		{Subject: alice, Date: date, EnvironmentID: "connection:bob", Action: domain.ActionPr, Metric: domain.Metric{Name: domain.MetricCount, Value: 4}},
	}
	if err := store.SaveFacts(context.Background(), domain.SaveFactsInput{Subject: alice, Facts: facts}); err != nil {
		t.Fatal(err)
	}
	usecase, err := app.NewGetTimeline(store, nil, app.GetTimelineOptions{Now: func() time.Time {
		return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}

	public, err := usecase.Execute(context.Background(), app.GetTimelineInput{Subject: alice, From: &date, To: &date})
	if err != nil {
		t.Fatal(err)
	}
	if len(public.Timeline.Environments) != 2 ||
		len(public.Timeline.Days) != 1 || public.Timeline.Days[0].Count != 9 {
		t.Fatalf("public timeline leaked subject facts: %#v", public.Timeline)
	}
	publicEnvironmentIDs := map[domain.EnvironmentID]bool{}
	for _, environment := range public.Timeline.Environments {
		publicEnvironmentIDs[environment.ID] = true
	}
	if !publicEnvironmentIDs["github"] || !publicEnvironmentIDs["custom:reading"] || publicEnvironmentIDs["connection:alice"] {
		t.Fatalf("public environment projection = %#v", public.Timeline.Environments)
	}

	owner, err := usecase.Execute(context.Background(), app.GetTimelineInput{
		Subject: alice, From: &date, To: &date, IncludeSubjectEnvironments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.Timeline.Environments) != 3 || len(owner.Timeline.Days) != 1 || owner.Timeline.Days[0].Count != 11 {
		t.Fatalf("owner timeline did not include its private facts: %#v", owner.Timeline)
	}
	for _, environment := range owner.Timeline.Environments {
		if environment.ID == "connection:bob" {
			t.Fatal("owner timeline included another subject's environment")
		}
	}

	privateOnly := domain.EnvironmentID("connection:alice")
	selected, err := usecase.Execute(context.Background(), app.GetTimelineInput{
		Subject: alice, From: &date, To: &date, EnvironmentIDs: []domain.EnvironmentID{privateOnly},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Timeline.Environments) != 0 || len(selected.Timeline.Days) != 0 {
		t.Fatalf("explicit private environment selection bypassed visibility: %#v", selected.Timeline)
	}
}
