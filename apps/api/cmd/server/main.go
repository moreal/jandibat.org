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
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/processruntime"
	"go.uber.org/zap"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 15 * time.Second
	serverWriteTimeout      = 35 * time.Second
	serverIdleTimeout       = 60 * time.Second
	serverMaxHeaderBytes    = 32 << 10
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}

func run() (resultErr error) {
	resource := observability.ResourceFromEnvironment("")
	logger, syncLogger, err := observability.NewLogger(observability.Config{
		Service: "api", Resource: resource, Development: resource.Environment != config.EnvironmentProduction,
	})
	if err != nil {
		return fmt.Errorf("construct logger: %w", err)
	}
	defer func() {
		if resultErr != nil {
			observability.Log(logger, "api.process_stopped", observability.SafeError(resultErr))
		}
		resultErr = errors.Join(resultErr, syncLogger())
	}()
	settings, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	observability.Default().SetResource(observability.ResourceFromEnvironment(settings.Environment))

	startupContext, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	app, err := buildApplication(startupContext, settings, logger)
	startupCancel()
	if err != nil {
		return fmt.Errorf("startup error: %w", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("resource cleanup: %w", err))
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := newHTTPServer(settings.APIAddress, newProcessHandler(app.dependencies, operations.NewLiveness(ctx.Done())), logger)

	serverErrors := make(chan error, 1)
	go func() {
		observability.Log(logger, "api.listening", zap.String("address", settings.APIAddress))
		serverErrors <- server.ListenAndServe()
	}()
	backgroundErrors := make(chan error, 1)
	go func() {
		backgroundErrors <- app.RunBackground(ctx)
	}()

	backgroundStopped := false
	var processErr error
	select {
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			processErr = fmt.Errorf("server error: %w", err)
		} else if err == nil {
			processErr = errors.New("server stopped unexpectedly")
		}
		stop()
	case err := <-backgroundErrors:
		if ctx.Err() == nil {
			if err != nil {
				processErr = fmt.Errorf("background workers stopped unexpectedly: %w", err)
			} else {
				processErr = errors.New("background workers stopped unexpectedly")
			}
		} else if err != nil {
			observability.Log(logger, "api.background_stop_failed", observability.SafeError(err))
		}
		backgroundStopped = true
		stop()
	case <-ctx.Done():
		observability.Log(logger, "api.shutdown_started", zap.String("reason", "signal"))
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		processErr = errors.Join(processErr, fmt.Errorf("graceful shutdown: %w", err))
		_ = server.Close()
	}
	if !backgroundStopped {
		select {
		case err := <-backgroundErrors:
			if err != nil {
				observability.Log(logger, "api.background_stop_failed", observability.SafeError(err))
			}
		case <-shutdownContext.Done():
			processErr = errors.Join(processErr, errors.New("background workers did not stop before the shutdown deadline"))
		}
	}
	return processErr
}

func newProcessHandler(dependencies apihttp.Dependencies, liveness operations.Liveness) http.Handler {
	health := processruntime.NewHealthHandler(liveness, dependencies.Readiness)
	mux := http.NewServeMux()
	mux.Handle("/livez", health)
	mux.Handle("/readyz", health)
	mux.Handle("/", apihttp.NewRouter(dependencies))
	return mux
}

func newHTTPServer(address string, handler http.Handler, logger *zap.Logger) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ErrorLog:          observability.NewHTTPServerErrorLog(logger),
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}
