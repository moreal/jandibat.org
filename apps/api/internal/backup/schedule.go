package backup

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ScheduleCheckResult describes policy observation, never backup recoverability.
// An expected initial paused incremental is healthy and initializing.
type ScheduleCheckResult struct {
	CheckedAt    time.Time
	Healthy      bool
	Initializing bool
}

type ScheduleChecker func(context.Context) (ScheduleCheckResult, error)

// ScheduleState is intentionally separate from durable file-check evidence.
type ScheduleState struct {
	checkMu sync.Mutex
	mu      sync.RWMutex
	now     func() time.Time
	result  ScheduleCheckResult
}

func NewScheduleState(now func() time.Time) *ScheduleState {
	return &ScheduleState{now: now}
}

func (s *ScheduleState) snapshot() ScheduleCheckResult {
	if s == nil {
		return ScheduleCheckResult{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.result
}

func (s *ScheduleState) Healthy() bool {
	if s == nil || s.now == nil {
		return false
	}
	r := s.snapshot()
	age := s.now().UTC().Sub(r.CheckedAt)
	return r.Healthy && !r.CheckedAt.IsZero() && age >= 0 && age <= checkStaleAfter
}

func (s *ScheduleState) Initializing() bool {
	return s.Healthy() && s.snapshot().Initializing
}

// Check can mark schedule health bad, but cannot mutate VerificationState.
// Failures preserve the last successful policy timestamp for stale diagnosis.
func (s *ScheduleState) Check(ctx context.Context, checker ScheduleChecker) error {
	if s == nil || s.now == nil {
		return errors.New("invalid schedule observation")
	}
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	var result ScheduleCheckResult
	var err error
	if checker == nil {
		err = errors.New("missing schedule checker")
	} else {
		result, err = checker(ctx)
	}
	now := s.now().UTC()
	age := now.Sub(result.CheckedAt)
	previous := s.snapshot()
	if err != nil || result.CheckedAt.IsZero() || result.CheckedAt.Before(previous.CheckedAt) ||
		age < 0 || age > checkStaleAfter || result.Initializing && !result.Healthy {
		s.mu.Lock()
		s.result.Healthy = false
		s.result.Initializing = false
		s.mu.Unlock()
		return errors.New("schedule observation failed")
	}
	s.mu.Lock()
	s.result = ScheduleCheckResult{CheckedAt: result.CheckedAt.UTC(), Healthy: result.Healthy, Initializing: result.Initializing}
	s.mu.Unlock()
	return nil
}

func (s *ScheduleState) RunChecks(ctx context.Context, checker ScheduleChecker) {
	_ = s.Check(ctx, checker)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Check(ctx, checker)
		}
	}
}
