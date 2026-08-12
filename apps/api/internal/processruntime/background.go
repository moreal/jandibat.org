package processruntime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

type Runner interface {
	Run(context.Context) error
}

type NamedRunner struct {
	Name   string
	Runner Runner
}

func RunGroup(ctx context.Context, runners ...NamedRunner) error {
	if len(runners) == 0 {
		<-ctx.Done()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(runners))
	for _, item := range runners {
		item := item
		if item.Runner == nil || item.Name == "" {
			return errors.New("runtime: invalid named runner")
		}
		go func() { results <- result{name: item.Name, err: item.Runner.Run(runCtx)} }()
	}
	first := <-results
	unexpected := ctx.Err() == nil
	cancel()
	all := []result{first}
	for len(all) < len(runners) {
		all = append(all, <-results)
	}
	var failures []error
	for _, item := range all {
		if item.err != nil && !errors.Is(item.err, context.Canceled) && !errors.Is(item.err, context.DeadlineExceeded) {
			failures = append(failures, fmt.Errorf("%s: %w", item.name, item.err))
		} else if unexpected && item.name == first.name {
			failures = append(failures, fmt.Errorf("%s stopped unexpectedly", item.name))
		}
	}
	return errors.Join(failures...)
}

type PeriodicRunner struct {
	Name     string
	Interval time.Duration
	Timeout  time.Duration
	Execute  func(context.Context) error
	Logf     func(string, ...any)
}

func (runner *PeriodicRunner) Run(ctx context.Context) error {
	if runner == nil || runner.Name == "" || runner.Interval <= 0 || runner.Timeout <= 0 || runner.Execute == nil {
		return errors.New("runtime: invalid periodic runner")
	}
	logf := runner.Logf
	if logf == nil {
		logf = log.Printf
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			runCtx, cancel := context.WithTimeout(ctx, runner.Timeout)
			err := runner.Execute(runCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				logf("%s run failed: %v", runner.Name, err)
			}
			timer.Reset(runner.Interval)
		}
	}
}
