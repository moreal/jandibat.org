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

// snapshotMemoryStore keeps the real memory fact/environment behavior while
// providing the consistent-read port normally supplied by CockroachDB.
type snapshotMemoryStore struct {
	*memory.Store
	updatedAt    *time.Time
	onProjection func()
}

func (store *snapshotMemoryStore) LoadSnapshotProjection(
	ctx context.Context,
	filter domain.LoadFactsInput,
	environmentIDs []domain.EnvironmentID,
	includePrivate bool,
) ([]domain.Fact, []domain.Environment, *time.Time, error) {
	if store.onProjection != nil {
		store.onProjection()
	}
	facts, err := store.LoadFacts(ctx, filter)
	if err != nil {
		return nil, nil, nil, err
	}
	selected := make(map[domain.EnvironmentID]bool, len(environmentIDs))
	for _, id := range environmentIDs {
		selected[id] = true
	}
	visibleFacts := make([]domain.Fact, 0, len(facts))
	for _, fact := range facts {
		if len(selected) == 0 || selected[fact.EnvironmentID] {
			visibleFacts = append(visibleFacts, fact)
		}
	}
	environments, err := store.LoadEnvironments(ctx, domain.LoadEnvironmentsInput{IDs: environmentIDsForTest(visibleFacts)})
	if err != nil {
		return nil, nil, nil, err
	}
	visibleEnvironments := make([]domain.Environment, 0, len(environments))
	allowed := make(map[domain.EnvironmentID]bool, len(environments))
	for _, environment := range environments {
		if environment.Scope == domain.EnvironmentScopeSubject &&
			(environment.OwnerSubject == nil || *environment.OwnerSubject != filter.Subject ||
				(environment.Metadata["visibility"] == "private" && !includePrivate)) {
			continue
		}
		visibleEnvironments = append(visibleEnvironments, environment)
		allowed[environment.ID] = true
	}
	keptFacts := visibleFacts[:0]
	for _, fact := range visibleFacts {
		if allowed[fact.EnvironmentID] {
			keptFacts = append(keptFacts, fact)
		}
	}
	return keptFacts, visibleEnvironments, store.updatedAt, nil
}

func environmentIDsForTest(facts []domain.Fact) []domain.EnvironmentID {
	ids := make([]domain.EnvironmentID, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.EnvironmentID)
	}
	return ids
}

func TestSnapshotGeneratedAtChangesWithoutChangingRevision(t *testing.T) {
	store := &snapshotMemoryStore{Store: memory.New()}
	ctx := context.Background()
	date := domain.Date("2026-03-03")
	environment := domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal}
	if err := store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{Environments: []domain.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFacts(ctx, domain.SaveFactsInput{Subject: "moreal", Facts: []domain.Fact{{
		Subject: "moreal", Date: date, EnvironmentID: "github", Action: domain.ActionCommit,
		Metric: domain.Metric{Name: domain.MetricCount, Value: 4},
	}}}); err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC)
	store.updatedAt = &updatedAt
	clock := time.Date(2026, 3, 3, 2, 0, 0, 0, time.UTC)
	usecase, err := app.NewGetTimeline(store, nil, app.GetTimelineOptions{Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	input := app.SnapshotInput{GetTimelineInput: app.GetTimelineInput{Subject: "moreal", From: &date, To: &date}, Audience: app.AudienceAnonymous}
	first, err := usecase.ExecuteSnapshot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	second, err := usecase.ExecuteSnapshot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.GeneratedAt.Equal(clock.Add(-time.Minute)) || !second.GeneratedAt.Equal(clock) {
		t.Fatalf("generatedAt first=%v second=%v", first.GeneratedAt, second.GeneratedAt)
	}
	if first.DataUpdatedAt == nil || !first.DataUpdatedAt.Equal(updatedAt) || second.DataUpdatedAt == nil {
		t.Fatalf("dataUpdatedAt first=%v second=%v", first.DataUpdatedAt, second.DataUpdatedAt)
	}
	if first.Revision == "" || first.Revision != second.Revision {
		t.Fatalf("revision first=%q second=%q", first.Revision, second.Revision)
	}
	if len(second.Timeline.Days) != 1 || second.Timeline.Days[0].Count != 4 {
		t.Fatalf("timeline=%#v", second.Timeline)
	}
}

func TestSnapshotGeneratedAtIsStampedAfterProjection(t *testing.T) {
	store := &snapshotMemoryStore{Store: memory.New()}
	before := time.Date(2026, 3, 3, 2, 0, 0, 0, time.UTC)
	after := before.Add(5 * time.Millisecond)
	clock := before
	store.onProjection = func() { clock = after }
	usecase, err := app.NewGetTimeline(store, nil, app.GetTimelineOptions{Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-03-03")
	output, err := usecase.ExecuteSnapshot(context.Background(), app.SnapshotInput{
		GetTimelineInput: app.GetTimelineInput{Subject: "never-seen", From: &date, To: &date},
		Audience:         app.AudienceAnonymous,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.GeneratedAt.Equal(after) || output.DataUpdatedAt != nil || output.Revision == "" {
		t.Fatalf("provenance=%#v", output)
	}
	if len(output.Timeline.Days) != 0 {
		t.Fatalf("empty days=%#v", output.Timeline.Days)
	}
}

func TestSnapshotAudienceControlsPrivateFactsAndRevision(t *testing.T) {
	store := &snapshotMemoryStore{Store: memory.New()}
	ctx := context.Background()
	subject := domain.SubjectID("alice")
	date := domain.Date("2026-08-12")
	environments := []domain.Environment{
		{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
		{ID: "connection:alice", Key: "github", Name: "Private GitHub", Scope: domain.EnvironmentScopeSubject, OwnerSubject: &subject, Metadata: map[string]string{"visibility": "private"}},
	}
	if err := store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{Environments: environments}); err != nil {
		t.Fatal(err)
	}
	facts := []domain.Fact{
		{Subject: subject, Date: date, EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}},
		{Subject: subject, Date: date, EnvironmentID: "connection:alice", Action: domain.ActionIssue, Metric: domain.Metric{Name: domain.MetricCount, Value: 2}},
	}
	if err := store.SaveFacts(ctx, domain.SaveFactsInput{Subject: subject, Facts: facts}); err != nil {
		t.Fatal(err)
	}
	usecase, err := app.NewGetTimeline(store, nil, app.GetTimelineOptions{Now: func() time.Time {
		return time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	base := app.GetTimelineInput{Subject: subject, From: &date, To: &date, IncludeSubjectEnvironments: true}
	anonymous, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: app.AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	nonowner, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: app.AudienceAuthenticatedNonOwner})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: app.AudienceOwner})
	if err != nil {
		t.Fatal(err)
	}
	if anonymous.Timeline.Days[0].Count != 1 || nonowner.Timeline.Days[0].Count != 1 || owner.Timeline.Days[0].Count != 3 {
		t.Fatalf("counts anonymous=%#v nonowner=%#v owner=%#v", anonymous.Timeline.Days, nonowner.Timeline.Days, owner.Timeline.Days)
	}
	if anonymous.Revision == nonowner.Revision || anonymous.Revision == owner.Revision || nonowner.Revision == owner.Revision {
		t.Fatalf("audience revisions anonymous=%q nonowner=%q owner=%q", anonymous.Revision, nonowner.Revision, owner.Revision)
	}
}

func TestSnapshotRangeIncludesLeapDayAndRejectsInvalidInputs(t *testing.T) {
	store := &snapshotMemoryStore{Store: memory.New()}
	ctx := context.Background()
	environment := domain.Environment{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal}
	if err := store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{Environments: []domain.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFacts(ctx, domain.SaveFactsInput{Subject: "alice", Facts: []domain.Fact{
		{Subject: "alice", Date: "2024-02-28", EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}},
		{Subject: "alice", Date: "2024-02-29", EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 2}},
		{Subject: "alice", Date: "2024-03-01", EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 3}},
	}}); err != nil {
		t.Fatal(err)
	}
	usecase, err := app.NewGetTimeline(store, nil, app.GetTimelineOptions{Now: func() time.Time {
		return time.Date(2024, 3, 1, 23, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	from, to := domain.Date("2024-02-28"), domain.Date("2024-03-01")
	base := app.GetTimelineInput{Subject: "alice", From: &from, To: &to, Timezone: "Asia/Seoul"}
	output, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: app.AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Timeline.Days) != 3 || output.Timeline.Days[0].Date != from || output.Timeline.Days[1].Date != "2024-02-29" || output.Timeline.Days[2].Date != to {
		t.Fatalf("inclusive leap range=%#v", output.Timeline.Days)
	}
	badZone := base
	badZone.Timezone = "Mars/Olympus"
	if _, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: badZone, Audience: app.AudienceAnonymous}); !errors.Is(err, app.ErrInvalidDate) {
		t.Fatalf("invalid timezone error=%v", err)
	}
	tooLong := base
	longFrom := domain.Date("2023-03-01")
	tooLong.From = &longFrom
	if _, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: tooLong, Audience: app.AudienceAnonymous}); !errors.Is(err, app.ErrDateRangeTooLarge) {
		t.Fatalf("long range error=%v", err)
	}
	if _, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: "unknown"}); !errors.Is(err, app.ErrInvalidAudience) {
		t.Fatalf("invalid audience error=%v", err)
	}
}

func TestSnapshotDuplicateEnvironmentSelectionHasSameResultAndRevision(t *testing.T) {
	store := &snapshotMemoryStore{Store: memory.New()}
	ctx := context.Background()
	if err := store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{Environments: []domain.Environment{
		{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal},
		{ID: "gitlab", Key: "gitlab", Name: "GitLab", Scope: domain.EnvironmentScopeGlobal},
	}}); err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-03-03")
	if err := store.SaveFacts(ctx, domain.SaveFactsInput{Subject: "alice", Facts: []domain.Fact{
		{Subject: "alice", Date: date, EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}},
		{Subject: "alice", Date: date, EnvironmentID: "gitlab", Action: domain.ActionIssue, Metric: domain.Metric{Name: domain.MetricCount, Value: 2}},
	}}); err != nil {
		t.Fatal(err)
	}
	usecase, err := app.NewGetTimeline(store, nil, app.GetTimelineOptions{Now: func() time.Time {
		return time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	base := app.GetTimelineInput{Subject: "alice", From: &date, To: &date, EnvironmentIDs: []domain.EnvironmentID{"github"}}
	one, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: app.AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	base.EnvironmentIDs = []domain.EnvironmentID{"github", "github"}
	duplicate, err := usecase.ExecuteSnapshot(ctx, app.SnapshotInput{GetTimelineInput: base, Audience: app.AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	if one.Timeline.Days[0].Count != 1 || duplicate.Timeline.Days[0].Count != 1 || one.Revision != duplicate.Revision {
		t.Fatalf("selected once=%#v duplicate=%#v", one, duplicate)
	}
}

func TestSnapshotFailedRefreshUsesConsistentStaleProjection(t *testing.T) {
	store := &snapshotMemoryStore{Store: memory.New()}
	ctx := context.Background()
	environment := domain.Environment{ID: "forge:github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal}
	if err := store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{Environments: []domain.Environment{environment}}); err != nil {
		t.Fatal(err)
	}
	date := domain.Date("2026-03-03")
	provider := &fakeProvider{environment: environment, err: errors.New("provider down")}
	usecase, err := app.NewGetTimeline(store, []app.Provider{provider}, app.GetTimelineOptions{Now: func() time.Time {
		return time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := app.SnapshotInput{GetTimelineInput: app.GetTimelineInput{Subject: "alice", From: &date, To: &date}, Audience: app.AudienceAnonymous}
	if _, err := usecase.ExecuteSnapshot(ctx, input); !errors.Is(err, app.ErrProviderUnavailable) {
		t.Fatalf("empty failed refresh error=%v", err)
	}
	if err := store.SaveFacts(ctx, domain.SaveFactsInput{Subject: "alice", Facts: []domain.Fact{{
		Subject: "alice", Date: date, EnvironmentID: "forge:github", Action: domain.ActionCommit,
		Metric: domain.Metric{Name: domain.MetricCount, Value: 7},
	}}}); err != nil {
		t.Fatal(err)
	}
	stale, err := usecase.ExecuteSnapshot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || len(stale.Warnings) != 1 || len(stale.Timeline.Days) != 1 || stale.Timeline.Days[0].Count != 7 {
		t.Fatalf("stale snapshot=%#v", stale)
	}
}
