package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrInvalidReadinessConfig = errors.New("operations: invalid readiness configuration")

// DependencyProbe is deliberately smaller than database or HTTP client APIs,
// allowing readiness to test dependencies without coupling transport handlers
// to concrete adapters.
type DependencyProbe interface {
	Check(context.Context) error
}

type DependencyProbeFunc func(context.Context) error

func (probe DependencyProbeFunc) Check(ctx context.Context) error { return probe(ctx) }

type ReadinessDependency struct {
	Name  string
	Probe DependencyProbe
}

type DependencyStatus struct {
	Name  string
	Ready bool
}

type ReadinessReport struct {
	Ready        bool
	Dependencies []DependencyStatus
}

type ReadinessChecker struct {
	timeout      time.Duration
	dependencies []ReadinessDependency
}

func NewReadinessChecker(timeout time.Duration, dependencies ...ReadinessDependency) (*ReadinessChecker, error) {
	if timeout <= 0 || len(dependencies) == 0 {
		return nil, ErrInvalidReadinessConfig
	}
	seen := make(map[string]struct{}, len(dependencies))
	copyOfDependencies := make([]ReadinessDependency, len(dependencies))
	for index, dependency := range dependencies {
		dependency.Name = strings.TrimSpace(dependency.Name)
		if dependency.Name == "" || dependency.Probe == nil {
			return nil, fmt.Errorf("%w: dependency name and probe are required", ErrInvalidReadinessConfig)
		}
		if _, duplicate := seen[dependency.Name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate dependency %q", ErrInvalidReadinessConfig, dependency.Name)
		}
		seen[dependency.Name] = struct{}{}
		copyOfDependencies[index] = dependency
	}
	return &ReadinessChecker{timeout: timeout, dependencies: copyOfDependencies}, nil
}

// Check evaluates dependencies concurrently with an individual timeout and
// returns only names and booleans, avoiding accidental exposure of DSNs or
// upstream error bodies through a health endpoint.
func (checker *ReadinessChecker) Check(ctx context.Context) ReadinessReport {
	type indexedStatus struct {
		index  int
		status DependencyStatus
	}
	results := make(chan indexedStatus, len(checker.dependencies))
	for index, dependency := range checker.dependencies {
		go func(index int, dependency ReadinessDependency) {
			dependencyCtx, cancel := context.WithTimeout(ctx, checker.timeout)
			defer cancel()
			ready := checkDependency(dependencyCtx, dependency.Probe)
			results <- indexedStatus{index: index, status: DependencyStatus{Name: dependency.Name, Ready: ready}}
		}(index, dependency)
	}

	statuses := make([]indexedStatus, 0, len(checker.dependencies))
	ready := true
	for range checker.dependencies {
		status := <-results
		statuses = append(statuses, status)
		ready = ready && status.status.Ready
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].index < statuses[j].index })
	report := ReadinessReport{Ready: ready, Dependencies: make([]DependencyStatus, len(statuses))}
	for index, status := range statuses {
		report.Dependencies[index] = status.status
	}
	return report
}

func checkDependency(ctx context.Context, probe DependencyProbe) bool {
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- fmt.Errorf("dependency probe panicked: %v", recovered)
			}
		}()
		result <- probe.Check(ctx)
	}()
	select {
	case err := <-result:
		return err == nil
	case <-ctx.Done():
		return false
	}
}

// Liveness reports whether this process has entered shutdown. External
// dependencies intentionally do not affect liveness and therefore cannot cause
// a restart loop during a database outage.
type Liveness struct {
	shuttingDown <-chan struct{}
}

func NewLiveness(shuttingDown <-chan struct{}) Liveness {
	return Liveness{shuttingDown: shuttingDown}
}

func (health Liveness) Live() bool {
	if health.shuttingDown == nil {
		return true
	}
	select {
	case <-health.shuttingDown:
		return false
	default:
		return true
	}
}
