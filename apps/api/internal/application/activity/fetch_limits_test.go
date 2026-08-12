package activity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/memory"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type controlledProvider struct {
	environment domain.Environment
	started     chan ProviderFetchInput
	release     <-chan struct{}
	empty       bool
	calls       atomic.Int32
}

func (provider *controlledProvider) Environment() domain.Environment { return provider.environment }

func (provider *controlledProvider) Fetch(ctx context.Context, input ProviderFetchInput) ([]domain.Fact, error) {
	provider.calls.Add(1)
	if provider.started != nil {
		provider.started <- input
	}
	if provider.release != nil {
		select {
		case <-provider.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if provider.empty {
		return []domain.Fact{}, nil
	}
	return []domain.Fact{{
		Subject: input.Subject, Date: input.From, EnvironmentID: provider.environment.ID,
		Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1},
	}}, nil
}

func TestRefreshProviderSingleflightCoalescesIdenticalMisses(t *testing.T) {
	release := make(chan struct{})
	provider := &controlledProvider{
		environment: domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
		started:     make(chan ProviderFetchInput, 2), release: release,
	}
	store := memory.New()
	usecase, err := NewGetTimeline(store, []Provider{provider}, GetTimelineOptions{GlobalFetchConcurrency: 2, ProviderFetchConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-08-12")
	filter := domain.LoadFactsInput{Subject: "octocat", From: &date, To: &date}
	results := make(chan error, 2)
	go func() {
		results <- usecase.refreshProvider(context.Background(), provider, filter.Subject, filter, []domain.Date{date}, time.Now(), "UTC", date, date)
	}()
	<-provider.started
	go func() {
		results <- usecase.refreshProvider(context.Background(), provider, filter.Subject, filter, []domain.Date{date}, time.Now(), "UTC", date, date)
	}()
	// Keep the leader blocked long enough for the second call to join the same
	// singleflight key. A duplicate outbound call would signal started again.
	select {
	case duplicate := <-provider.started:
		t.Fatalf("duplicate provider fetch started before leader completed: %#v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestRefreshProviderRejectsGlobalAndPerProviderSaturation(t *testing.T) {
	tests := []struct {
		name        string
		global      int
		perProvider int
	}{
		{name: "global", global: 1, perProvider: 4},
		{name: "provider", global: 4, perProvider: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := make(chan struct{})
			provider := &controlledProvider{
				environment: domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
				started:     make(chan ProviderFetchInput, 2), release: release,
			}
			usecase, err := NewGetTimeline(memory.New(), []Provider{provider}, GetTimelineOptions{
				GlobalFetchConcurrency: test.global, ProviderFetchConcurrency: test.perProvider,
			})
			if err != nil {
				t.Fatal(err)
			}
			date := domain.Date("2026-08-12")
			firstFilter := domain.LoadFactsInput{Subject: "first", From: &date, To: &date}
			first := make(chan error, 1)
			go func() {
				first <- usecase.refreshProvider(context.Background(), provider, firstFilter.Subject, firstFilter, []domain.Date{date}, time.Now(), "UTC", date, date)
			}()
			<-provider.started

			secondFilter := domain.LoadFactsInput{Subject: "second", From: &date, To: &date}
			err = usecase.refreshProvider(context.Background(), provider, secondFilter.Subject, secondFilter, []domain.Date{date}, time.Now(), "UTC", date, date)
			if !errors.Is(err, ErrProviderSaturated) {
				t.Fatalf("saturated fetch error = %v", err)
			}
			close(release)
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			if calls := provider.calls.Load(); calls != 1 {
				t.Fatalf("provider calls = %d, want 1", calls)
			}
		})
	}
}

func TestUnknownEmptyProviderResultDoesNotCreateDurableState(t *testing.T) {
	store := memory.New()
	provider := &controlledProvider{
		environment: domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
		empty:       true,
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	usecase, err := NewGetTimeline(store, []Provider{provider}, GetTimelineOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-08-12")
	input := GetTimelineInput{Subject: "attacker-random-handle", Timezone: "UTC", From: &date, To: &date}
	for range 2 {
		if _, err := usecase.Execute(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	exists, err := store.SubjectExists(context.Background(), input.Subject)
	if err != nil {
		t.Fatal(err)
	}
	environments, err := store.LoadEnvironments(context.Background(), domain.LoadEnvironmentsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if exists || len(environments) != 0 {
		t.Fatalf("empty unknown lookup persisted state: exists=%t environments=%#v", exists, environments)
	}
	if calls := provider.calls.Load(); calls != 2 {
		t.Fatalf("provider calls = %d, want 2 without a durable negative cache", calls)
	}
}
