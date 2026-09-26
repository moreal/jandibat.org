package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/backup"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"go.uber.org/zap"
)

const (
	defaultRecordPath  = "/var/run/jandibat-backup/verified.json"
	chainScriptPath    = "/workspace/scripts/db-verify-backup-chain.sh"
	scheduleScriptPath = "/workspace/scripts/db-observe-backup-schedule.sh"
	maxChainResult     = 4096
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}

func run() error {
	resource := observability.ResourceFromEnvironment("")
	logger, syncLogger, err := observability.NewLogger(observability.Config{
		Service: "backup-tools", Resource: resource, Development: resource.Environment != "production",
	})
	if err != nil {
		return errors.New("backup-tools logger unavailable")
	}
	defer func() { _ = syncLogger() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMode(ctx, os.Args[1:], os.Getenv, nil, nil, nil, os.Stdout); err != nil {
		observability.Log(logger, "backup_tools.stopped", zap.String("reason", "operation failed"))
		return err
	}
	return nil
}

// runMode permits only the two verification modes. The checker and listener
// arguments are test seams; production always uses the fixed packaged script
// and opens its own internal listener.
func runMode(ctx context.Context, args []string, getenv func(string) string, checker backup.Checker, scheduleChecker backup.ScheduleChecker, supplied net.Listener, out io.Writer) error {
	if len(args) != 1 || (args[0] != "check-once" && args[0] != "run-verifier") || getenv == nil {
		return errors.New("invalid backup-tools mode")
	}
	path := getenv("BACKUP_VERIFIED_RECORD_FILE")
	if path == "" {
		path = defaultRecordPath
	}
	state, err := backup.NewVerificationState(path, time.Now)
	if err != nil {
		return errors.New("verification state unavailable")
	}
	if checker == nil {
		if getenv("BACKUP_VERIFIER_DATABASE_URL") == "" {
			return errors.New("verification database configuration unavailable")
		}
		if _, err := os.Stat(chainScriptPath); err != nil {
			return errors.New("chain checker unavailable")
		}
		checker = scriptChecker(chainScriptPath, path, getenv)
	}
	if args[0] == "check-once" {
		if err := state.Check(ctx, checker); err != nil {
			return errors.New("chain verification failed")
		}
		if out != nil {
			_, _ = io.WriteString(out, "backup chain verified\n")
		}
		return nil
	}
	if scheduleChecker == nil {
		if _, err := os.Stat(scheduleScriptPath); err != nil {
			return errors.New("schedule observer unavailable")
		}
		scheduleChecker = scriptScheduleChecker(scheduleScriptPath, path, getenv)
	}
	schedule := backup.NewScheduleState(time.Now)
	address := getenv("BACKUP_METRICS_LISTEN_ADDR")
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid verifier listen address")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate()) {
		return errors.New("verifier listen address must be internal")
	}
	handler, err := backup.NewBackupMetricsHandler(state, schedule, getenv("BACKUP_METRICS_TOKEN_FILE"))
	if err != nil {
		return errors.New("verifier metrics configuration unavailable")
	}
	listener := supplied
	if listener == nil {
		listener, err = net.Listen("tcp", address)
		if err != nil {
			return errors.New("verifier listener unavailable")
		}
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	go state.RunChecks(ctx, checker)
	go schedule.RunChecks(ctx, scheduleChecker)
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("verifier metrics server failed")
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return errors.New("verifier shutdown failed")
		}
		return nil
	}
}

func scriptChecker(path, recordPath string, getenv func(string) string) backup.Checker {
	return func(ctx context.Context) (backup.CheckResult, error) {
		output, err := runSafeScript(ctx, path, recordPath, getenv)
		if err != nil {
			return backup.CheckResult{}, errors.New("chain check subprocess failed")
		}
		return parseChainResult(output)
	}
}

func scriptScheduleChecker(path, recordPath string, getenv func(string) string) backup.ScheduleChecker {
	return func(ctx context.Context) (backup.ScheduleCheckResult, error) {
		output, err := runSafeScript(ctx, path, recordPath, getenv)
		if err != nil {
			return backup.ScheduleCheckResult{}, errors.New("schedule observation subprocess failed")
		}
		return parseScheduleResult(output)
	}
}

func runSafeScript(ctx context.Context, path, recordPath string, getenv func(string) string) ([]byte, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "sh", path)
	// A shell child may retain output pipes after cancellation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"BACKUP_VERIFIER_DATABASE_URL=" + getenv("BACKUP_VERIFIER_DATABASE_URL"),
		"TMPDIR=" + filepath.Dir(recordPath),
	}
	if client := getenv("COCKROACH_SQL_BIN"); client != "" {
		cmd.Env = append(cmd.Env, "COCKROACH_SQL_BIN="+client)
	}
	var output boundedBuffer
	output.limit = maxChainResult
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("backup observation subprocess failed")
	}
	return output.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("chain check output exceeded limit")
	}
	return b.Buffer.Write(p)
}

func parseScheduleResult(data []byte) (backup.ScheduleCheckResult, error) {
	bad := backup.ScheduleCheckResult{}
	if len(data) == 0 || len(data) > maxChainResult {
		return bad, errors.New("invalid schedule observation")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return bad, errors.New("invalid schedule observation")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		keyToken, err := d.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return bad, errors.New("invalid schedule observation")
		}
		switch key {
		case "schemaVersion", "checkedAt", "healthy", "initializing":
		default:
			return bad, errors.New("invalid schedule observation")
		}
		if _, exists := fields[key]; exists {
			return bad, errors.New("invalid schedule observation")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return bad, errors.New("invalid schedule observation")
		}
		fields[key] = value
	}
	if len(fields) != 4 {
		return bad, errors.New("invalid schedule observation")
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') {
		return bad, errors.New("invalid schedule observation")
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return bad, errors.New("invalid schedule observation")
	}
	var version int
	var stamp string
	healthy, healthyOK := strictJSONBool(fields["healthy"])
	initializing, initializingOK := strictJSONBool(fields["initializing"])
	if json.Unmarshal(fields["schemaVersion"], &version) != nil || version != 1 ||
		json.Unmarshal(fields["checkedAt"], &stamp) != nil || !strings.HasSuffix(stamp, "Z") ||
		!healthyOK || !initializingOK || initializing && !healthy {
		return bad, errors.New("invalid schedule observation")
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || checkedAt.IsZero() {
		return bad, errors.New("invalid schedule observation")
	}
	return backup.ScheduleCheckResult{CheckedAt: checkedAt, Healthy: healthy, Initializing: initializing}, nil
}

func strictJSONBool(raw json.RawMessage) (bool, bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// parseChainResult requires one versioned, exact-field object. No SQL text,
// provider URI, object key or arbitrary labels can cross into metric state.
func parseChainResult(data []byte) (backup.CheckResult, error) {
	bad := backup.CheckResult{}
	if len(data) == 0 || len(data) > maxChainResult {
		return bad, errors.New("invalid chain check result")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return bad, errors.New("invalid chain check result")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		keyToken, err := d.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return bad, errors.New("invalid chain check result")
		}
		switch key {
		case "schemaVersion", "chainId", "collectionId", "checkedAt", "recoveryTimestamp", "fileChecked", "passed":
		default:
			return bad, errors.New("invalid chain check result")
		}
		if _, exists := fields[key]; exists {
			return bad, errors.New("invalid chain check result")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return bad, errors.New("invalid chain check result")
		}
		fields[key] = value
	}
	if len(fields) != 7 {
		return bad, errors.New("invalid chain check result")
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') {
		return bad, errors.New("invalid chain check result")
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return bad, errors.New("invalid chain check result")
	}
	var version int
	var chainID, collectionID, timestamp, checkedAt string
	var checked, passed bool
	if json.Unmarshal(fields["schemaVersion"], &version) != nil || version != 1 ||
		json.Unmarshal(fields["chainId"], &chainID) != nil ||
		json.Unmarshal(fields["collectionId"], &collectionID) != nil ||
		json.Unmarshal(fields["checkedAt"], &checkedAt) != nil ||
		json.Unmarshal(fields["recoveryTimestamp"], &timestamp) != nil ||
		json.Unmarshal(fields["fileChecked"], &checked) != nil ||
		json.Unmarshal(fields["passed"], &passed) != nil || !checked || !passed ||
		!safeEvidenceID(chainID) || !safeEvidenceID(collectionID) ||
		!strings.HasSuffix(timestamp, "Z") || !strings.HasSuffix(checkedAt, "Z") {
		return bad, errors.New("invalid chain check result")
	}
	recovery, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || recovery.IsZero() {
		return bad, errors.New("invalid chain check result")
	}
	checkTime, err := time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil || checkTime.IsZero() || recovery.After(checkTime) {
		return bad, errors.New("invalid chain check result")
	}
	return backup.CheckResult{ChainID: chainID, CollectionID: collectionID,
		RecoveryTimestamp: recovery, CheckedAt: checkTime, FileChecked: true, Passed: true}, nil
}

func safeEvidenceID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.' {
			continue
		}
		return false
	}
	return true
}
