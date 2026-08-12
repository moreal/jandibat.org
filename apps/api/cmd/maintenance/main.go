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
		observability.Logf("maintenance.stopped", "outcome=failed")
		os.Exit(1)
	}
}

func run() (resultErr error) {
	settings, err := config.LoadForProcess(os.LookupEnv, config.ProcessMaintenance)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	observability.Default().SetResource(observability.ResourceFromEnvironment(settings.Environment))
	startupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	app, err := buildMaintenance(startupCtx, settings)
	cancel()
	if err != nil {
		return fmt.Errorf("startup error: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, app.Close()) }()
	if isMaintenanceCommand(os.Args[1:]) {
		commandCtx, commandCancel := context.WithTimeout(context.Background(), settings.MaintenanceTimeout)
		defer commandCancel()
		return runMaintenanceCommand(commandCtx, app, os.Args[1:], os.Stdout)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := newHealthServer(settings.MaintenanceHealthAddress, newMaintenanceProcessHandler(
		processruntime.NewHealthHandler(operations.NewLiveness(ctx.Done()), app.readiness), app.metrics.Handler(),
	))
	observability.Logf("maintenance.listening", "address=%s", settings.MaintenanceHealthAddress)
	return processruntime.ServeAndRun(ctx, server, app.runner, settings.ShutdownTimeout)
}

func newMaintenanceProcessHandler(health, metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/livez", health)
	mux.Handle("/readyz", health)
	mux.Handle("/healthz", health)
	mux.Handle("/metrics", metrics)
	return mux
}
