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

type timelineSettingsReader struct {
	settings app.SubjectTimelineSettings
	found    bool
	err      error
	calls    int
}

func (reader *timelineSettingsReader) LoadSubjectTimelineSettings(_ context.Context, _ domain.SubjectID) (app.SubjectTimelineSettings, bool, error) {
	reader.calls++
	return reader.settings, reader.found, reader.err
}

type inputCapturingProvider struct {
	input       app.ProviderFetchInput
	environment domain.Environment
	err         error
}

func (provider *inputCapturingProvider) Environment() domain.Environment { return provider.environment }
func (provider *inputCapturingProvider) Fetch(_ context.Context, input app.ProviderFetchInput) ([]domain.Fact, error) {
	provider.input = input
	return nil, provider.err
}

func TestGetTimelineUsesManagedSubjectTimezoneAndAllowsExplicitOverride(t *testing.T) {
	t.Parallel()
	reader := &timelineSettingsReader{found: true, settings: app.SubjectTimelineSettings{
		Timezone: "Asia/Seoul", FailurePolicy: domain.FetchFailurePurge,
	}}
	provider := &inputCapturingProvider{environment: domain.Environment{
		ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal,
	}}
	usecase, err := app.NewGetTimeline(memory.New(), []app.Provider{provider}, app.GetTimelineOptions{
		SubjectSettings: reader,
		Now:             func() time.Time { return time.Date(2026, 8, 11, 16, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-08-12")
	output, err := usecase.Execute(context.Background(), app.GetTimelineInput{Subject: "subject-1", From: &date, To: &date})
	if err != nil {
		t.Fatal(err)
	}
	if output.Timeline.Timezone != "Asia/Seoul" || provider.input.Timezone != "Asia/Seoul" {
		t.Fatalf("settings timezone not applied: output=%q provider=%q", output.Timeline.Timezone, provider.input.Timezone)
	}

	provider.input = app.ProviderFetchInput{}
	output, err = usecase.Execute(context.Background(), app.GetTimelineInput{
		Subject: "subject-1", ProviderSubject: "owner-handle", Timezone: "America/New_York", FailurePolicy: domain.FetchFailureKeepStale,
		From: &date, To: &date, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Timeline.Timezone != "America/New_York" || provider.input.Timezone != "America/New_York" {
		t.Fatalf("explicit timezone did not override settings: output=%q provider=%q", output.Timeline.Timezone, provider.input.Timezone)
	}
	if provider.input.Subject != "subject-1" || provider.input.ProviderSubject != "owner-handle" {
		t.Fatalf("local/provider subject separation = %#v", provider.input)
	}
	if reader.calls != 1 {
		t.Fatalf("settings reader calls = %d, want 1; fully explicit requests should not load defaults", reader.calls)
	}
}

func TestGetTimelineUsesManagedSubjectPurgePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memory.New()
	environment := domain.Environment{ID: "gitlab", Key: "gitlab", Name: "GitLab", Scope: domain.EnvironmentScopeGlobal}
	if err := store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{Environments: []domain.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-08-12")
	if err := store.SaveFacts(ctx, domain.SaveFactsInput{Subject: "subject-1", Facts: []domain.Fact{{
		Subject: "subject-1", Date: date, EnvironmentID: environment.ID, Action: domain.ActionCommit,
		Metric: domain.Metric{Name: domain.MetricCount, Value: 1},
	}}}); err != nil {
		t.Fatal(err)
	}
	reader := &timelineSettingsReader{found: true, settings: app.SubjectTimelineSettings{
		Timezone: "UTC", FailurePolicy: domain.FetchFailurePurge,
	}}
	provider := &inputCapturingProvider{environment: environment, err: errors.New("provider unavailable")}
	usecase, err := app.NewGetTimeline(store, []app.Provider{provider}, app.GetTimelineOptions{
		SubjectSettings: reader,
		Now:             func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usecase.Execute(ctx, app.GetTimelineInput{Subject: "subject-1", From: &date, To: &date}); !errors.Is(err, app.ErrProviderUnavailable) {
		t.Fatalf("purge-policy error = %v, want provider unavailable after stale facts are removed", err)
	}
	facts, err := store.LoadFacts(ctx, domain.LoadFactsInput{Subject: "subject-1", From: &date, To: &date})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("managed purge policy retained facts: %#v", facts)
	}
}

func TestGetTimelineFallsBackForUnmanagedSubjectAndPropagatesSettingsErrors(t *testing.T) {
	t.Parallel()
	date := domain.Date("2026-08-12")
	reader := &timelineSettingsReader{}
	usecase, err := app.NewGetTimeline(memory.New(), nil, app.GetTimelineOptions{
		DefaultTimezone: "UTC", FailurePolicy: domain.FetchFailureKeepStale, SubjectSettings: reader,
		Now: func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := usecase.Execute(context.Background(), app.GetTimelineInput{Subject: "public-handle", From: &date, To: &date})
	if err != nil {
		t.Fatal(err)
	}
	if output.Timeline.Timezone != "UTC" {
		t.Fatalf("unmanaged subject timezone = %q, want UTC fallback", output.Timeline.Timezone)
	}

	sentinel := errors.New("settings unavailable")
	reader.err = sentinel
	if _, err := usecase.Execute(context.Background(), app.GetTimelineInput{Subject: "subject-1", From: &date, To: &date}); !errors.Is(err, sentinel) {
		t.Fatalf("settings error = %v, want %v", err, sentinel)
	}
}
