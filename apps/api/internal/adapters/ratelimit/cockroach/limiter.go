// Package cockroach implements a shared, atomic fixed-window rate limiter.
package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/ratelimit/cockroach/generated"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
)

var ErrInvalidConfig = errors.New("rate limit cockroach: invalid configuration")

type Limiter struct {
	executor appdb.DBTX
	policies map[string]handlers.RateLimitPolicy
	now      func() time.Time
}

var _ handlers.RateLimiter = (*Limiter)(nil)

func New(executor appdb.DBTX, policies map[string]handlers.RateLimitPolicy, now func() time.Time) (*Limiter, error) {
	if executor == nil || len(policies) == 0 {
		return nil, ErrInvalidConfig
	}
	value := reflect.ValueOf(executor)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, ErrInvalidConfig
	}
	if now == nil {
		now = time.Now
	}
	cloned := make(map[string]handlers.RateLimitPolicy, len(policies))
	for rawScope, policy := range policies {
		scope := strings.TrimSpace(rawScope)
		if scope == "" || len(scope) > 128 || scope != rawScope || policy.Limit <= 0 || policy.Window <= 0 {
			return nil, fmt.Errorf("%w: invalid policy %q", ErrInvalidConfig, rawScope)
		}
		cloned[scope] = policy
	}
	return &Limiter{executor: executor, policies: cloned, now: now}, nil
}

func (limiter *Limiter) Allow(ctx context.Context, scope, key string) (bool, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	policy, ok := limiter.policies[scope]
	if !ok {
		return true, 0, nil
	}
	now := limiter.now().UTC()
	windowStart := now.Truncate(policy.Window)
	expiresAt := windowStart.Add(policy.Window)
	keyHash := sha256.Sum256([]byte(key))
	row, err := generated.IncrementRateLimitBucket(ctx, limiter.executor,
		scope, keyHash[:], windowStart, expiresAt, int64(policy.Limit),
	)
	if err != nil {
		return false, 0, fmt.Errorf("increment rate-limit bucket: %w", err)
	}
	if row == nil {
		return false, maxDuration(expiresAt.Sub(now), time.Second), nil
	}
	if row.Count < 1 || row.Count > int64(policy.Limit) {
		return false, 0, fmt.Errorf("%w: database returned count %d for limit %d", ErrInvalidConfig, row.Count, policy.Limit)
	}
	return true, 0, nil
}

func maxDuration(left, right time.Duration) time.Duration {
	if left < right {
		return right
	}
	return left
}
