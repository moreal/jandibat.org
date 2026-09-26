package backup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestVerificationKeepsDurableRecoveryWatermarkAcrossFailedChecks(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "verified.json")
	state, err := NewVerificationState(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	verified := now.Add(-10 * time.Minute)
	good := CheckResult{ChainID: "chain_1", CollectionID: "collection_1", RecoveryTimestamp: verified, FileChecked: true}
	if err := state.Check(context.Background(), func(context.Context) (CheckResult, error) { return good, nil }); err != nil {
		t.Fatal(err)
	}
	if !state.Healthy() || !state.LastVerified().Equal(verified) {
		t.Fatalf("initial state: healthy=%v verified=%v", state.Healthy(), state.LastVerified())
	}

	for _, tc := range []struct {
		name   string
		result CheckResult
		err    error
	}{
		{"checker failure", CheckResult{}, errors.New("s3://secret:user@host/object")},
		{"missing files", CheckResult{ChainID: "chain_2", CollectionID: "collection_1", RecoveryTimestamp: now, FileChecked: false}, nil},
		{"older recovery", CheckResult{ChainID: "chain_1", CollectionID: "collection_1", RecoveryTimestamp: verified.Add(-time.Second), FileChecked: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now = now.Add(time.Minute)
			err := state.Check(context.Background(), func(context.Context) (CheckResult, error) { return tc.result, tc.err })
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "s3://") {
				t.Fatalf("unsafe error: %v", err)
			}
			if state.Healthy() || !state.LastVerified().Equal(verified) {
				t.Fatalf("failed check advanced recovery: healthy=%v verified=%v", state.Healthy(), state.LastVerified())
			}
			reloaded, err := NewVerificationState(path, clock)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Healthy() || !reloaded.LastVerified().Equal(verified) {
				t.Fatalf("durable failed check lost: healthy=%v verified=%v", reloaded.Healthy(), reloaded.LastVerified())
			}
		})
	}
}

func TestVerificationRestartAndStaleness(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	dir := t.TempDir()
	path := filepath.Join(dir, "verified.json")
	state, err := NewVerificationState(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if state.Healthy() || !state.LastVerified().IsZero() {
		t.Fatal("missing record was treated as verified")
	}
	result := CheckResult{ChainID: "chain_1", CollectionID: "collection_1", RecoveryTimestamp: now.Add(-time.Minute), FileChecked: true}
	if err := state.Check(context.Background(), func(context.Context) (CheckResult, error) { return result, nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions: %v, %v", info, err)
	}
	preserved, err := NewVerificationState(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if !preserved.Healthy() || !preserved.LastVerified().Equal(result.RecoveryTimestamp) {
		t.Fatal("preserved emptyDir did not retain verification")
	}
	now = now.Add(15*time.Minute + time.Second)
	if preserved.Healthy() || !preserved.LastVerified().Equal(result.RecoveryTimestamp) {
		t.Fatal("stale check remained healthy or lost watermark")
	}
	lost, err := NewVerificationState(filepath.Join(t.TempDir(), "verified.json"), clock)
	if err != nil {
		t.Fatal(err)
	}
	if lost.Healthy() || !lost.LastVerified().IsZero() {
		t.Fatal("replacement Pod inherited missing emptyDir evidence")
	}
}

func TestMetricsRequiresExactAuthenticatedGETAndAlwaysAnswers(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	state, err := NewVerificationState(filepath.Join(t.TempDir(), "verified.json"), clock)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewVerificationMetricsHandler(state, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, auth string
		want               int
	}{
		{"POST", "/metrics", "Bearer " + testToken, 405},
		{"GET", "/", "Bearer " + testToken, 404},
		{"GET", "/metrics/", "Bearer " + testToken, 404},
		{"GET", "/met%72ics", "Bearer " + testToken, 404},
		{"GET", "/metrics?", "Bearer " + testToken, 404},
		{"GET", "/metrics?x=1", "Bearer " + testToken, 404},
		{"GET", "/metrics", "", 403},
		{"GET", "/metrics", "Bearer wrong", 403},
		{"GET", "/metrics", "Bearer " + testToken, 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.auth != "" {
			r.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want || strings.Contains(w.Body.String(), testToken) {
			t.Fatalf("%s %s: code=%d body=%q", tc.method, tc.path, w.Code, w.Body.String())
		}
		if tc.want == 200 && (!strings.Contains(w.Body.String(), "jandibat_backup_check_healthy 0\n") || !strings.Contains(w.Body.String(), "jandibat_backup_verified_recovery_timestamp_seconds 0\n")) {
			t.Fatalf("missing unknown metrics: %q", w.Body.String())
		}
	}
	result := CheckResult{ChainID: "chain_1", CollectionID: "collection_1", RecoveryTimestamp: now.Add(-time.Minute), FileChecked: true}
	if err := state.Check(context.Background(), func(context.Context) (CheckResult, error) { return result, nil }); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "jandibat_backup_check_healthy 1\n") || !strings.Contains(w.Body.String(), "jandibat_backup_verified_recovery_timestamp_seconds 1790337540\n") {
		t.Fatalf("healthy metrics: %d %q", w.Code, w.Body.String())
	}
	now = now.Add(15*time.Minute + time.Second)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "jandibat_backup_check_healthy 0\n") || !strings.Contains(w.Body.String(), "jandibat_backup_verified_recovery_timestamp_seconds 1790337540\n") {
		t.Fatalf("stale scrape: %d %q", w.Code, w.Body.String())
	}
}

func TestVerificationRejectsUnsafeEvidenceAndRecord(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "verified.json")
	state, err := NewVerificationState(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []CheckResult{
		{ChainID: "s3://user:secret@host/key", CollectionID: "safe", RecoveryTimestamp: now, FileChecked: true},
		{ChainID: "safe", CollectionID: "collection/path", RecoveryTimestamp: now, FileChecked: true},
		{ChainID: "safe", CollectionID: "safe", RecoveryTimestamp: now.Add(time.Second), FileChecked: true},
		{ChainID: "safe", CollectionID: "safe", RecoveryTimestamp: now, FileChecked: false},
	} {
		if err := state.Check(context.Background(), func(context.Context) (CheckResult, error) { return result, nil }); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("accepted unsafe evidence: %v", err)
		}
		if state.Healthy() || !state.LastVerified().IsZero() {
			t.Fatal("unsafe evidence changed recovery state")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secret") || strings.Contains(string(data), "s3://") {
			t.Fatalf("unsafe record: %s", data)
		}
		if !json.Valid(data) {
			t.Fatalf("torn record: %s", data)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewVerificationState(path, func() time.Time { return now }); err == nil {
		t.Fatal("oversized record accepted")
	}
}

func TestMetricsScrapeDoesNotWaitForChecker(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	state, err := NewVerificationState(filepath.Join(t.TempDir(), "verified.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewVerificationMetricsHandler(state, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(done)
		state.RunChecks(ctx, func(context.Context) (CheckResult, error) {
			close(started)
			<-release
			return CheckResult{}, errors.New("private failure")
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("checker did not start")
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	scraped := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, r); scraped <- w }()
	select {
	case w := <-scraped:
		if w.Code != 200 || !strings.Contains(w.Body.String(), "jandibat_backup_check_healthy 0\n") {
			t.Fatalf("blocked-check scrape: %d %q", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("scrape blocked on checker")
	}
	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("checker loop did not stop")
	}
}

func TestMetricsScrapeDoesNotWaitForRecordPersistence(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "verified.json")
	state, err := NewVerificationState(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	first := CheckResult{ChainID: "chain_1", CollectionID: "collection_1", RecoveryTimestamp: now.Add(-time.Minute), FileChecked: true}
	if err := state.Check(context.Background(), func(context.Context) (CheckResult, error) { return first, nil }); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewVerificationMetricsHandler(state, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	persisted, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	state.persist = func(path string, record verificationRecord) error {
		// Exercise the real atomic file replace, then hold its caller before
		// in-memory publication. A scrape must still use the old snapshot.
		err := replaceRecord(path, record)
		close(persisted)
		<-release
		return err
	}
	now = now.Add(time.Minute)
	second := CheckResult{ChainID: "chain_1", CollectionID: "collection_1", RecoveryTimestamp: now, FileChecked: true}
	done := make(chan error, 1)
	go func() {
		done <- state.Check(context.Background(), func(context.Context) (CheckResult, error) { return second, nil })
	}()
	select {
	case <-persisted:
	case <-time.After(time.Second):
		t.Fatal("record replacement did not finish")
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	scraped := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, r); scraped <- w }()
	select {
	case w := <-scraped:
		if w.Code != 200 || !strings.Contains(w.Body.String(), "jandibat_backup_verified_recovery_timestamp_seconds 1790337540\n") {
			t.Fatalf("blocked-persistence scrape: %d %q", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("scrape blocked on record persistence")
	}
	releaseOnce()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("check did not finish")
	}
	if !state.LastVerified().Equal(second.RecoveryTimestamp) {
		t.Fatal("new recovery was not published")
	}
	reloaded, err := NewVerificationState(path, clock)
	if err != nil || !reloaded.LastVerified().Equal(second.RecoveryTimestamp) {
		t.Fatalf("real record not replaced: %v, %v", reloaded, err)
	}
}
