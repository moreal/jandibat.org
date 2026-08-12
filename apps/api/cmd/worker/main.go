package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
)

func main() {
	if err := run(); err != nil {
		observability.Logf("worker.process_stopped", "%v", err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	settings, err := config.LoadForProcess(os.LookupEnv, config.ProcessWorker)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	observability.Default().SetResource(observability.ResourceFromEnvironment(settings.Environment))
	startupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	app, err := buildWorker(startupCtx, settings)
	cancel()
	if err != nil {
		return fmt.Errorf("startup error: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, app.Close()) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := newHealthServer(settings.WorkerHealthAddress, newWorkerProcessHandler(
		processruntime.NewHealthHandler(operations.NewLiveness(ctx.Done()), app.readiness), app.metrics.Handler(),
	))
	observability.Logf("worker.listening", "address=%s", settings.WorkerHealthAddress)
	return processruntime.ServeAndRun(ctx, server, app.runner, settings.ShutdownTimeout)
}

func newWorkerProcessHandler(health, metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/livez", health)
	mux.Handle("/readyz", health)
	mux.Handle("/healthz", health)
	mux.Handle("/metrics", metrics)
	return mux
}

func newHealthServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
}
