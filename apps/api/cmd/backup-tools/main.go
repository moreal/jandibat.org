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
	"strings"
	"syscall"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/backup"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"go.uber.org/zap"
)

const (
	defaultRecordPath = "/var/run/jandibat-backup/verified.json"
	chainScriptPath   = "/workspace/scripts/db-verify-backup-chain.sh"
	maxChainResult    = 4096
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
	if err := runMode(ctx, os.Args[1:], os.Getenv, nil, nil, os.Stdout); err != nil {
		observability.Log(logger, "backup_tools.stopped", zap.String("reason", "operation failed"))
		return err
	}
	return nil
}

// runMode permits only the two verification modes. The checker and listener
// arguments are test seams; production always uses the fixed packaged script
// and opens its own internal listener.
func runMode(ctx context.Context, args []string, getenv func(string) string, checker backup.Checker, supplied net.Listener, out io.Writer) error {
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
		checker = scriptChecker(chainScriptPath, getenv)
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
	address := getenv("BACKUP_METRICS_LISTEN_ADDR")
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid verifier listen address")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate()) {
		return errors.New("verifier listen address must be internal")
	}
	handler, err := backup.NewVerificationMetricsHandler(state, getenv("BACKUP_METRICS_TOKEN_FILE"))
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

func scriptChecker(path string, getenv func(string) string) backup.Checker {
	return func(ctx context.Context) (backup.CheckResult, error) {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(checkCtx, "sh", path)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"BACKUP_VERIFIER_DATABASE_URL=" + getenv("BACKUP_VERIFIER_DATABASE_URL"),
		}
		if client := getenv("COCKROACH_SQL_BIN"); client != "" {
			cmd.Env = append(cmd.Env, "COCKROACH_SQL_BIN="+client)
		}
		var output boundedBuffer
		output.limit = maxChainResult
		cmd.Stdout = &output
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return backup.CheckResult{}, errors.New("chain check subprocess failed")
		}
		return parseChainResult(output.Bytes())
	}
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
		case "schemaVersion", "chainId", "collectionId", "recoveryTimestamp", "fileChecked":
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
	if len(fields) != 5 {
		return bad, errors.New("invalid chain check result")
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') {
		return bad, errors.New("invalid chain check result")
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return bad, errors.New("invalid chain check result")
	}
	var version int
	var chainID, collectionID, timestamp string
	var checked bool
	if json.Unmarshal(fields["schemaVersion"], &version) != nil || version != 1 ||
		json.Unmarshal(fields["chainId"], &chainID) != nil ||
		json.Unmarshal(fields["collectionId"], &collectionID) != nil ||
		json.Unmarshal(fields["recoveryTimestamp"], &timestamp) != nil ||
		json.Unmarshal(fields["fileChecked"], &checked) != nil || !checked ||
		!safeEvidenceID(chainID) || !safeEvidenceID(collectionID) || !strings.HasSuffix(timestamp, "Z") {
		return bad, errors.New("invalid chain check result")
	}
	recovery, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || recovery.IsZero() {
		return bad, errors.New("invalid chain check result")
	}
	return backup.CheckResult{ChainID: chainID, CollectionID: collectionID,
		RecoveryTimestamp: recovery, FileChecked: true}, nil
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
