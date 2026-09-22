package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"go.uber.org/zap/zapcore"
)

func TestNewHTTPServerHasBoundedProductionLimits(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newHTTPServer("127.0.0.1:8080", handler, nil)

	if server.Addr != "127.0.0.1:8080" || server.Handler == nil {
		t.Fatalf("server routing config = Addr %q, Handler %T", server.Addr, server.Handler)
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 5s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %s, want 15s", server.ReadTimeout)
	}
	if server.WriteTimeout != 35*time.Second {
		t.Fatalf("WriteTimeout = %s, want 35s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s, want 60s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 32<<10 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, 32<<10)
	}
}

func TestHTTPServerLimitsLeaveTimeForHandlerDeadline(t *testing.T) {
	server := newHTTPServer(":0", http.NotFoundHandler(), nil)
	settings, err := config.Load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}

	if server.WriteTimeout <= 30*time.Second {
		t.Fatalf("WriteTimeout = %s, must exceed the router's 30s deadline", server.WriteTimeout)
	}
	if server.ReadTimeout >= server.WriteTimeout {
		t.Fatalf("ReadTimeout %s must be less than WriteTimeout %s", server.ReadTimeout, server.WriteTimeout)
	}
	if settings.ShutdownTimeout <= server.WriteTimeout {
		t.Fatalf("default ShutdownTimeout %s must exceed WriteTimeout %s", settings.ShutdownTimeout, server.WriteTimeout)
	}
	if server.MaxHeaderBytes <= 0 || server.MaxHeaderBytes >= 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d, want a positive bound below 1 MiB", server.MaxHeaderBytes)
	}
}

func TestHTTPServerPanicUsesSafeJSONDiagnosticLogger(t *testing.T) {
	logs := make(logWriteChannel, 1)
	logger, _, err := observability.NewLogger(observability.Config{
		Service: "api", Resource: observability.Resource{Environment: "production"}, Output: logs,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	serverSide, clientSide := net.Pipe()
	listener := newPipeListener(serverSide)
	server := newHTTPServer("pipe", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("opaque-server-panic-secret")
	}), logger)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		_ = clientSide.Close()
		<-serveResult
	})

	if _, err := io.WriteString(clientSide, "GET / HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, _ = io.ReadAll(clientSide)
	select {
	case encoded := <-logs:
		if bytes.Contains(encoded, []byte("opaque-server-panic-secret")) || bytes.Contains(encoded, []byte("goroutine")) {
			t.Fatalf("server diagnostic exposed panic or stack: %s", encoded)
		}
		if bytes.Count(encoded, []byte("\n")) != 1 {
			t.Fatalf("server diagnostic is not one JSON line: %q", encoded)
		}
		var record map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(encoded), &record); err != nil {
			t.Fatalf("decode server diagnostic: %v: %s", err, encoded)
		}
		if record["event"] != "http.server_error" {
			t.Fatalf("server diagnostic = %#v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("server panic produced no structured diagnostic")
	}
}

type logWriteChannel chan []byte

func (writer logWriteChannel) Write(value []byte) (int, error) {
	copyOfValue := bytes.Clone(value)
	writer <- copyOfValue
	return len(value), nil
}

func (logWriteChannel) Sync() error { return nil }

var _ zapcore.WriteSyncer = logWriteChannel(nil)

type pipeListener struct {
	connection chan net.Conn
	closed     chan struct{}
	closeOnce  sync.Once
}

func newPipeListener(connection net.Conn) *pipeListener {
	connections := make(chan net.Conn, 1)
	connections <- connection
	return &pipeListener{connection: connections, closed: make(chan struct{})}
}

func (listener *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connection:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *pipeListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (*pipeListener) Addr() net.Addr { return pipeAddress("pipe") }

type pipeAddress string

func (address pipeAddress) Network() string { return string(address) }
func (address pipeAddress) String() string  { return string(address) }
