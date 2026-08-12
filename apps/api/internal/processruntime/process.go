package processruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ServeAndRun supervises a health/API server and an optional background
// runner. Either component exiting unexpectedly fails the process; cancellation
// stops work and drains the HTTP server within the configured deadline.
func ServeAndRun(ctx context.Context, server *http.Server, runner Runner, shutdownTimeout time.Duration) error {
	if server == nil || shutdownTimeout <= 0 {
		return errors.New("runtime: invalid process lifecycle configuration")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	var runnerErrors <-chan error
	if runner != nil {
		results := make(chan error, 1)
		runnerErrors = results
		go func() { results <- runner.Run(runCtx) }()
	}

	var processErr error
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			processErr = fmt.Errorf("health server: %w", err)
		} else {
			processErr = errors.New("health server stopped unexpectedly")
		}
	case err := <-runnerErrors:
		if ctx.Err() == nil {
			if err == nil {
				processErr = errors.New("background runner stopped unexpectedly")
			} else {
				processErr = fmt.Errorf("background runner: %w", err)
			}
		}
	case <-ctx.Done():
	}
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		processErr = errors.Join(processErr, fmt.Errorf("graceful shutdown: %w", err))
		_ = server.Close()
	}
	if runnerErrors != nil {
		select {
		case err := <-runnerErrors:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				processErr = errors.Join(processErr, fmt.Errorf("background shutdown: %w", err))
			}
		case <-shutdownCtx.Done():
			processErr = errors.Join(processErr, errors.New("background runner did not stop before shutdown deadline"))
		}
	}
	return processErr
}
