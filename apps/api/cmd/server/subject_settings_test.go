package main

import (
	"context"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestRuntimeSubjectSettingsMapsDurablePreferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := subjects.NewMemoryRepository()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	service, err := subjects.NewService(repository, subjects.Config{
		Now: func() time.Time { return now }, NewID: func() (string, error) { return "subject-1", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	user := subjects.User{
		ID: "user-1", PrimaryEmail: "owner@example.test", Status: subjects.UserStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := service.ProvisionUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSubject(ctx, user.ID, subjects.CreateSubjectInput{Handle: "owner", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	timezone := "Asia/Seoul"
	disabled := false
	interval := 45
	policy := subjects.FailurePurge
	if _, err := service.UpdateSubjectSettings(ctx, user.ID, "subject-1", subjects.UpdateSubjectSettingsInput{
		Timezone: &timezone, SyncEnabled: &disabled, SyncIntervalMinutes: &interval, FailurePolicy: &policy,
	}); err != nil {
		t.Fatal(err)
	}

	reader := runtimeSubjectSettings{repository: repository}
	timeline, found, err := reader.LoadSubjectTimelineSettings(ctx, "subject-1")
	if err != nil || !found {
		t.Fatalf("timeline settings found=%v error=%v", found, err)
	}
	if timeline.Timezone != timezone || timeline.FailurePolicy != activity.FetchFailurePurge {
		t.Fatalf("timeline settings = %#v", timeline)
	}
	syncSettings, found, err := reader.LoadSubjectSyncSettings(ctx, "subject-1")
	if err != nil || !found {
		t.Fatalf("sync settings found=%v error=%v", found, err)
	}
	if syncSettings.Timezone != timezone || syncSettings.Enabled || syncSettings.Interval != 45*time.Minute || syncSettings.FailurePolicy != activity.FetchFailurePurge {
		t.Fatalf("sync settings = %#v", syncSettings)
	}
	if _, found, err := reader.LoadSubjectSyncSettings(ctx, "unmanaged"); err != nil || found {
		t.Fatalf("unmanaged settings found=%v error=%v", found, err)
	}
}
