package cockroach

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestNewRejectsNilDatabase(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) || store != nil {
		t.Fatalf("New(nil) = (%v, %v), want (nil, ErrNilDB)", store, err)
	}
}

func TestGetSubjectScansNullableProfileThroughDatabaseSQL(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "owner", "handle", "display", "timezone", "public", "created", "updated"},
		Rows: [][]driver.Value{{
			"subject-1", "user-1", "alice", nil, "Asia/Seoul", true, now, now,
		}},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	subject, err := store.GetSubject(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetSubject() error = %v", err)
	}
	if subject.ID != "subject-1" || subject.OwnerUserID != "user-1" || subject.DisplayName != nil {
		t.Fatalf("GetSubject() = %#v", subject)
	}
	if query := script.Calls()[0].Query; !strings.Contains(query, "id = $1 OR handle = $1") {
		t.Fatalf("identifier lookup query = %s", query)
	}
}

func TestSaveUserUpdatesOnlyAnExistingActiveUserAndRetainsSettings(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	verified := now
	user := subjects.User{
		ID: "user-1", PrimaryEmail: "person@example.com", EmailVerifiedAt: &verified,
		Status: subjects.UserStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	settings := subjects.UserSettings{Locale: "ko-KR", Timezone: "Asia/Seoul", Theme: subjects.ThemeDark, UpdatedAt: now}

	if err := store.SaveUser(context.Background(), user, settings); err != nil {
		t.Fatalf("SaveUser() error = %v", err)
	}
	calls := script.Calls()
	if len(calls) != 4 || !strings.Contains(calls[2].Query, "ON CONFLICT (user_id) DO NOTHING") {
		t.Fatalf("provisioning calls = %#v", calls)
	}
	if strings.Contains(calls[1].Query, "INSERT INTO users") || !strings.Contains(calls[1].Query, "WHERE id = $1 AND status = 'active'") {
		t.Fatalf("user provisioning can recreate or reactivate lifecycle row: %s", calls[1].Query)
	}
}

func TestSaveUserRejectsMissingOrNonActiveLifecycleRow(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 0},
		fakedb.Step{Operation: fakedb.Rollback},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	user := subjects.User{ID: "deleted-user", PrimaryEmail: "person@example.com", Status: subjects.UserStatusActive, CreatedAt: now, UpdatedAt: now}

	if err := store.SaveUser(context.Background(), user, subjects.UserSettings{Locale: "en-US", Timezone: "UTC", Theme: subjects.ThemeSystem, UpdatedAt: now}); !errors.Is(err, subjects.ErrForbidden) {
		t.Fatalf("SaveUser() error = %v, want ErrForbidden", err)
	}
	calls := script.Calls()
	if len(calls) != 3 || strings.Contains(calls[1].Query, "INSERT INTO users") || !strings.Contains(calls[1].Query, "status = 'active'") {
		t.Fatalf("inactive lifecycle calls = %#v", calls)
	}
}

func TestListSubjectsUsesStableDescendingCursor(t *testing.T) {
	cursorTime := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"id", "owner", "handle", "display", "timezone", "public", "created", "updated"},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	items, err := store.ListSubjects(context.Background(), "user-1", &subjects.SubjectCursor{CreatedAt: cursorTime, ID: "subject-2"}, 26)
	if err != nil || len(items) != 0 {
		t.Fatalf("ListSubjects() = (%#v, %v)", items, err)
	}
	call := script.Calls()[0]
	for _, fragment := range []string{
		"created_at < $2 OR (created_at = $2 AND id > $3)",
		"ORDER BY created_at DESC, id LIMIT $4",
	} {
		if !strings.Contains(call.Query, fragment) {
			t.Errorf("list query missing %q: %s", fragment, call.Query)
		}
	}
	if len(call.Args) != 4 || call.Args[3].Value != 26 {
		t.Fatalf("list args = %#v", call.Args)
	}
}

func TestSaveSubjectSettingsUpdatesResourceAndPreferencesAtomically(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	settings := subjects.SubjectSettings{
		SubjectID: "subject-1", Timezone: "Asia/Seoul", IsPublic: false,
		DefaultTheme: subjects.ThemeDark, WeekStart: subjects.WeekStartMonday,
		SyncEnabled: true, SyncIntervalMinutes: 30, FailurePolicy: subjects.FailurePurge,
		UpdatedAt: now,
	}

	if err := store.SaveSubjectSettings(context.Background(), settings); err != nil {
		t.Fatalf("SaveSubjectSettings() error = %v", err)
	}
	calls := script.Calls()
	if len(calls) != 4 || calls[0].Operation != fakedb.Begin || calls[3].Operation != fakedb.Commit {
		t.Fatalf("transaction calls = %#v", calls)
	}
	if !strings.Contains(calls[1].Query, "UPDATE subject_settings") || !strings.Contains(calls[2].Query, "UPDATE subjects") {
		t.Fatalf("atomic update queries = %#v", calls)
	}
}

func TestGetSubjectSettingsJoinsDenormalizedVisibility(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(fakedb.Step{
		Operation: fakedb.Query,
		Columns:   []string{"subject", "timezone", "public", "theme", "week", "sync", "interval", "policy", "updated"},
		Rows: [][]driver.Value{{
			"subject-1", "Asia/Seoul", false, "github-dark", "monday", true, int64(30), "purge", now,
		}},
	})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)

	settings, err := store.GetSubjectSettings(context.Background(), "subject-1")
	if err != nil {
		t.Fatalf("GetSubjectSettings() error = %v", err)
	}
	if settings.Timezone != "Asia/Seoul" || settings.IsPublic || settings.DefaultTheme != subjects.ThemeGitHubDark || settings.SyncIntervalMinutes != 30 {
		t.Fatalf("GetSubjectSettings() = %#v", settings)
	}
	if query := script.Calls()[0].Query; !strings.Contains(query, "JOIN subjects AS s") {
		t.Fatalf("settings query = %s", query)
	}
}

func TestCreateSubjectRollsBackCrossNamespaceCollision(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"id", "owner", "handle", "display", "timezone", "public", "created", "updated"}},
		fakedb.Step{Operation: fakedb.Exec, Err: &pgconn.PgError{Code: "23505"}},
		fakedb.Step{Operation: fakedb.Rollback},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	subject := subjects.Subject{
		ID: "subject-1", OwnerUserID: "user-1", Handle: "alice", Timezone: "UTC", IsPublic: true,
		CreatedAt: now, UpdatedAt: now,
	}
	settings := subjects.SubjectSettings{
		SubjectID: subject.ID, Timezone: subject.Timezone, IsPublic: subject.IsPublic,
		DefaultTheme: subjects.ThemeSystem, WeekStart: subjects.WeekStartSunday,
		SyncEnabled: true, SyncIntervalMinutes: 60, FailurePolicy: subjects.FailureKeepStale, UpdatedAt: now,
	}

	err := store.CreateSubject(context.Background(), subject, settings)
	if !errors.Is(err, subjects.ErrConflict) {
		t.Fatalf("CreateSubject() error = %v, want ErrConflict", err)
	}
	if remaining := script.Remaining(); remaining != 0 {
		t.Fatalf("remaining scripted operations = %d", remaining)
	}
}

func TestDeleteSubjectQueuesOAuthRevocationsBeforeCascade(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Begin},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"id"}, Rows: [][]driver.Value{{"subject-1"}}},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"id"}, Rows: [][]driver.Value{{"018f0000-0000-7000-8000-000000000001"}, {"018f0000-0000-7000-8000-000000000002"}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 2},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
		fakedb.Step{Operation: fakedb.Commit},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	if err := store.DeleteSubject(context.Background(), "subject-1"); err != nil {
		t.Fatal(err)
	}
	calls := script.Calls()
	if len(calls) != 6 || !strings.Contains(calls[1].Query, "FROM subjects") || !strings.Contains(calls[1].Query, "FOR UPDATE") ||
		!strings.Contains(calls[2].Query, "FROM provider_connections") || !strings.Contains(calls[2].Query, "FOR UPDATE") ||
		!strings.Contains(calls[3].Query, "INSERT INTO provider_token_revocation_jobs") ||
		!strings.Contains(calls[3].Query, "access_token_ciphertext IS NOT NULL") || strings.Contains(calls[3].Query, "ON CONFLICT") ||
		!strings.Contains(calls[4].Query, "DELETE FROM subjects") {
		t.Fatalf("subject delete sequence = %#v", calls)
	}
}
