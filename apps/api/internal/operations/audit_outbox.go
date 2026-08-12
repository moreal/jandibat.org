package operations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrInvalidAuditOutbox = errors.New("operations: invalid mutation audit outbox")

type MutationAuditTransaction interface {
	Active() bool
	CommitFailure() bool
	Enqueue(context.Context, AuditEvent) error
	Commit() error
	Rollback() error
}

type mutationRollbackContextKey struct{}
type mutationFailureCommitContextKey struct{}

type MutationRollbackScope struct {
	mu        sync.Mutex
	committed bool
	hooks     []func()
}

type MutationFailureCommitMarker struct {
	mu     sync.RWMutex
	marked bool
}

func WithMutationFailureCommitMarker(ctx context.Context) (context.Context, *MutationFailureCommitMarker) {
	marker := &MutationFailureCommitMarker{}
	return context.WithValue(ctx, mutationFailureCommitContextKey{}, marker), marker
}

func MarkMutationFailureCommit(ctx context.Context) bool {
	marker, ok := ctx.Value(mutationFailureCommitContextKey{}).(*MutationFailureCommitMarker)
	if !ok || marker == nil {
		return false
	}
	marker.mu.Lock()
	marker.marked = true
	marker.mu.Unlock()
	return true
}

func (marker *MutationFailureCommitMarker) Marked() bool {
	if marker == nil {
		return false
	}
	marker.mu.RLock()
	defer marker.mu.RUnlock()
	return marker.marked
}

func WithMutationRollbackScope(ctx context.Context) (context.Context, *MutationRollbackScope) {
	scope := &MutationRollbackScope{}
	return context.WithValue(ctx, mutationRollbackContextKey{}, scope), scope
}

// OnMutationRollback registers cleanup for an external capability acquired
// before the final state+outbox commit (for example a fresh OAuth token).
func OnMutationRollback(ctx context.Context, hook func()) bool {
	scope, ok := ctx.Value(mutationRollbackContextKey{}).(*MutationRollbackScope)
	if !ok || scope == nil || hook == nil {
		return false
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.committed {
		return false
	}
	scope.hooks = append(scope.hooks, hook)
	return true
}

func (scope *MutationRollbackScope) Commit() {
	if scope == nil {
		return
	}
	scope.mu.Lock()
	scope.committed = true
	scope.hooks = nil
	scope.mu.Unlock()
}

func (scope *MutationRollbackScope) Rollback() {
	if scope == nil {
		return
	}
	scope.mu.Lock()
	if scope.committed {
		scope.mu.Unlock()
		return
	}
	hooks := append([]func(){}, scope.hooks...)
	scope.hooks = nil
	scope.mu.Unlock()
	for index := len(hooks) - 1; index >= 0; index-- {
		hooks[index]()
	}
}

type MutationAuditCoordinator interface {
	BeginMutation(context.Context) (context.Context, MutationAuditTransaction, error)
}

type MutationAuditDelivery struct {
	ID         string
	Event      AuditEvent
	ClaimToken string
	Attempts   int
}

type MutationAuditOutbox interface {
	ClaimMutationAudits(context.Context, time.Time, time.Duration, int, int) ([]MutationAuditDelivery, error)
	DeliverMutationAudit(context.Context, string, string, time.Time) error
	RetryMutationAudit(context.Context, string, string, time.Time, time.Time, int, string) (string, error)
}

type MutationAuditDispatcherConfig struct {
	PollInterval time.Duration
	Lease        time.Duration
	BatchSize    int
	MaxAttempts  int
}

type MutationAuditDispatcher struct {
	store  MutationAuditOutbox
	clock  Clock
	config MutationAuditDispatcherConfig
}

func NewMutationAuditDispatcher(store MutationAuditOutbox, clock Clock, config MutationAuditDispatcherConfig) (*MutationAuditDispatcher, error) {
	if store == nil || clock == nil || config.PollInterval <= 0 || config.Lease <= 0 || config.BatchSize <= 0 || config.MaxAttempts <= 0 || config.MaxAttempts > 5 {
		return nil, ErrInvalidAuditOutbox
	}
	return &MutationAuditDispatcher{store: store, clock: clock, config: config}, nil
}

func (dispatcher *MutationAuditDispatcher) Run(ctx context.Context) error {
	run := func() error {
		_, err := dispatcher.RunOnce(ctx)
		return err
	}
	if err := run(); err != nil {
		return err
	}
	ticker := time.NewTicker(dispatcher.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			if err := run(); err != nil {
				return err
			}
		}
	}
}

func (dispatcher *MutationAuditDispatcher) RunOnce(ctx context.Context) (int, error) {
	now := dispatcher.clock.Now().UTC()
	items, err := dispatcher.store.ClaimMutationAudits(ctx, now, dispatcher.config.Lease, dispatcher.config.BatchSize, dispatcher.config.MaxAttempts)
	if err != nil {
		return 0, fmt.Errorf("claim mutation audits: %w", err)
	}
	delivered := 0
	for _, item := range items {
		if err := dispatcher.store.DeliverMutationAudit(ctx, item.ID, item.ClaimToken, now); err == nil {
			delivered++
			continue
		} else if errors.Is(err, ErrInvalidAuditOutbox) {
			// Another replica may have reclaimed an expired lease between this
			// worker's claim and delivery. The claim token is a fence, so the stale
			// delivery changed no state and is safe to skip. Do not terminate the
			// whole dispatcher or prevent later items in the batch from draining.
			continue
		}
		next := now.Add(time.Duration(item.Attempts) * time.Second)
		if _, retryErr := dispatcher.store.RetryMutationAudit(ctx, item.ID, item.ClaimToken, now, next, dispatcher.config.MaxAttempts, "delivery_failed"); retryErr != nil && !errors.Is(retryErr, ErrInvalidAuditOutbox) {
			return delivered, fmt.Errorf("retry mutation audit: %w", retryErr)
		}
	}
	return delivered, nil
}
