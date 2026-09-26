package backup

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleObservationNeverChangesVerifiedRecovery(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	recovery, err := NewVerificationState(filepath.Join(t.TempDir(), "verified.json"), clock)
	if err != nil {
		t.Fatal(err)
	}
	schedule := NewScheduleState(clock)
	goodFile := checkedResult("chain_1", "collection_1", now.Add(-time.Minute), now)
	if err := recovery.Check(context.Background(), func(context.Context) (CheckResult, error) { return goodFile, nil }); err != nil {
		t.Fatal(err)
	}
	initial := ScheduleCheckResult{CheckedAt: now, Healthy: true, Initializing: true}
	if err := schedule.Check(context.Background(), func(context.Context) (ScheduleCheckResult, error) { return initial, nil }); err != nil {
		t.Fatal(err)
	}
	if !schedule.Healthy() || !schedule.Initializing() || !recovery.Healthy() || !recovery.LastVerified().Equal(goodFile.RecoveryTimestamp) {
		t.Fatal("initial paused schedule incorrectly failed or gated file-checked recovery")
	}
	now = now.Add(time.Minute)
	if err := schedule.Check(context.Background(), func(context.Context) (ScheduleCheckResult, error) {
		return ScheduleCheckResult{}, errors.New("private SQL URI")
	}); err == nil || strings.Contains(err.Error(), "URI") {
		t.Fatalf("unsafe schedule failure: %v", err)
	}
	if schedule.Healthy() || schedule.Initializing() || !recovery.Healthy() || !recovery.LastVerified().Equal(goodFile.RecoveryTimestamp) {
		t.Fatal("failed schedule check changed recovery evidence")
	}
	goodSchedule := ScheduleCheckResult{CheckedAt: now, Healthy: true}
	if err := schedule.Check(context.Background(), func(context.Context) (ScheduleCheckResult, error) { return goodSchedule, nil }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := recovery.Check(context.Background(), func(context.Context) (CheckResult, error) { return CheckResult{}, errors.New("missing backup files") }); err == nil {
		t.Fatal("missing files accepted")
	}
	if !schedule.Healthy() || recovery.Healthy() || !recovery.LastVerified().Equal(goodFile.RecoveryTimestamp) {
		t.Fatal("healthy schedule falsely advanced failed file check")
	}
}

func TestScheduleMetricsStaleAndUnknown(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	recovery, err := NewVerificationState(filepath.Join(t.TempDir(), "verified.json"), clock)
	if err != nil {
		t.Fatal(err)
	}
	schedule := NewScheduleState(clock)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewBackupMetricsHandler(recovery, schedule, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	scrape := func() string {
		r := httptest.NewRequest("GET", "/metrics", nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("metrics status %d", w.Code)
		}
		return w.Body.String()
	}
	if body := scrape(); !strings.Contains(body, "jandibat_backup_schedule_policy_healthy 0\n") || !strings.Contains(body, "jandibat_backup_schedule_initializing 0\n") || !strings.Contains(body, "jandibat_backup_schedule_last_check_timestamp_seconds 0\n") {
		t.Fatalf("missing schedule was not unknown: %q", body)
	}
	if err := schedule.Check(context.Background(), func(context.Context) (ScheduleCheckResult, error) {
		return ScheduleCheckResult{CheckedAt: now, Healthy: true, Initializing: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if body := scrape(); !strings.Contains(body, "jandibat_backup_schedule_policy_healthy 1\n") || !strings.Contains(body, "jandibat_backup_schedule_initializing 1\n") {
		t.Fatalf("initializing schedule counted as failed: %q", body)
	}
	now = now.Add(15*time.Minute + time.Second)
	if body := scrape(); !strings.Contains(body, "jandibat_backup_schedule_policy_healthy 0\n") || !strings.Contains(body, "jandibat_backup_schedule_initializing 0\n") || !strings.Contains(body, "jandibat_backup_schedule_last_check_timestamp_seconds 1790337600\n") {
		t.Fatalf("stale schedule still healthy or lost last check: %q", body)
	}
}

func TestScheduleRejectsOlderSuccessfulObservationWithoutRewindingMetric(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	recovery, err := NewVerificationState(filepath.Join(t.TempDir(), "verified.json"), clock)
	if err != nil {
		t.Fatal(err)
	}
	fileCheck := checkedResult("chain_1", "collection_1", now.Add(-time.Minute), now)
	if err := recovery.Check(context.Background(), func(context.Context) (CheckResult, error) { return fileCheck, nil }); err != nil {
		t.Fatal(err)
	}
	schedule := NewScheduleState(clock)
	first := ScheduleCheckResult{CheckedAt: now, Healthy: true, Initializing: false}
	if err := schedule.Check(context.Background(), func(context.Context) (ScheduleCheckResult, error) { return first, nil }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	older := ScheduleCheckResult{CheckedAt: first.CheckedAt.Add(-time.Minute), Healthy: true, Initializing: true}
	if err := schedule.Check(context.Background(), func(context.Context) (ScheduleCheckResult, error) { return older, nil }); err == nil {
		t.Fatal("older successful schedule result was accepted")
	}
	if got := schedule.snapshot(); !got.CheckedAt.Equal(first.CheckedAt) || got.Healthy || got.Initializing {
		t.Fatalf("older result rewound or kept schedule healthy: %+v", got)
	}
	if !recovery.Healthy() || !recovery.LastVerified().Equal(fileCheck.RecoveryTimestamp) {
		t.Fatal("schedule regression changed file-checked recovery")
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewBackupMetricsHandler(recovery, schedule, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "jandibat_backup_schedule_last_check_timestamp_seconds 1790337600\n") || !strings.Contains(w.Body.String(), "jandibat_backup_schedule_policy_healthy 0\n") {
		t.Fatalf("schedule regression metric was not fail-closed and monotonic: %d %q", w.Code, w.Body.String())
	}
}
