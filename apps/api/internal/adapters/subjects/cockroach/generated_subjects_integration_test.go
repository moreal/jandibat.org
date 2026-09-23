//go:build integration

package cockroach

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestGeneratedSubjectRepositoryVerticalSlice(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	userID := "scythe-subject-user-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, primary_email) VALUES ($1, $2)`, userID, userID+"@example.invalid"); err != nil {
		t.Fatalf("create test owner: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	baseID := "scythe-subject-" + suffix
	settings := subjects.SubjectSettings{
		Timezone: "UTC", IsPublic: true, DefaultTheme: subjects.ThemeSystem,
		WeekStart: subjects.WeekStartSunday, SyncEnabled: true,
		SyncIntervalMinutes: 60, FailurePolicy: subjects.FailureKeepStale, UpdatedAt: now,
	}
	first := subjects.Subject{
		ID: baseID + "-a", OwnerUserID: userID, Handle: "probe-" + suffix + "-a",
		Timezone: "UTC", IsPublic: true, CreatedAt: now, UpdatedAt: now,
	}
	second := subjects.Subject{
		ID: baseID + "-b", OwnerUserID: userID, Handle: "probe-" + suffix + "-b",
		Timezone: "UTC", IsPublic: true, CreatedAt: now, UpdatedAt: now,
	}
	for _, subject := range []subjects.Subject{first, second} {
		initial := settings
		initial.SubjectID = subject.ID
		created, err := store.ClaimOrCreateSubject(ctx, subject, initial)
		if err != nil || created.ID != subject.ID {
			t.Fatalf("create subject %s = (%+v, %v)", subject.ID, created, err)
		}
	}

	byID, err := store.GetSubject(ctx, first.ID)
	if err != nil || byID.Handle != first.Handle || byID.DisplayName != nil {
		t.Fatalf("load nullable subject by ID = (%+v, %v)", byID, err)
	}
	byHandle, err := store.GetSubject(ctx, second.Handle)
	if err != nil || byHandle.ID != second.ID {
		t.Fatalf("load subject by handle = (%+v, %v)", byHandle, err)
	}
	if _, err := store.ListSubjects(ctx, userID, nil, 0); !errors.Is(err, subjects.ErrInvalidInput) {
		t.Fatalf("zero list limit error = %v", err)
	}

	pageOne, err := store.ListSubjects(ctx, userID, nil, 1)
	if err != nil || len(pageOne) != 1 {
		t.Fatalf("first page = (%+v, %v)", pageOne, err)
	}
	pageTwo, err := store.ListSubjects(ctx, userID, &subjects.SubjectCursor{
		CreatedAt: pageOne[0].CreatedAt, ID: pageOne[0].ID,
	}, 1)
	if err != nil || len(pageTwo) != 1 || pageTwo[0].ID == pageOne[0].ID {
		t.Fatalf("equal-timestamp second page = (%+v, %v)", pageTwo, err)
	}
	seen := map[string]bool{pageOne[0].ID: true, pageTwo[0].ID: true}
	if !seen[first.ID] || !seen[second.ID] {
		t.Fatalf("stable cursor skipped or duplicated subjects: %v", seen)
	}

	first.Handle = "renamed-" + suffix
	first.UpdatedAt = now.Add(time.Second)
	if err := store.SaveSubject(ctx, first); err != nil {
		t.Fatalf("update subject = %v", err)
	}
	updated, err := store.GetSubject(ctx, first.ID)
	if err != nil || updated.Handle != first.Handle {
		t.Fatalf("load updated subject = (%+v, %v)", updated, err)
	}

	gotSettings, err := store.GetSubjectSettings(ctx, second.ID)
	if err != nil || gotSettings.SubjectID != second.ID || gotSettings.DefaultTheme != subjects.ThemeSystem {
		t.Fatalf("load settings = (%+v, %v)", gotSettings, err)
	}
	gotSettings.Timezone = "Asia/Seoul"
	gotSettings.IsPublic = false
	gotSettings.UpdatedAt = now.Add(2 * time.Second)
	if err := store.SaveSubjectSettings(ctx, gotSettings); err != nil {
		t.Fatalf("update settings = %v", err)
	}
	updatedSettings, err := store.GetSubjectSettings(ctx, second.ID)
	if err != nil || updatedSettings.Timezone != "Asia/Seoul" || updatedSettings.IsPublic {
		t.Fatalf("load updated settings = (%+v, %v)", updatedSettings, err)
	}

	if err := store.DeleteSubject(ctx, first.ID); err != nil {
		t.Fatalf("delete subject = %v", err)
	}
	if _, err := store.GetSubject(ctx, first.ID); !errors.Is(err, subjects.ErrNotFound) {
		t.Fatalf("deleted subject lookup error = %v", err)
	}
	if err := store.DeleteSubject(ctx, second.ID); err != nil {
		t.Fatalf("delete second subject = %v", err)
	}
	if _, err := store.GetSubject(ctx, fmt.Sprintf("%s-missing", baseID)); !errors.Is(err, subjects.ErrNotFound) {
		t.Fatalf("missing subject lookup error = %v", err)
	}
}

func TestSubjectServicePageUsesTupleAfterDeletedAnchor(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := subjects.NewService(store, subjects.Config{})
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	ownerID, otherID := "page-owner-"+suffix, "page-other-"+suffix
	ids := []string{"page-a-" + suffix, "page-b-" + suffix, "page-c-" + suffix, "page-other-subject-" + suffix}
	for _, id := range []string{ownerID, otherID} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, primary_email) VALUES ($1, $2)`, id, id+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for _, id := range ids {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, id)
		}
		for _, id := range []string{ownerID, otherID} {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, id)
		}
	})
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index, id := range ids {
		owner := ownerID
		if index == len(ids)-1 {
			owner = otherID
		}
		if _, err := pool.Exec(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public, created_at, updated_at) VALUES ($1, $2, $3, 'UTC', true, $4, $4)`, id, owner, id, now); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.ListSubjectsPage(ctx, ownerID, nil, 2)
	if err != nil || len(first.Subjects) != 2 || first.Subjects[0].ID != ids[0] || first.Subjects[1].ID != ids[1] || !first.HasNextPage {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	anchor := subjects.SubjectCursor{CreatedAt: first.Subjects[1].CreatedAt, ID: first.Subjects[1].ID}
	if _, err := pool.Exec(ctx, `DELETE FROM subjects WHERE id = $1`, anchor.ID); err != nil {
		t.Fatal(err)
	}
	second, err := service.ListSubjectsPage(ctx, ownerID, &anchor, 2)
	if err != nil || len(second.Subjects) != 1 || second.Subjects[0].ID != ids[2] || second.HasNextPage {
		t.Fatalf("second page after deleted anchor = %+v, %v", second, err)
	}
}
