package main

import (
	"bytes"
	"context"
	"errors"
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
	good := `{"schemaVersion":1,"chainId":"chain_1","collectionId":"jandibat_backup_v1","fullScheduleId":"9223372036854775807","incrementalScheduleId":"9223372036854775806","checkedAt":"2026-09-25T11:01:00Z","recoveryTimestamp":"2026-09-25T11:00:00Z","fileChecked":true,"passed":true}`
	for _, raw := range []string{
		good + "\n{}", good[:len(good)-1] + `,"objectKey":"private"}`,
		strings.Replace(good, `"schemaVersion":1,`, "", 1),
		strings.Replace(good, `"schemaVersion":1,`, `"schemaVersion":2,`, 1),
		strings.Replace(good, `"chainId":"chain_1",`, `"chainId":"chain_1","chainId":"chain_2",`, 1),
		strings.Replace(good, `"fileChecked":true`, `"fileChecked":false`, 1),
		strings.Replace(good, `"passed":true`, `"passed":false`, 1),
		strings.Replace(good, `"fullScheduleId":"9223372036854775807",`, "", 1),
		strings.Replace(good, `"checkedAt":"2026-09-25T11:01:00Z",`, "", 1),
		strings.Replace(good, `"incrementalScheduleId":"9223372036854775806"`, `"incrementalScheduleId":"0"`, 1),
		strings.Replace(good, `"chainId":"chain_1"`, `"chainId":null`, 1),
		strings.Replace(good, `"chainId":"chain_1"`, `"chainId":"s3://secret@host/key"`, 1),
		strings.Repeat("x", 4097),
	} {
		if _, err := parseChainResult([]byte(raw)); err == nil {
			t.Fatalf("unsafe chain result accepted")
		}
	}
	got, err := parseChainResult([]byte(good))
	if err != nil || got.ChainID != "chain_1" || !got.FileChecked || !got.Passed ||
		got.FullScheduleID != "9223372036854775807" || got.IncrementalScheduleID != "9223372036854775806" ||
		!got.CheckedAt.Equal(time.Date(2026, 9, 25, 11, 1, 0, 0, time.UTC)) ||
		!got.RecoveryTimestamp.Equal(time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("canonical chain result rejected: %+v, %v", got, err)
	}
}

func TestCheckOncePersistsOnlyVerifiedEvidence(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "verified.json")
	env := map[string]string{"BACKUP_VERIFIED_RECORD_FILE": record}
	getenv := func(k string) string { return env[k] }
	var out bytes.Buffer
	now := time.Now().UTC().Add(-time.Minute)
	checker := func(context.Context) (backup.CheckResult, error) {
		return backup.CheckResult{ChainID: "chain_1", CollectionID: "jandibat_backup_v1", FullScheduleID: "9223372036854775807", IncrementalScheduleID: "9223372036854775806", CheckedAt: time.Now().UTC(), RecoveryTimestamp: now, FileChecked: true, Passed: true}, nil
	}
	if err := runMode(context.Background(), []string{"check-once"}, getenv, checker, nil, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil || !bytes.Contains(data, []byte(`"chainId":"chain_1"`)) ||
		!bytes.Contains(data, []byte(`"linkedScheduleIds":["9223372036854775807","9223372036854775806"]`)) ||
		!bytes.Contains(data, []byte(`"outcome":"pass"`)) || strings.Contains(out.String(), "chain_1") {
		t.Fatalf("durable check-once evidence absent or exposed: %v", err)
	}
	if err := runMode(context.Background(), []string{"check-once"}, getenv, func(context.Context) (backup.CheckResult, error) {
		return backup.CheckResult{}, errors.New("s3://secret:user@host/object")
	}, nil, &out); err == nil || strings.Contains(err.Error(), "secret") {
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
		if err := runMode(context.Background(), []string{"run-verifier"}, func(k string) string { return env[k] }, nil, nil, &bytes.Buffer{}); err == nil {
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

func TestScriptCheckerUsesRecordDirectoryForPrivateTemporaryFiles(t *testing.T) {
	recordDir := t.TempDir()
	forbiddenTemp := filepath.Join(t.TempDir(), "unavailable")
	t.Setenv("TMPDIR", forbiddenTemp)
	script := filepath.Join(t.TempDir(), "needs-private-tmp.sh")
	body := `set -eu
scratch=$(mktemp -d "$TMPDIR/jandibat-chain-capture.XXXXXX")
rmdir "$scratch"
printf '%s\n' '{"schemaVersion":1,"chainId":"chain_1","collectionId":"jandibat_backup_v1","fullScheduleId":"9223372036854775807","incrementalScheduleId":"9223372036854775806","checkedAt":"2026-09-25T11:01:00Z","recoveryTimestamp":"2026-09-25T11:00:00Z","fileChecked":true,"passed":true}'
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
	if err != nil || got.ChainID != "chain_1" {
		t.Fatalf("private temporary directory not available to checker: %v", err)
	}
}
