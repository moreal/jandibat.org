package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Rate-limit scopes are deliberately split by network and stable identity.
// A request must pass every applicable bucket; changing IPs cannot reset an
// account/ceremony quota, while unrelated accounts behind a NAT retain their
// own identity quota. Sensitive identifiers are hashed before this boundary.
const (
	rateScopeMagicLinkIP           = "magic_link_ip"            // 20 / 15m
	rateScopeMagicLinkAccount      = "magic_link_account"       // 5 / 15m
	rateScopeMagicLinkIPDaily      = "magic_link_ip_daily"      // 100 / day
	rateScopeMagicLinkAccountDaily = "magic_link_account_daily" // 20 / day
	rateScopeMagicConsumeIP        = "magic_link_consume_ip"    // 20 / 15m
	rateScopeMagicConsumeToken     = "magic_link_consume_token" // 5 / 15m
	rateScopePasskeyIP             = "passkey_ip"               // 30 / minute
	rateScopePasskeyAccount        = "passkey_account"          // 20 / minute
	rateScopePasskeyEmail          = "passkey_email"            // 10 / minute
	rateScopePasskeyCeremony       = "passkey_ceremony"         // 10 / minute
	rateScopeConnectionIP          = "connection_ip"            // 20 / minute
	rateScopeConnectionAccount     = "connection_account"       // 20 / minute
	rateScopeSyncIP                = "sync_ip"                  // 10 / minute
	rateScopeSyncAccount           = "sync_account"             // 10 / minute
	rateScopeRotateKeyIP           = "rotate_key_ip"            // 5 / hour
	rateScopeRotateKeyAccount      = "rotate_key_account"       // 5 / hour
	rateScopeCustomIngestIP        = "custom_ingest_ip"         // 120 / minute
	rateScopeCustomIngestProvider  = "custom_ingest_provider"   // 120 / minute
	rateScopePublicActivityIP      = "public_activity_ip"       // 60 / minute
	rateScopePublicActivityAccount = "public_activity_account"  // 60 / minute
	rateScopeMutationIntentIP      = "mutation_intent_ip"       // 120 / minute
	rateScopeMutationIntentSession = "mutation_intent_session"  // 120 / minute
)

type RateLimiter interface {
	Allow(context.Context, string, string) (bool, time.Duration, error)
}

type RateLimitPolicy struct {
	Limit  int
	Window time.Duration
}

type rateWindow struct {
	started time.Time
	count   int
}

// MemoryRateLimiter is a bounded, process-local fixed-window limiter. A shared
// deployment can provide the same port with a transactional Redis/SQL adapter.
type MemoryRateLimiter struct {
	mu       sync.Mutex
	policies map[string]RateLimitPolicy
	windows  map[string]rateWindow
	now      func() time.Time
}

func NewMemoryRateLimiter(policies map[string]RateLimitPolicy, now func() time.Time) *MemoryRateLimiter {
	if now == nil {
		now = time.Now
	}
	cloned := make(map[string]RateLimitPolicy, len(policies))
	for key, policy := range policies {
		cloned[key] = policy
	}
	return &MemoryRateLimiter{policies: cloned, windows: make(map[string]rateWindow), now: now}
}

func DefaultRateLimiter() *MemoryRateLimiter {
	return NewMemoryRateLimiter(DefaultRateLimitPolicies(), nil)
}

func DefaultRateLimitPolicies() map[string]RateLimitPolicy {
	return map[string]RateLimitPolicy{
		rateScopeMagicLinkIP:           {Limit: 20, Window: 15 * time.Minute},
		rateScopeMagicLinkAccount:      {Limit: 5, Window: 15 * time.Minute},
		rateScopeMagicLinkIPDaily:      {Limit: 100, Window: 24 * time.Hour},
		rateScopeMagicLinkAccountDaily: {Limit: 20, Window: 24 * time.Hour},
		rateScopeMagicConsumeIP:        {Limit: 20, Window: 15 * time.Minute},
		rateScopeMagicConsumeToken:     {Limit: 5, Window: 15 * time.Minute},
		rateScopePasskeyIP:             {Limit: 30, Window: time.Minute},
		rateScopePasskeyAccount:        {Limit: 20, Window: time.Minute},
		rateScopePasskeyEmail:          {Limit: 10, Window: time.Minute},
		rateScopePasskeyCeremony:       {Limit: 10, Window: time.Minute},
		rateScopeConnectionIP:          {Limit: 20, Window: time.Minute},
		rateScopeConnectionAccount:     {Limit: 20, Window: time.Minute},
		rateScopeSyncIP:                {Limit: 10, Window: time.Minute},
		rateScopeSyncAccount:           {Limit: 10, Window: time.Minute},
		rateScopeRotateKeyIP:           {Limit: 5, Window: time.Hour},
		rateScopeRotateKeyAccount:      {Limit: 5, Window: time.Hour},
		rateScopeCustomIngestIP:        {Limit: 120, Window: time.Minute},
		rateScopeCustomIngestProvider:  {Limit: 120, Window: time.Minute},
		rateScopePublicActivityIP:      {Limit: 60, Window: time.Minute},
		rateScopePublicActivityAccount: {Limit: 60, Window: time.Minute},
		rateScopeMutationIntentIP:      {Limit: 120, Window: time.Minute},
		rateScopeMutationIntentSession: {Limit: 120, Window: time.Minute},
	}
}

func rateLimitIdentity(kind, value string) string {
	normalized := strings.TrimSpace(value)
	digest := sha256.Sum256([]byte(kind + "\x00" + normalized))
	return kind + ":sha256:" + hex.EncodeToString(digest[:])
}

func (limiter *MemoryRateLimiter) Allow(ctx context.Context, scope, key string) (bool, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	policy, ok := limiter.policies[scope]
	if !ok || policy.Limit <= 0 || policy.Window <= 0 {
		return true, 0, nil
	}
	now := limiter.now().UTC()
	windowKey := scope + "\x00" + key
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	window := limiter.windows[windowKey]
	if window.started.IsZero() || !now.Before(window.started.Add(policy.Window)) {
		window = rateWindow{started: now}
	}
	if window.count >= policy.Limit {
		return false, window.started.Add(policy.Window).Sub(now), nil
	}
	window.count++
	limiter.windows[windowKey] = window
	return true, 0, nil
}
