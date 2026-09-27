package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/backup"
)

const fixtureToken = "0123456789abcdef0123456789abcdef"

func TestParseChainResultRejectsNonCanonicalEvidence(t *testing.T) {
	good := `{"schemaVersion":2,"chainId":"2026.09.25-110000.00","collectionId":"jandibat_backup_v1","checkedAt":"2026-09-25T11:01:00Z","recoveryTimestamp":"2026-09-25T11:00:00Z","fileChecked":true,"passed":true,"backupPath":"2026/09/25-110000.00"}`
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	for name, raw := range map[string]string{
		"v1":                   strings.Replace(good, `"schemaVersion":2,`, `"schemaVersion":1,`, 1),
		"trailing object":      good + "\n{}",
		"unknown field":        good[:len(good)-1] + `,"objectKey":"private"}`,
		"missing version":      strings.Replace(good, `"schemaVersion":2,`, "", 1),
		"missing backup path":  strings.Replace(good, `,"backupPath":"2026/09/25-110000.00"`, "", 1),
		"duplicate chain":      strings.Replace(good, `"chainId":"2026.09.25-110000.00",`, `"chainId":"2026.09.25-110000.00","chainId":"2026.09.25-110000.00",`, 1),
		"duplicate path":       strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"2026/09/25-110000.00","backupPath":"2026/09/25-110000.00"`, 1),
		"version type":         strings.Replace(good, `"schemaVersion":2`, `"schemaVersion":"2"`, 1),
		"null chain":           strings.Replace(good, `"chainId":"2026.09.25-110000.00"`, `"chainId":null`, 1),
		"path type":            strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":42`, 1),
		"null path":            strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":null`, 1),
		"files false":          strings.Replace(good, `"fileChecked":true`, `"fileChecked":false`, 1),
		"files null":           strings.Replace(good, `"fileChecked":true`, `"fileChecked":null`, 1),
		"passed false":         strings.Replace(good, `"passed":true`, `"passed":false`, 1),
		"wrong collection":     strings.Replace(good, `"collectionId":"jandibat_backup_v1"`, `"collectionId":"other_collection"`, 1),
		"mismatched chain":     strings.Replace(good, `"chainId":"2026.09.25-110000.00"`, `"chainId":"2026.09.25-110001.00"`, 1),
		"leading slash":        strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"/2026/09/25-110000.00"`, 1),
		"path traversal":       strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"2026/09/../25-110000.00"`, 1),
		"path injection":       strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"2026/09/25-110000.00' OR true --"`, 1),
		"provider path":        strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"s3://secret@host/key"`, 1),
		"noncanonical path":    strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"2026/9/25-110000.00"`, 1),
		"invalid date":         strings.Replace(good, `"backupPath":"2026/09/25-110000.00"`, `"backupPath":"2026/02/30-110000.00"`, 1),
		"invalid check time":   strings.Replace(good, `"checkedAt":"2026-09-25T11:01:00Z"`, `"checkedAt":"2026-09-25T25:01:00Z"`, 1),
		"non-UTC check time":   strings.Replace(good, `"checkedAt":"2026-09-25T11:01:00Z"`, `"checkedAt":"2026-09-25T11:01:00+00:00"`, 1),
		"future check time":    strings.Replace(good, `"checkedAt":"2026-09-25T11:01:00Z"`, `"checkedAt":"`+future+`"`, 1),
		"invalid recovery":     strings.Replace(good, `"recoveryTimestamp":"2026-09-25T11:00:00Z"`, `"recoveryTimestamp":"not-a-date"`, 1),
		"recovery after check": strings.Replace(good, `"recoveryTimestamp":"2026-09-25T11:00:00Z"`, `"recoveryTimestamp":"2026-09-25T11:02:00Z"`, 1),
		"oversized":            strings.Repeat("x", 4097),
	} {
		if _, err := parseChainResult([]byte(raw)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: unsafe chain result accepted or leaked: %v", name, err)
		}
	}
	got, err := parseChainResult([]byte(good))
	if err != nil || got.ChainID != "2026.09.25-110000.00" || got.BackupPath != "2026/09/25-110000.00" || !got.FileChecked || !got.Passed ||
		!got.CheckedAt.Equal(time.Date(2026, 9, 25, 11, 1, 0, 0, time.UTC)) ||
		!got.RecoveryTimestamp.Equal(time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("canonical chain result rejected: %+v, %v", got, err)
	}
}

func TestParseScheduleResultUsesExactSafeFourFieldContract(t *testing.T) {
	good := `{"schemaVersion":1,"checkedAt":"2026-09-25T12:00:00Z","healthy":true,"initializing":true}`
	for _, raw := range []string{
		strings.Replace(good, `"schemaVersion":1,`, "", 1),
		strings.Replace(good, `"healthy":true`, `"healthy":false`, 1),
		strings.Replace(good, `"healthy":true`, `"healthy":null`, 1),
		strings.Replace(good, `"initializing":true`, `"initializing":null`, 1),
		strings.Replace(good, `"healthy":true`, `"healthy":true,"healthy":false`, 1),
		good[:len(good)-1] + `,"fullScheduleId":"private"}`,
		good + "\n{}", strings.Repeat("x", 4097),
	} {
		if _, err := parseScheduleResult([]byte(raw)); err == nil {
			t.Fatal("unsafe schedule observation accepted")
		}
	}
	got, err := parseScheduleResult([]byte(good))
	if err != nil || !got.Healthy || !got.Initializing || !got.CheckedAt.Equal(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected initial paused policy observation: %+v %v", got, err)
	}
}

func TestRunVerifierKeepsFileRecoveryWhenScheduleQueryFails(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureToken), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BACKUP_VERIFIED_RECORD_FILE": filepath.Join(dir, "verified.json"), "BACKUP_METRICS_TOKEN_FILE": tokenFile, "BACKUP_METRICS_LISTEN_ADDR": "127.0.0.1:0"}
	getenv := func(k string) string { return env[k] }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	checkedAt := time.Now().UTC()
	go func() {
		done <- runMode(ctx, []string{"run-verifier"}, getenv,
			func(context.Context) (backup.CheckResult, error) {
				return backup.CheckResult{ChainID: "chain_1", CollectionID: "jandibat_backup_v1", RecoveryTimestamp: checkedAt.Add(-time.Minute), CheckedAt: checkedAt, FileChecked: true, Passed: true}, nil
			},
			func(context.Context) (backup.ScheduleCheckResult, error) {
				return backup.ScheduleCheckResult{}, errors.New("private SQL URI")
			},
			listener, &bytes.Buffer{})
	}()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", "http://"+listener.Addr().String()+"/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+fixtureToken)
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), "jandibat_backup_check_healthy 1\n") {
				if !strings.Contains(string(body), "jandibat_backup_schedule_policy_healthy 0\n") {
					t.Fatalf("schedule query failure masked: %q", body)
				}
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("verifier did not stop")
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("file-checked recovery did not become healthy independently")
}

func TestCheckOncePersistsOnlyVerifiedEvidence(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "verified.json")
	env := map[string]string{"BACKUP_VERIFIED_RECORD_FILE": record}
	getenv := func(k string) string { return env[k] }
	var out bytes.Buffer
	now := time.Now().UTC().Add(-time.Minute)
	checker := func(context.Context) (backup.CheckResult, error) {
		return backup.CheckResult{ChainID: "chain_1", CollectionID: "jandibat_backup_v1", CheckedAt: time.Now().UTC(), RecoveryTimestamp: now, FileChecked: true, Passed: true}, nil
	}
	if err := runMode(context.Background(), []string{"check-once"}, getenv, checker, nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil || !bytes.Contains(data, []byte(`"chainId":"chain_1"`)) ||
		bytes.Contains(data, []byte(`"linkedScheduleIds"`)) ||
		!bytes.Contains(data, []byte(`"outcome":"pass"`)) || strings.Contains(out.String(), "chain_1") {
		t.Fatalf("durable check-once evidence absent or exposed: %v", err)
	}
	if err := runMode(context.Background(), []string{"check-once"}, getenv, func(context.Context) (backup.CheckResult, error) {
		return backup.CheckResult{}, errors.New("s3://secret:user@host/object")
	}, nil, nil, &out); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe failure: %v", err)
	}
}

func TestRunVerifierServesMetricsDuringFailedCheck(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureToken), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"BACKUP_VERIFIED_RECORD_FILE": filepath.Join(dir, "verified.json"),
		"BACKUP_METRICS_TOKEN_FILE":   tokenFile,
		"BACKUP_METRICS_LISTEN_ADDR":  "127.0.0.1:0",
	}
	getenv := func(k string) string { return env[k] }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runMode(ctx, []string{"run-verifier"}, getenv, func(context.Context) (backup.CheckResult, error) {
			close(started)
			<-release
			return backup.CheckResult{}, errors.New("secret credential")
		}, func(context.Context) (backup.ScheduleCheckResult, error) {
			return backup.ScheduleCheckResult{}, errors.New("schedule unavailable")
		}, listener, &bytes.Buffer{})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("checker did not start")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+fixtureToken)
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("metrics unavailable during check: %d", resp.StatusCode)
	}
	close(release)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("verifier did not stop")
	}
}

func TestRunVerifierRefusesUnsafeConfiguration(t *testing.T) {
	for _, addr := range []string{"", ":8080", "0.0.0.0:8080", "example.org:8080"} {
		env := map[string]string{"BACKUP_METRICS_LISTEN_ADDR": addr, "BACKUP_METRICS_TOKEN_FILE": "/missing"}
		if err := runMode(context.Background(), []string{"run-verifier"}, func(k string) string { return env[k] }, nil, nil, nil, &bytes.Buffer{}); err == nil {
			t.Fatalf("unsafe listen address accepted")
		}
	}
}

func TestScriptCheckerBoundsOutputAndRedactsFailure(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		if k == "BACKUP_VERIFIER_DATABASE_URL" {
			return "postgresql://secret@fixture/db"
		}
		return ""
	}
	for _, tc := range []struct{ name, script string }{
		{"stderr credential", "echo 'postgresql://secret@fixture/db' >&2; exit 1\n"},
		{"oversized stdout", "head -c 5000 /dev/zero\n"},
		{"malformed evidence", "printf '%s\\n' '{\"schemaVersion\":1,\"collectionId\":\"secret\"}'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.WriteFile(path, []byte(tc.script), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := scriptChecker(path, filepath.Join(dir, "verified.json"), getenv)(context.Background())
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "postgresql://") {
				t.Fatalf("unsafe script result: %v", err)
			}
		})
	}
}

func TestScriptCheckerStopsOnContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slow.sh")
	if err := os.WriteFile(path, []byte("sleep 30\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := scriptChecker(path, filepath.Join(t.TempDir(), "verified.json"), func(string) string { return "" })(ctx)
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("checker ignored cancellation: %v", err)
	}
}

func TestScriptScheduleCheckerKeepsSQLFailurePrivate(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "schedule.sh")
	if err := os.WriteFile(script, []byte("echo 's3://private-user:private-key@storage' >&2; exit 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		if key == "BACKUP_VERIFIER_DATABASE_URL" {
			return "postgresql://private@fixture/db"
		}
		return ""
	}
	_, err := scriptScheduleChecker(script, filepath.Join(dir, "verified.json"), getenv)(context.Background())
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "s3://") {
		t.Fatalf("schedule observer leaked SQL failure: %v", err)
	}
	if err := os.WriteFile(script, []byte("printf '%s\\n' '{\"schemaVersion\":1,\"checkedAt\":\"2026-09-25T12:00:00Z\",\"healthy\":false,\"initializing\":false}'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := scriptScheduleChecker(script, filepath.Join(dir, "verified.json"), getenv)(context.Background())
	if err != nil || result.Healthy || result.Initializing {
		t.Fatalf("safe drift observation rejected: %+v %v", result, err)
	}
}

func TestScriptCheckerUsesRecordDirectoryForPrivateTemporaryFiles(t *testing.T) {
	recordDir := t.TempDir()
	forbiddenTemp := filepath.Join(t.TempDir(), "unavailable")
	t.Setenv("TMPDIR", forbiddenTemp)
	script := filepath.Join(t.TempDir(), "needs-private-tmp.sh")
	body := `set -eu
scratch=$(mktemp -d "$TMPDIR/jandibat-chain-capture.XXXXXX")
rmdir "$scratch"
printf '%s\n' '{"schemaVersion":2,"chainId":"2026.09.25-110000.00","collectionId":"jandibat_backup_v1","checkedAt":"2026-09-25T11:01:00Z","recoveryTimestamp":"2026-09-25T11:00:00Z","fileChecked":true,"passed":true,"backupPath":"2026/09/25-110000.00"}'
`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		switch key {
		case "BACKUP_VERIFIER_DATABASE_URL":
			return "postgresql://secret@fixture/db"
		case "TMPDIR":
			return forbiddenTemp
		default:
			return ""
		}
	}
	got, err := scriptChecker(script, filepath.Join(recordDir, "verified.json"), getenv)(context.Background())
	if err != nil || got.ChainID != "2026.09.25-110000.00" || got.BackupPath != "2026/09/25-110000.00" {
		t.Fatalf("private temporary directory not available to checker: %v", err)
	}
}
