package cockroach

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRevocationOperationsRequirePGXPool(t *testing.T) {
	store := &Store{}
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	id := "018f0000-0000-7000-8000-000000000010"
	claim := "018f0000-0000-7000-8000-000000000011"
	tests := []struct {
		name string
		call func() error
	}{
		{"schema", func() error { return store.CheckRevocationSchema(ctx) }},
		{"revoke", func() error { _, err := store.RevokeConnectionAggregate(ctx, id, now); return err }},
		{"claim", func() error {
			_, err := store.ClaimOAuthTokenRevocations(ctx, now, now.Add(time.Minute), 1)
			return err
		}},
		{"complete", func() error { return store.CompleteOAuthTokenRevocation(ctx, id, claim) }},
		{"retry", func() error { return store.RetryOAuthTokenRevocation(ctx, id, claim, now) }},
		{"dead-letter", func() error { return store.DeadLetterOAuthTokenRevocation(ctx, id, claim, now) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrNilDB) {
				t.Fatalf("%s without pgx pool = %v, want %v", tc.name, err, ErrNilDB)
			}
		})
	}
}
