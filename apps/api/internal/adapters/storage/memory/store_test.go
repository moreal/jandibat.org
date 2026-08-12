package memory

import (
	"context"
	"sync"
	"testing"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func TestStoreClonesAndRangeReplacementIsScoped(t *testing.T) {
	store := New()
	metadata := map[string]string{"repo": "original"}
	facts := []domain.Fact{
		{Subject: "s", Date: "2026-03-01", EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}, Metadata: metadata},
		{Subject: "s", Date: "2026-03-02", EnvironmentID: "custom", Action: domain.ActionCustom, Metric: domain.Metric{Name: domain.MetricCount, Value: 3}},
	}
	if err := store.SaveFacts(context.Background(), domain.SaveFactsInput{Subject: "s", Facts: facts}); err != nil {
		t.Fatal(err)
	}
	metadata["repo"] = "mutated"
	date := domain.Date("2026-03-01")
	if err := store.ReplaceFacts(context.Background(), domain.LoadFactsInput{Subject: "s", From: &date, To: &date}, []domain.EnvironmentID{"github"}, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadFacts(context.Background(), domain.LoadFactsInput{Subject: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].EnvironmentID != "custom" {
		t.Fatalf("unexpected facts after scoped replacement: %+v", loaded)
	}
}

func TestStoreAllowsConcurrentAccess(t *testing.T) {
	store := New()
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(2)
		go func() {
			defer group.Done()
			_ = store.SaveFacts(context.Background(), domain.SaveFactsInput{Subject: "s", Facts: []domain.Fact{{Subject: "s", Date: "2026-03-01", EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}}}})
		}()
		go func() {
			defer group.Done()
			_, _ = store.LoadFacts(context.Background(), domain.LoadFactsInput{Subject: "s"})
		}()
	}
	group.Wait()
}
