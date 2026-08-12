package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationAuditDispatcherSkipsStaleClaimAndDrainsBatch(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := &mutationAuditOutboxStub{
		items: []MutationAuditDelivery{
			{ID: "stale", ClaimToken: "old-claim", Attempts: 1},
			{ID: "current", ClaimToken: "current-claim", Attempts: 1},
		},
		deliverErrors: map[string]error{"stale": ErrInvalidAuditOutbox},
	}
	dispatcher, err := NewMutationAuditDispatcher(store, retentionClock{now: now}, MutationAuditDispatcherConfig{
		PollInterval: time.Second, Lease: time.Minute, BatchSize: 25, MaxAttempts: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := dispatcher.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("stale fenced delivery terminated dispatcher: %v", err)
	}
	if delivered != 1 || len(store.deliveries) != 2 || len(store.retries) != 0 {
		t.Fatalf("delivered=%d deliveries=%v retries=%v", delivered, store.deliveries, store.retries)
	}
}

func TestMutationAuditDispatcherRetriesOrdinaryDeliveryFailureAndIgnoresLostRetryFence(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := &mutationAuditOutboxStub{
		items:         []MutationAuditDelivery{{ID: "reclaimed", ClaimToken: "old-claim", Attempts: 2}},
		deliverErrors: map[string]error{"reclaimed": errors.New("sink unavailable")},
		retryErrors:   map[string]error{"reclaimed": ErrInvalidAuditOutbox},
	}
	dispatcher, err := NewMutationAuditDispatcher(store, retentionClock{now: now}, MutationAuditDispatcherConfig{
		PollInterval: time.Second, Lease: time.Minute, BatchSize: 25, MaxAttempts: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.RunOnce(context.Background()); err != nil || delivered != 0 || len(store.retries) != 1 {
		t.Fatalf("RunOnce delivered=%d error=%v retries=%v", delivered, err, store.retries)
	}
}

type mutationAuditOutboxStub struct {
	items         []MutationAuditDelivery
	deliverErrors map[string]error
	retryErrors   map[string]error
	deliveries    []string
	retries       []string
}

func (store *mutationAuditOutboxStub) ClaimMutationAudits(context.Context, time.Time, time.Duration, int, int) ([]MutationAuditDelivery, error) {
	return append([]MutationAuditDelivery(nil), store.items...), nil
}

func (store *mutationAuditOutboxStub) DeliverMutationAudit(_ context.Context, id, _ string, _ time.Time) error {
	store.deliveries = append(store.deliveries, id)
	return store.deliverErrors[id]
}

func (store *mutationAuditOutboxStub) RetryMutationAudit(_ context.Context, id, _ string, _, _ time.Time, _ int, _ string) (string, error) {
	store.retries = append(store.retries, id)
	return "pending", store.retryErrors[id]
}
