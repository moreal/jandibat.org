package operations

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestReadinessCheckerChecksDependenciesAndPreservesOrder(t *testing.T) {
	checker, err := NewReadinessChecker(time.Second,
		ReadinessDependency{Name: "database", Probe: DependencyProbeFunc(func(context.Context) error { return nil })},
		ReadinessDependency{Name: "provider", Probe: DependencyProbeFunc(func(context.Context) error { return errors.New("secret upstream detail") })},
	)
	if err != nil {
		t.Fatal(err)
	}
	report := checker.Check(context.Background())
	want := ReadinessReport{Ready: false, Dependencies: []DependencyStatus{{Name: "database", Ready: true}, {Name: "provider", Ready: false}}}
	if !reflect.DeepEqual(report, want) {
		t.Fatalf("Check() = %#v, want %#v", report, want)
	}
}

func TestReadinessCheckerTimesOutDependency(t *testing.T) {
	checker, _ := NewReadinessChecker(10*time.Millisecond, ReadinessDependency{
		Name: "slow", Probe: DependencyProbeFunc(func(context.Context) error {
			select {}
		}),
	})
	started := time.Now()
	report := checker.Check(context.Background())
	if report.Ready || time.Since(started) > time.Second {
		t.Fatalf("Check() = %#v after %s", report, time.Since(started))
	}
}

func TestReadinessCheckerContainsProbePanic(t *testing.T) {
	checker, _ := NewReadinessChecker(time.Second, ReadinessDependency{
		Name: "panicking", Probe: DependencyProbeFunc(func(context.Context) error {
			panic("boom")
		}),
	})
	if report := checker.Check(context.Background()); report.Ready || report.Dependencies[0].Ready {
		t.Fatalf("Check() = %#v", report)
	}
}

func TestReadinessAndLivenessValidateSemantics(t *testing.T) {
	if _, err := NewReadinessChecker(0); !errors.Is(err, ErrInvalidReadinessConfig) {
		t.Fatalf("NewReadinessChecker() error = %v", err)
	}
	if _, err := NewReadinessChecker(time.Second,
		ReadinessDependency{Name: "same", Probe: DependencyProbeFunc(func(context.Context) error { return nil })},
		ReadinessDependency{Name: "same", Probe: DependencyProbeFunc(func(context.Context) error { return nil })},
	); !errors.Is(err, ErrInvalidReadinessConfig) {
		t.Fatalf("duplicate dependency error = %v", err)
	}

	shutdown := make(chan struct{})
	liveness := NewLiveness(shutdown)
	if !liveness.Live() {
		t.Fatal("process should initially be live")
	}
	close(shutdown)
	if liveness.Live() {
		t.Fatal("process should not be live after shutdown")
	}
}
