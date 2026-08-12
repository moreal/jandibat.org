package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
)

func TestNewHTTPServerHasBoundedProductionLimits(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newHTTPServer("127.0.0.1:8080", handler)

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
	server := newHTTPServer(":0", http.NotFoundHandler())
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
