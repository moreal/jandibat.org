package activity

import (
	"regexp"
	"testing"
	"time"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func revisionFixture() SnapshotRevisionInput {
	owner := domain.SubjectID("alice")
	updated := time.Date(2024, 2, 29, 12, 30, 0, 0, time.UTC)
	return SnapshotRevisionInput{
		Timeline: domain.Timeline{
			Subject:  owner,
			Timezone: "Asia/Seoul",
			Environments: []domain.Environment{
				{ID: "private", Key: "custom", Name: "Private", Scope: domain.EnvironmentScopeSubject, OwnerSubject: &owner, Metadata: map[string]string{"z": "2", "a": "1"}},
				{ID: "public", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
			},
			Days: []domain.Day{
				{Date: "2024-02-29", Count: 2, Level: domain.LevelLow, Entries: []domain.DayEntry{
					{EnvironmentID: "private", Action: domain.ActionCustom, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}, Metadata: map[string]string{"b": "2", "a": "1"}},
					{EnvironmentID: "public", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}},
				}},
				{Date: "2024-03-01", Count: 0, Level: domain.LevelNone},
			},
		},
		From:           "2024-02-29",
		To:             "2024-03-01",
		Timezone:       "Asia/Seoul",
		EnvironmentIDs: []domain.EnvironmentID{"public", "private"},
		Audience:       AudienceOwner,
		DataUpdatedAt:  &updated,
		GeneratedAt:    time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestSnapshotRevisionIsOpaqueAndStableForEquivalentAuthorizedResults(t *testing.T) {
	base := revisionFixture()
	first := ComputeSnapshotRevision(base)
	if !regexp.MustCompile(`^v1:[a-f0-9]{64}$`).MatchString(first) {
		t.Fatalf("revision is not a versioned opaque SHA-256 digest: %q", first)
	}
	other := revisionFixture()
	other.GeneratedAt = other.GeneratedAt.Add(time.Hour)
	other.Timeline.Environments[0], other.Timeline.Environments[1] = other.Timeline.Environments[1], other.Timeline.Environments[0]
	other.Timeline.Days[0], other.Timeline.Days[1] = other.Timeline.Days[1], other.Timeline.Days[0]
	other.Timeline.Days[1].Entries[0], other.Timeline.Days[1].Entries[1] = other.Timeline.Days[1].Entries[1], other.Timeline.Days[1].Entries[0]
	other.EnvironmentIDs = []domain.EnvironmentID{"private", "public", "private"}
	other.Timeline.Environments[1].Metadata = map[string]string{"a": "1", "z": "2"}
	other.Timeline.Days[1].Entries[1].Metadata = map[string]string{"a": "1", "b": "2"}
	if got := ComputeSnapshotRevision(other); got != first {
		t.Fatalf("equivalent authorized result changed revision: %q != %q", got, first)
	}
}

func TestSnapshotRevisionChangesForVisibleDataAndQueryScope(t *testing.T) {
	base := revisionFixture()
	wantDifferentFrom := ComputeSnapshotRevision(base)
	tests := map[string]func(*SnapshotRevisionInput){
		"count":                func(input *SnapshotRevisionInput) { input.Timeline.Days[0].Count++ },
		"date":                 func(input *SnapshotRevisionInput) { input.Timeline.Days[0].Date = "2024-02-28" },
		"entry environment":    func(input *SnapshotRevisionInput) { input.Timeline.Days[0].Entries[0].EnvironmentID = "public" },
		"entry metadata":       func(input *SnapshotRevisionInput) { input.Timeline.Days[0].Entries[0].Metadata["a"] = "changed" },
		"environment metadata": func(input *SnapshotRevisionInput) { input.Timeline.Environments[0].Metadata["a"] = "changed" },
		"visibility":           func(input *SnapshotRevisionInput) { input.Audience = AudienceAnonymous },
		"nonowner visibility":  func(input *SnapshotRevisionInput) { input.Audience = AudienceAuthenticatedNonOwner },
		"data updated": func(input *SnapshotRevisionInput) {
			later := input.DataUpdatedAt.Add(time.Second)
			input.DataUpdatedAt = &later
		},
		"from":            func(input *SnapshotRevisionInput) { input.From = "2024-02-28" },
		"to":              func(input *SnapshotRevisionInput) { input.To = "2024-03-02" },
		"timezone":        func(input *SnapshotRevisionInput) { input.Timezone = "UTC" },
		"result timezone": func(input *SnapshotRevisionInput) { input.Timeline.Timezone = "UTC" },
		"selected environment": func(input *SnapshotRevisionInput) {
			input.EnvironmentIDs = []domain.EnvironmentID{"private"}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			input := revisionFixture()
			change(&input)
			if got := ComputeSnapshotRevision(input); got == wantDifferentFrom {
				t.Fatalf("%s change did not change revision", name)
			}
		})
	}
}

func TestSnapshotRevisionDistinguishesAllAudienceScopes(t *testing.T) {
	seen := make(map[string]bool)
	for _, scope := range []AudienceScope{AudienceAnonymous, AudienceOwner, AudienceAuthenticatedNonOwner} {
		input := revisionFixture()
		input.Audience = scope
		revision := ComputeSnapshotRevision(input)
		if seen[revision] {
			t.Fatalf("audience %q shared revision with another audience", scope)
		}
		seen[revision] = true
	}
}

func TestSnapshotRevisionEmptyNeverSeenSnapshotIsStable(t *testing.T) {
	input := SnapshotRevisionInput{
		Timeline: domain.Timeline{Subject: "never-seen", Timezone: "UTC"},
		From:     "2026-02-01", To: "2026-02-28", Timezone: "UTC", Audience: AudienceAnonymous,
		GeneratedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
	first := ComputeSnapshotRevision(input)
	input.GeneratedAt = input.GeneratedAt.Add(time.Minute)
	if got := ComputeSnapshotRevision(input); got != first {
		t.Fatalf("empty snapshot changed only with generation time: %q != %q", got, first)
	}
	updated := input.GeneratedAt
	input.DataUpdatedAt = &updated
	if got := ComputeSnapshotRevision(input); got == first {
		t.Fatal("nil dataUpdatedAt and a known data update must have different revisions")
	}
}

func TestSnapshotRevisionNormalizesNilAndEmptyVisibleCollections(t *testing.T) {
	base := revisionFixture()
	want := ComputeSnapshotRevision(base)
	tests := map[string]func(*SnapshotRevisionInput){
		"empty day entries": func(input *SnapshotRevisionInput) {
			input.Timeline.Days[1].Entries = []domain.DayEntry{}
		},
		"empty environment metadata": func(input *SnapshotRevisionInput) {
			input.Timeline.Environments[1].Metadata = map[string]string{}
		},
		"empty entry metadata": func(input *SnapshotRevisionInput) {
			input.Timeline.Days[0].Entries[1].Metadata = map[string]string{}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			input := revisionFixture()
			change(&input)
			if got := ComputeSnapshotRevision(input); got != want {
				t.Fatalf("equivalent empty values changed revision: %q != %q", got, want)
			}
		})
	}

	empty := SnapshotRevisionInput{Timeline: domain.Timeline{Subject: "never-seen", Timezone: "UTC"}, From: "2026-01-01", To: "2026-01-02", Timezone: "UTC", Audience: AudienceAnonymous}
	emptyWant := ComputeSnapshotRevision(empty)
	empty.Timeline.Environments = []domain.Environment{}
	empty.Timeline.Days = []domain.Day{}
	if got := ComputeSnapshotRevision(empty); got != emptyWant {
		t.Fatalf("nil and empty top-level result collections changed revision: %q != %q", got, emptyWant)
	}
}
