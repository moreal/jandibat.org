package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
			if err := Run(context.Background(), getenv, slog.New(slog.NewTextHandler(&strings.Builder{}, nil))); err == nil {
				t.Fatal("accepted invalid configuration")
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
	go func() {
		done <- Run(ctx, func(key string) string { return values[key] }, slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
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
}
