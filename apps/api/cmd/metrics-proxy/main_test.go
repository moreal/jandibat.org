package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func productionTestLogger(t *testing.T, output *bytes.Buffer) *zap.Logger {
	t.Helper()
	logger, _, err := observability.NewLogger(observability.Config{
		Service: "metrics-proxy", Resource: observability.Resource{Environment: "production"}, Output: zapcore.AddSync(output),
	})
	if err != nil {
		t.Fatal(err)
	}
	return logger
}

func logRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("invalid JSON log %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestRunRejectsMissingAndUnsafeConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{"missing source", map[string]string{"METRICS_LISTEN_ADDR": "127.0.0.1:0", "METRICS_TOKEN_FILE": file}},
		{"public listener", map[string]string{"METRICS_SOURCE": "api", "METRICS_LISTEN_ADDR": "0.0.0.0:9000", "METRICS_TOKEN_FILE": file}},
		{"missing token file", map[string]string{"METRICS_SOURCE": "api", "METRICS_LISTEN_ADDR": "127.0.0.1:0"}},
		{"missing cockroach TLS", map[string]string{"METRICS_SOURCE": "cockroach", "METRICS_LISTEN_ADDR": "127.0.0.1:0", "METRICS_TOKEN_FILE": file, "COCKROACH_METRICS_HOST": "db.local"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string { return tc.values[key] }
			var output bytes.Buffer
			if err := Run(context.Background(), getenv, productionTestLogger(t, &output)); err == nil {
				t.Fatal("accepted invalid configuration")
			}
			records := logRecords(t, &output)
			if len(records) != 1 || records[0]["event"] != "metrics_proxy.process_stopped" || records[0]["service"] != "metrics-proxy" {
				t.Fatalf("failure logs=%v", records)
			}
			if strings.Contains(output.String(), "0123456789abcdef") || strings.Contains(output.String(), file) {
				t.Fatalf("failure log exposed secret or file: %s", output.String())
			}
		})
	}
}

func TestRunServesOnlyAuthenticatedMetricsUntilCanceled(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	_ = reservation.Close()
	values := map[string]string{"METRICS_SOURCE": "api", "METRICS_LISTEN_ADDR": address, "METRICS_TOKEN_FILE": file}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var output bytes.Buffer
	logger := productionTestLogger(t, &output)
	go func() {
		done <- Run(ctx, func(key string) string { return values[key] }, logger)
	}()
	client := &http.Client{Timeout: time.Second}
	var response *http.Response
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		response, err = client.Get("http://" + address + "/metrics")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("listener did not start: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
	records := logRecords(t, &output)
	if len(records) != 1 || records[0]["event"] != "metrics_proxy.listening" || records[0]["service"] != "metrics-proxy" {
		t.Fatalf("startup logs=%v", records)
	}
	if strings.Contains(output.String(), "0123456789abcdef") || strings.Contains(output.String(), file) {
		t.Fatalf("startup log exposed secret or file: %s", output.String())
	}
}

func TestHTTPServerDiagnosticsUseSafeJSONLogger(t *testing.T) {
	var output bytes.Buffer
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), productionTestLogger(t, &output))
	server.ErrorLog.Print("panic serving Bearer 0123456789abcdef0123456789abcdef: upstream-secret\nstack")
	records := logRecords(t, &output)
	if len(records) != 1 || records[0]["event"] != "http.server_error" {
		t.Fatalf("server logs=%v", records)
	}
	if strings.Contains(output.String(), "0123456789abcdef") || strings.Contains(output.String(), "upstream-secret") || strings.Contains(output.String(), "stack") {
		t.Fatalf("server log exposed diagnostic: %s", output.String())
	}
}
