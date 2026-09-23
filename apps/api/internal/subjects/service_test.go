package subjects

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServiceSubjectLifecycleAndOwnership(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 9, 30, 0, 0, time.UTC)
	ids := []string{"sub_owner", "sub_other"}
	service := newTestService(t, now, func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	provisionTestUser(t, service, "owner", "owner@example.com", now)
	provisionTestUser(t, service, "other", "other@example.com", now)

	profile, err := service.GetCurrentUser(ctx, "owner")
	if err != nil {
		t.Fatalf("GetCurrentUser() error = %v", err)
	}
	if profile.PrimaryEmail != "owner@example.com" || profile.Status != UserStatusActive {
		t.Fatalf("GetCurrentUser() = %#v", profile)
	}
	settings, err := service.GetUserSettings(ctx, "owner")
	if err != nil {
		t.Fatalf("GetUserSettings() error = %v", err)
	}
	if settings.Locale != "en-US" || settings.Timezone != "UTC" || settings.Theme != ThemeSystem {
		t.Fatalf("default user settings = %#v", settings)
	}

	locale, timezone, theme := "ko-KR", "Asia/Seoul", ThemeDark
	settings, err = service.UpdateUserSettings(ctx, "owner", UpdateUserSettingsInput{
		Locale: &locale, Timezone: &timezone, Theme: &theme,
	})
	if err != nil {
		t.Fatalf("UpdateUserSettings() error = %v", err)
	}
	if settings.Locale != locale || settings.Timezone != timezone || settings.Theme != theme {
		t.Fatalf("updated user settings = %#v", settings)
	}

	displayName := "Owner's activity"
	subject, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{
		Handle: "owner.activity", DisplayName: &displayName, Timezone: "Asia/Seoul",
	})
	if err != nil {
		t.Fatalf("CreateSubject() error = %v", err)
	}
	if subject.ID != "sub_owner" || !subject.IsPublic || subject.OwnerUserID != "owner" {
		t.Fatalf("created subject = %#v", subject)
	}
	if got, err := service.GetSubject(ctx, "", "owner.activity"); err != nil || got.ID != subject.ID {
		t.Fatalf("anonymous GetSubject() = %#v, %v", got, err)
	}

	subjectSettings, err := service.GetSubjectSettings(ctx, "owner", subject.ID)
	if err != nil {
		t.Fatalf("GetSubjectSettings() error = %v", err)
	}
	if subjectSettings.DefaultTheme != ThemeSystem || subjectSettings.WeekStart != WeekStartSunday ||
		!subjectSettings.SyncEnabled || subjectSettings.SyncIntervalMinutes != 60 || subjectSettings.FailurePolicy != FailureKeepStale {
		t.Fatalf("default subject settings = %#v", subjectSettings)
	}

	isPublic := false
	interval := 120
	policy := FailurePurge
	subjectSettings, err = service.UpdateSubjectSettings(ctx, "owner", subject.Handle, UpdateSubjectSettingsInput{
		IsPublic: &isPublic, SyncIntervalMinutes: &interval, FailurePolicy: &policy,
	})
	if err != nil {
		t.Fatalf("UpdateSubjectSettings() error = %v", err)
	}
	if subjectSettings.IsPublic || subjectSettings.SyncIntervalMinutes != interval || subjectSettings.FailurePolicy != policy {
		t.Fatalf("updated subject settings = %#v", subjectSettings)
	}
	if _, err := service.GetSubject(ctx, "", subject.Handle); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous private GetSubject() error = %v, want forbidden", err)
	}
	if _, err := service.GetSubjectSettings(ctx, "other", subject.Handle); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner GetSubjectSettings() error = %v, want forbidden", err)
	}
	privateSubject, err := service.GetSubject(ctx, "owner", subject.Handle)
	if err != nil || privateSubject.IsPublic {
		t.Fatalf("owner private GetSubject() = %#v, %v", privateSubject, err)
	}

	newHandle := "renamed"
	updated, err := service.UpdateSubject(ctx, "owner", subject.ID, UpdateSubjectInput{
		Handle: &newHandle, DisplayNameSet: true, DisplayName: nil,
	})
	if err != nil {
		t.Fatalf("UpdateSubject() error = %v", err)
	}
	if updated.Handle != newHandle || updated.DisplayName != nil || updated.Timezone != "Asia/Seoul" || updated.IsPublic {
		t.Fatalf("updated subject = %#v", updated)
	}
	if _, err := service.GetSubject(ctx, "owner", subject.Handle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old handle GetSubject() error = %v, want not found", err)
	}
	if err := service.DeleteSubject(ctx, "other", newHandle); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner DeleteSubject() error = %v, want forbidden", err)
	}
	if err := service.DeleteSubject(ctx, "owner", newHandle); err != nil {
		t.Fatalf("DeleteSubject() error = %v", err)
	}
	if _, err := service.GetSubject(ctx, "owner", newHandle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted GetSubject() error = %v, want not found", err)
	}
}

func TestCreateSubjectAtomicallyClaimsOwnerlessShadowAndPreservesIdentity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 9, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	ids := []string{"new-owner-id", "new-other-id"}
	service, err := NewService(repository, Config{Now: func() time.Time { return now }, NewID: func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	provisionTestUser(t, service, "owner", "owner@example.com", now)
	provisionTestUser(t, service, "other", "other@example.com", now)
	shadow := Subject{ID: "public-handle", Handle: "public-handle", Timezone: "UTC", IsPublic: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	repository.mu.Lock()
	repository.subjects[shadow.ID] = shadow
	repository.subjectIDByLookup[shadow.ID] = shadow.ID
	repository.mu.Unlock()

	claimed, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: shadow.Handle, Timezone: "Asia/Seoul"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != shadow.ID || claimed.OwnerUserID != "owner" || claimed.Handle != shadow.Handle || claimed.Timezone != "Asia/Seoul" {
		t.Fatalf("claimed subject = %#v", claimed)
	}
	if _, err := service.CreateSubject(ctx, "other", CreateSubjectInput{Handle: shadow.Handle, Timezone: "UTC"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second owner claim = %v, want conflict", err)
	}
	settings, err := repository.GetSubjectSettings(ctx, shadow.ID)
	if err != nil || settings.SubjectID != shadow.ID {
		t.Fatalf("claimed settings = %#v, %v", settings, err)
	}
}

func TestServiceListSubjectsUsesStableCursorAndOwnerScope(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	ids := []string{"sub_c", "sub_a", "sub_b", "sub_other"}
	service := newTestService(t, now, func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	provisionTestUser(t, service, "owner", "owner@example.com", now)
	provisionTestUser(t, service, "other", "other@example.com", now)
	for _, handle := range []string{"charlie", "alpha", "bravo"} {
		if _, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: handle, Timezone: "UTC"}); err != nil {
			t.Fatalf("CreateSubject(%q) error = %v", handle, err)
		}
	}
	if _, err := service.CreateSubject(ctx, "other", CreateSubjectInput{Handle: "outsider", Timezone: "UTC"}); err != nil {
		t.Fatalf("CreateSubject(other) error = %v", err)
	}

	first, err := service.ListSubjects(ctx, "owner", ListSubjectsInput{Limit: 2})
	if err != nil {
		t.Fatalf("first ListSubjects() error = %v", err)
	}
	if got := subjectIDs(first.Subjects); fmt.Sprint(got) != fmt.Sprint([]string{"sub_a", "sub_b"}) {
		t.Fatalf("first ids = %v", got)
	}
	if !first.PageInfo.HasNextPage || first.PageInfo.NextCursor == nil {
		t.Fatalf("first pageInfo = %#v", first.PageInfo)
	}
	second, err := service.ListSubjects(ctx, "owner", ListSubjectsInput{Limit: 2, Cursor: *first.PageInfo.NextCursor})
	if err != nil {
		t.Fatalf("second ListSubjects() error = %v", err)
	}
	if got := subjectIDs(second.Subjects); fmt.Sprint(got) != fmt.Sprint([]string{"sub_c"}) {
		t.Fatalf("second ids = %v", got)
	}
	if second.PageInfo.HasNextPage || second.PageInfo.NextCursor != nil {
		t.Fatalf("second pageInfo = %#v", second.PageInfo)
	}
}

func TestServiceListSubjectsPageUsesTypedKeysetAfterDeletedAnchor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	ids := []string{"sub_c", "sub_a", "sub_b", "sub_other"}
	service := newTestService(t, now, func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	provisionTestUser(t, service, "owner", "owner@example.com", now)
	provisionTestUser(t, service, "other", "other@example.com", now)
	for _, handle := range []string{"charlie", "alpha", "bravo"} {
		if _, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: handle, Timezone: "UTC"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.CreateSubject(ctx, "other", CreateSubjectInput{Handle: "outsider", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}

	first, err := service.ListSubjectsPage(ctx, "owner", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectIDs(first.Subjects); fmt.Sprint(got) != fmt.Sprint([]string{"sub_a", "sub_b"}) || !first.HasNextPage {
		t.Fatalf("first page = %v, next=%t", got, first.HasNextPage)
	}
	anchor := SubjectCursor{CreatedAt: first.Subjects[1].CreatedAt, ID: first.Subjects[1].ID}
	if err := service.DeleteSubject(ctx, "owner", anchor.ID); err != nil {
		t.Fatal(err)
	}
	second, err := service.ListSubjectsPage(ctx, "owner", &anchor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectIDs(second.Subjects); fmt.Sprint(got) != fmt.Sprint([]string{"sub_c"}) || second.HasNextPage {
		t.Fatalf("second page after deleted anchor = %v, next=%t", got, second.HasNextPage)
	}
	if empty, err := service.ListSubjectsPage(ctx, "owner", &SubjectCursor{CreatedAt: now, ID: "sub_z"}, 2); err != nil || len(empty.Subjects) != 0 || empty.HasNextPage {
		t.Fatalf("empty page = %+v, %v", empty, err)
	}
}

func TestServiceListSubjectsPageRejectsInvalidBoundsAndOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	service := newTestService(t, now, func() (string, error) { return "unused", nil })
	provisionTestUser(t, service, "owner", "owner@example.com", now)
	for _, first := range []int{-1, 0, 101} {
		if _, err := service.ListSubjectsPage(ctx, "owner", nil, first); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("first %d error = %v, want invalid input", first, err)
		}
	}
	for _, cursor := range []SubjectCursor{
		{ID: "subject"},
		{CreatedAt: now},
		{CreatedAt: now, ID: strings.Repeat("x", 65)},
		{CreatedAt: now, ID: "subject\x00id"},
		{CreatedAt: now, ID: "subject\nid"},
		{CreatedAt: now, ID: string([]byte{0xff})},
	} {
		if _, err := service.ListSubjectsPage(ctx, "owner", &cursor, 1); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cursor id %q error = %v, want invalid input", cursor.ID, err)
		}
	}
	validMultibyte := SubjectCursor{CreatedAt: now, ID: strings.Repeat("界", 64)}
	if _, err := service.ListSubjectsPage(ctx, "owner", &validMultibyte, 1); err != nil {
		t.Fatalf("64-rune multibyte cursor error = %v", err)
	}
	for _, owner := range []string{"", "unknown"} {
		if _, err := service.ListSubjectsPage(ctx, owner, nil, 1); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("owner %q error = %v, want unauthenticated", owner, err)
		}
	}
}

func TestServiceValidationAndConflicts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 10, 30, 0, 0, time.UTC)
	ids := []string{"first", "second", "third", "fourth"}
	service := newTestService(t, now, func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	provisionTestUser(t, service, "owner", "owner@example.com", now)

	if _, err := service.CreateSubject(ctx, "", CreateSubjectInput{Handle: "valid", Timezone: "UTC"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unauthenticated CreateSubject() error = %v", err)
	}
	if _, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: "bad handle", Timezone: "UTC"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid handle error = %v", err)
	}
	if _, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: "valid", Timezone: "Mars/Olympus"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid timezone error = %v", err)
	}
	if _, err := service.UpdateUserSettings(ctx, "owner", UpdateUserSettingsInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty user patch error = %v", err)
	}
	if _, err := service.ListSubjects(ctx, "owner", ListSubjectsInput{Limit: 101}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid limit error = %v", err)
	}
	if _, err := service.ListSubjects(ctx, "owner", ListSubjectsInput{Cursor: "%%%"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid cursor error = %v", err)
	}

	first, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: "first-handle", Timezone: "UTC"})
	if err != nil {
		t.Fatalf("CreateSubject(first) error = %v", err)
	}
	if _, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: first.ID, Timezone: "UTC"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("identifier conflict error = %v", err)
	}
	interval := 14
	if _, err := service.UpdateSubjectSettings(ctx, "owner", first.ID, UpdateSubjectSettingsInput{SyncIntervalMinutes: &interval}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid interval error = %v", err)
	}
	unknownTheme := HeatmapTheme("sepia")
	if _, err := service.UpdateSubjectSettings(ctx, "owner", first.ID, UpdateSubjectSettingsInput{DefaultTheme: &unknownTheme}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid theme error = %v", err)
	}
}

func TestNonActiveUsersCannotExerciseSubjectOwnershipOrMutations(t *testing.T) {
	t.Parallel()
	for _, status := range []UserStatus{UserStatusDisabled, UserStatusPending, UserStatusDeletionPending} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
			repository := NewMemoryRepository()
			service, err := NewService(repository, Config{Now: func() time.Time { return now }, NewID: func() (string, error) { return "sub-one", nil }})
			if err != nil {
				t.Fatal(err)
			}
			provisionTestUser(t, service, "owner", "owner@example.com", now)
			subject, err := service.CreateSubject(ctx, "owner", CreateSubjectInput{Handle: "owned", Timezone: "UTC"})
			if err != nil {
				t.Fatal(err)
			}
			isPublic := false
			if _, err := service.UpdateSubjectSettings(ctx, "owner", subject.ID, UpdateSubjectSettingsInput{IsPublic: &isPublic}); err != nil {
				t.Fatal(err)
			}

			repository.mu.Lock()
			user := repository.users["owner"]
			user.Status = status
			repository.users[user.ID] = user
			repository.mu.Unlock()

			// Re-provisioning from a stale authentication result must not undo an
			// account lifecycle transition.
			stale := user
			stale.Status = UserStatusActive
			if err := service.ProvisionUser(ctx, stale); err != nil {
				t.Fatal(err)
			}
			stored, err := repository.GetUser(ctx, user.ID)
			if err != nil || stored.Status != status {
				t.Fatalf("re-provisioned status = %q, error=%v", stored.Status, err)
			}

			if _, err := service.GetCurrentUser(ctx, user.ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("GetCurrentUser() error = %v, want ErrForbidden", err)
			}
			if owned, err := service.OwnsSubject(ctx, user.ID, subject.ID); owned || !errors.Is(err, ErrForbidden) {
				t.Fatalf("OwnsSubject() = (%t, %v), want false/ErrForbidden", owned, err)
			}
			if _, err := service.GetSubject(ctx, user.ID, subject.ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("private GetSubject() error = %v, want ErrForbidden", err)
			}
			newHandle := "changed"
			if _, err := service.UpdateSubject(ctx, user.ID, subject.ID, UpdateSubjectInput{Handle: &newHandle}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("UpdateSubject() error = %v, want ErrForbidden", err)
			}
			if _, err := service.AuthorizeSubjectDeletion(ctx, user.ID, subject.ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("AuthorizeSubjectDeletion() error = %v, want ErrForbidden", err)
			}
		})
	}
}

func TestMemoryRepositoryDefensiveCopiesAndConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 12, 11, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	verifiedAt := now
	user := User{
		ID: "owner", PrimaryEmail: "owner@example.com", EmailVerifiedAt: &verifiedAt,
		Status: UserStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SaveUser(ctx, user, defaultUserSettings(now)); err != nil {
		t.Fatalf("SaveUser() error = %v", err)
	}
	name := "Original"
	subject := Subject{
		ID: "sub_one", OwnerUserID: user.ID, Handle: "original", DisplayName: &name,
		Timezone: "UTC", IsPublic: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSubject(ctx, subject, defaultSubjectSettings(subject, now)); err != nil {
		t.Fatalf("CreateSubject() error = %v", err)
	}
	name = "Mutated outside"
	got, err := repository.GetSubject(ctx, subject.ID)
	if err != nil {
		t.Fatalf("GetSubject() error = %v", err)
	}
	if got.DisplayName == nil || *got.DisplayName != "Original" {
		t.Fatalf("stored display name = %v", got.DisplayName)
	}
	*got.DisplayName = "Mutated return"
	again, _ := repository.GetSubject(ctx, subject.ID)
	if again.DisplayName == nil || *again.DisplayName != "Original" {
		t.Fatalf("repository leaked returned pointer: %#v", again)
	}

	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if worker%2 == 0 {
					_, _ = repository.GetSubject(ctx, subject.ID)
					_, _ = repository.ListSubjects(ctx, user.ID, nil, 10)
					continue
				}
				settings, err := repository.GetUserSettings(ctx, user.ID)
				if err == nil {
					settings.Theme = ThemeLight
					_ = repository.SaveUserSettings(ctx, user.ID, settings)
				}
			}
		}(worker)
	}
	wait.Wait()
}

func newTestService(t *testing.T, now time.Time, newID func() (string, error)) *Service {
	t.Helper()
	service, err := NewService(NewMemoryRepository(), Config{
		Now: func() time.Time { return now }, NewID: newID,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func provisionTestUser(t *testing.T, service *Service, id, email string, now time.Time) {
	t.Helper()
	verifiedAt := now
	err := service.ProvisionUser(context.Background(), User{
		ID: id, PrimaryEmail: email, EmailVerifiedAt: &verifiedAt,
		Status: UserStatusActive, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("ProvisionUser(%q) error = %v", id, err)
	}
}

func subjectIDs(subjects []Subject) []string {
	ids := make([]string, len(subjects))
	for index, subject := range subjects {
		ids[index] = subject.ID
	}
	return ids
}
