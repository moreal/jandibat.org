package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewLoggerProductionWritesOneJSONRecordWithFixedFields(t *testing.T) {
	var output bytes.Buffer
	logger, sync, err := NewLogger(Config{
		Service:  "api",
		Resource: Resource{BuildSHA: "abc123", Environment: "production", Region: "icn"},
		Output:   zapcore.AddSync(&output),
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	Log(logger, "http.request_failed", SafeString("request_id", "request-123"))
	if err := sync(); err != nil {
		t.Fatalf("sync logger: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %q", len(lines), output.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode JSON log: %v: %q", err, lines[0])
	}
	for key, want := range map[string]string{
		"service": "api", "build_sha": "abc123", "environment": "production", "region": "icn",
		"event": "http.request_failed", "request_id": "request-123",
	} {
		if got := record[key]; got != want {
			t.Errorf("record[%q] = %#v, want %q", key, got, want)
		}
	}
}

func TestLogBoundsEventAndAddsTypedRequestFields(t *testing.T) {
	core, observed := observer.New(zap.InfoLevel)
	Log(zap.New(core), "ATTACK event", SafeString("request_id", "request-123"), zap.String("method", "POST"))

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("observed entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["event"] != "unknown" || fields["request_id"] != "request-123" || fields["method"] != "POST" {
		t.Fatalf("observed fields = %#v", fields)
	}
}

func TestSafeFieldsRedactBeforeEncodingAndPreventLineInjection(t *testing.T) {
	var output bytes.Buffer
	logger, _, err := NewLogger(Config{
		Service: "api", Resource: Resource{Environment: "production"}, Output: zapcore.AddSync(&output),
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	Log(logger, "api.process_stopped",
		SafeString("authorization", "Bearer token-value"),
		SafeString("proxy_authorization", "Basic dXNlcjpwYXNz"),
		SafeString("url", "https://example.test/callback?access_token=query-secret&api_key=key-secret"),
		SafeError(errors.New("postgresql://service:p%40ssword@db.example/app?password=db-secret\nforged=true")),
	)

	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("log injection created multiple records: %q", output.String())
	}
	for _, secret := range []string{"token-value", "dXNlcjpwYXNz", "query-secret", "key-secret", "p%40ssword", "db-secret", "\nforged=true"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("JSON log leaked %q: %s", secret, output.String())
		}
	}
	if got := strings.Count(output.String(), "[REDACTED]"); got != 6 {
		t.Fatalf("redaction count = %d, want 6: %s", got, output.String())
	}
}

func TestLoggerSyncIgnoresTerminalErrorsOnly(t *testing.T) {
	for _, test := range []struct {
		name    string
		syncErr error
		wantErr bool
	}{
		{name: "invalid descriptor", syncErr: syscall.EINVAL},
		{name: "not a terminal", syncErr: syscall.ENOTTY},
		{name: "disk failure", syncErr: errors.New("disk failure"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := &syncErrorWriter{syncErr: test.syncErr}
			_, sync, err := NewLogger(Config{Service: "api", Resource: Resource{Environment: "test"}, Output: output})
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			err = sync()
			if (err != nil) != test.wantErr {
				t.Fatalf("sync error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

type syncErrorWriter struct {
	bytes.Buffer
	syncErr error
}

func (writer *syncErrorWriter) Sync() error { return writer.syncErr }
