// Package cockroach implements a shared, atomic fixed-window rate limiter.
package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
)

var ErrInvalidConfig = errors.New("rate limit cockroach: invalid configuration")

const incrementBucketQuery = `
INSERT INTO api_rate_limit_buckets (
  scope, key_hash, window_start, count, expires_at
) VALUES ($1, $2, $3, 1, $4)
ON CONFLICT (scope, key_hash, window_start) DO UPDATE SET
  count = api_rate_limit_buckets.count + 1,
  expires_at = excluded.expires_at
WHERE api_rate_limit_buckets.count < $5
RETURNING count`

type Limiter struct {
	db       *sql.DB
	policies map[string]handlers.RateLimitPolicy
	now      func() time.Time
}

var _ handlers.RateLimiter = (*Limiter)(nil)

func New(db *sql.DB, policies map[string]handlers.RateLimitPolicy, now func() time.Time) (*Limiter, error) {
	if db == nil || len(policies) == 0 {
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
	return &Limiter{db: db, policies: cloned, now: now}, nil
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
	var count int64
	err := limiter.db.QueryRowContext(ctx, incrementBucketQuery,
		scope, keyHash[:], windowStart, expiresAt, policy.Limit,
	).Scan(&count)
	if err == sql.ErrNoRows {
		return false, maxDuration(expiresAt.Sub(now), time.Second), nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("increment rate-limit bucket: %w", err)
	}
	if count < 1 || count > int64(policy.Limit) {
		return false, 0, fmt.Errorf("%w: database returned count %d for limit %d", ErrInvalidConfig, count, policy.Limit)
	}
	return true, 0, nil
}

func maxDuration(left, right time.Duration) time.Duration {
	if left < right {
		return right
	}
	return left
}
