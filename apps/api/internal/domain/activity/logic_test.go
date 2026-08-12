package activity

import (
	"errors"
	"testing"
	"time"
)

func TestLevelForCount(t *testing.T) {
	tests := []struct {
		count int
		want  Level
	}{
		{count: 0, want: LevelNone},
		{count: 2, want: LevelLow},
		{count: 5, want: LevelMedium},
		{count: 9, want: LevelHigh},
		{count: 10, want: LevelVeryHigh},
	}

	for _, tc := range tests {
		if got := LevelForCount(tc.count); got != tc.want {
			t.Fatalf("count %d: expected %d, got %d", tc.count, tc.want, got)
		}
	}
}

func TestAddMetricValuesRejectsNegativeValuesAndIntegerOverflow(t *testing.T) {
	t.Parallel()
	maximum := int(^uint(0) >> 1)

	if got, err := AddMetricValues(maximum-1, 1); err != nil || got != maximum {
		t.Fatalf("AddMetricValues() at integer limit = %d, %v; want %d, nil", got, err, maximum)
	}
	if _, err := AddMetricValues(maximum, 1); !errors.Is(err, ErrMetricAggregationOverflow) {
		t.Fatalf("AddMetricValues() overflow error = %v, want %v", err, ErrMetricAggregationOverflow)
	}
	if _, err := AddMetricValues(1, -1); !errors.Is(err, ErrNegativeMetricValue) {
		t.Fatalf("AddMetricValues() negative error = %v, want %v", err, ErrNegativeMetricValue)
	}
}

func TestBuildTimelineFromFactsRejectsMetricAggregationOverflow(t *testing.T) {
	t.Parallel()
	environment := Environment{ID: "env", Key: "env", Name: "Environment", Scope: EnvironmentScopeGlobal}
	maximum := int(^uint(0) >> 1)
	fact := func(value int) Fact {
		return Fact{
			Subject: "subject", Date: "2026-08-12", EnvironmentID: environment.ID,
			Action: ActionCustom, Metric: Metric{Name: MetricCount, Value: value},
		}
	}

	if _, err := BuildTimelineFromFacts("subject", "UTC", []Environment{environment}, []Fact{fact(maximum), fact(1)}); !errors.Is(err, ErrMetricAggregationOverflow) {
		t.Fatalf("BuildTimelineFromFacts() overflow error = %v, want %v", err, ErrMetricAggregationOverflow)
	}
}

func TestBuildTimelineFromFactsSortsAndNormalizes(t *testing.T) {
	owner := SubjectID("moreal")
	githubID := EnvironmentID("env-github")
	bookLogID := EnvironmentID("env-booklog")
	environments := []Environment{
		{
			ID:    githubID,
			Key:   "github",
			Name:  "GitHub",
			Scope: EnvironmentScopeGlobal,
		},
		{
			ID:           bookLogID,
			Key:          "book-log",
			Name:         "Book Log",
			Scope:        EnvironmentScopeSubject,
			OwnerSubject: &owner,
		},
	}

	timeline, err := BuildTimelineFromFacts("moreal", "Asia/Seoul", environments, []Fact{
		{
			Subject:       "moreal",
			Date:          "2026-03-02",
			EnvironmentID: githubID,
			Action:        ActionCommit,
			Metric:        Metric{Name: MetricCount, Value: 7},
			Metadata: map[string]string{
				"repo": "jandibat.org",
			},
		},
		{
			Subject:       "moreal",
			Date:          "2026-03-01",
			EnvironmentID: bookLogID,
			Action:        ActionCustom,
			Metric:        Metric{Name: MetricCount, Value: 1},
			Metadata: map[string]string{
				"source": "book-log",
			},
		},
		{
			Subject:       "moreal",
			Date:          "2026-03-02",
			EnvironmentID: githubID,
			Action:        ActionCommit,
			Metric:        Metric{Name: MetricCount, Value: 3},
			Metadata: map[string]string{
				"repo": "jandibat.org",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if timeline.Days[0].Date != "2026-03-01" {
		t.Fatalf("expected first day 2026-03-01, got %s", timeline.Days[0].Date)
	}

	if timeline.Days[1].Count != 10 {
		t.Fatalf("expected second day count 10, got %d", timeline.Days[1].Count)
	}

	if timeline.Days[1].Level != LevelVeryHigh {
		t.Fatalf("expected second day level %d, got %d", LevelVeryHigh, timeline.Days[1].Level)
	}

	if len(timeline.Days[1].Entries) != 2 {
		t.Fatalf("expected 2 entries for second day, got %d", len(timeline.Days[1].Entries))
	}

	if timeline.Days[1].Entries[0].EnvironmentID != githubID {
		t.Fatalf("expected first entry environment %q, got %q", githubID, timeline.Days[1].Entries[0].EnvironmentID)
	}

	if timeline.Days[1].Entries[0].Metadata["repo"] != "jandibat.org" {
		t.Fatal("expected metadata to be preserved in day entry")
	}

	if len(timeline.Environments) != 2 {
		t.Fatalf("expected 2 used environments, got %d", len(timeline.Environments))
	}
}

func TestBuildTimelineFromFactsRejectsMismatchedSubject(t *testing.T) {
	environments := []Environment{
		{
			ID:    "env-github",
			Key:   "github",
			Name:  "GitHub",
			Scope: EnvironmentScopeGlobal,
		},
	}

	_, err := BuildTimelineFromFacts("moreal", "Asia/Seoul", environments, []Fact{
		{
			Subject:       "someone-else",
			Date:          "2026-03-02",
			EnvironmentID: "env-github",
			Action:        ActionCommit,
			Metric:        Metric{Name: MetricCount, Value: 1},
		},
	})
	if err == nil {
		t.Fatal("expected error for mismatched subject")
	}
}

func TestBuildTimelineFromFactsRejectsUnknownEnvironment(t *testing.T) {
	environments := []Environment{
		{
			ID:    "env-github",
			Key:   "github",
			Name:  "GitHub",
			Scope: EnvironmentScopeGlobal,
		},
	}

	_, err := BuildTimelineFromFacts("moreal", "Asia/Seoul", environments, []Fact{
		{
			Subject:       "moreal",
			Date:          "2026-03-02",
			EnvironmentID: "env-not-found",
			Action:        ActionCommit,
			Metric:        Metric{Name: MetricCount, Value: 1},
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown environment id")
	}
}

func TestShouldRefresh(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	cached := now.Add(-2 * time.Minute)

	policy := CachePolicy{
		HotTTL:  5 * time.Minute,
		ColdTTL: 0,
	}

	refreshHot, err := ShouldRefresh(now, &cached, "2026-03-02", "2026-03-02", policy, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refreshHot {
		t.Fatal("expected no refresh for hot record within ttl")
	}

	refreshForce, err := ShouldRefresh(now, &cached, "2026-03-01", "2026-03-02", policy, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !refreshForce {
		t.Fatal("expected refresh when force=true")
	}
}

func TestResolveFactsOnFetchFailure(t *testing.T) {
	existing := []Fact{
		{
			Subject:       "moreal",
			Date:          "2026-03-02",
			EnvironmentID: "env-github",
			Action:        ActionCommit,
			Metric:        Metric{Name: MetricCount, Value: 1},
			Metadata:      map[string]string{"repo": "jandibat.org"},
		},
	}

	kept, err := ResolveFactsOnFetchFailure(existing, FetchFailureKeepStale)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept fact, got %d", len(kept))
	}

	purged, err := ResolveFactsOnFetchFailure(existing, FetchFailurePurge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(purged) != 0 {
		t.Fatalf("expected 0 purged facts, got %d", len(purged))
	}
}
