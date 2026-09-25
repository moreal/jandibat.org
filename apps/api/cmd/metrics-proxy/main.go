package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/metricsproxy"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"go.uber.org/zap"
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}

func run() (resultErr error) {
	resource := observability.ResourceFromEnvironment("")
	logger, syncLogger, err := observability.NewLogger(observability.Config{
		Service: "metrics-proxy", Resource: resource, Development: resource.Environment != "production",
	})
	if err != nil {
		return errors.New("metrics proxy logger initialization failed")
	}
	defer func() { resultErr = errors.Join(resultErr, syncLogger()) }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, os.Getenv, logger)
}

// Run starts only the metrics handler. It does not initialize the API application.
func Run(ctx context.Context, getenv func(string) string, logger *zap.Logger) (resultErr error) {
	defer func() {
		if resultErr != nil {
			observability.Log(logger, "metrics_proxy.process_stopped", observability.SafeError(resultErr))
		}
	}()
	if getenv == nil {
		return errors.New("missing environment reader")
	}
	address := getenv("METRICS_LISTEN_ADDR")
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid metrics listen address")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate()) {
		return errors.New("metrics listen address must be internal")
	}
	tokenFile := getenv("METRICS_TOKEN_FILE")
	if tokenFile == "" {
		return errors.New("missing metrics token file")
	}
	f, err := os.Open(tokenFile)
	if err != nil {
		return errors.New("open metrics token file failed")
	}
	defer f.Close()
	token, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(token) > 4096 {
		return errors.New("read metrics token file failed")
	}
	cfg := metricsproxy.Config{
		Source: getenv("METRICS_SOURCE"), Token: token,
		CockroachHost:       getenv("COCKROACH_METRICS_HOST"),
		CockroachCAFile:     getenv("COCKROACH_METRICS_CA_FILE"),
		CockroachServerName: getenv("COCKROACH_METRICS_SERVER_NAME"),
	}
	handler, err := metricsproxy.NewHandler(cfg, nil)
	if err != nil {
		return fmt.Errorf("metrics proxy configuration: %w", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("metrics proxy listen failed")
	}
	defer listener.Close()
	server := newHTTPServer(address, handler, logger)
	observability.Log(logger, "metrics_proxy.listening", zap.String("address", address), zap.String("source", cfg.Source))
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("metrics proxy server failed")
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return errors.New("metrics proxy shutdown failed")
		}
		return nil
	}
}

func newHTTPServer(address string, handler http.Handler, logger *zap.Logger) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ErrorLog: observability.NewHTTPServerErrorLog(logger),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
}
