package processruntime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunGroupCancelsSiblingsOnFailure(t *testing.T) {
	stopped := make(chan struct{})
	err := RunGroup(context.Background(),
		NamedRunner{Name: "failed", Runner: runnerFunc(func(context.Context) error { return errors.New("boom") })},
		NamedRunner{Name: "sibling", Runner: runnerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		})},
	)
	if err == nil || !strings.Contains(err.Error(), "failed: boom") {
		t.Fatalf("RunGroup error = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("sibling was not cancelled")
	}
}

func TestPeriodicRunnerRunsImmediatelyAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	runner := &PeriodicRunner{Name: "test", Interval: time.Hour, Timeout: time.Second, Execute: func(context.Context) error {
		calls.Add(1)
		cancel()
		return nil
	}}
	if err := runner.Run(ctx); err != nil || calls.Load() != 1 {
		t.Fatalf("PeriodicRunner.Run = %v, calls = %d", err, calls.Load())
	}
}

type runnerFunc func(context.Context) error

func (run runnerFunc) Run(ctx context.Context) error { return run(ctx) }
