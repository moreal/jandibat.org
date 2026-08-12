package handlers

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMemoryRateLimiterSeparatesScopesAndExpiresWindows(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	limiter := NewMemoryRateLimiter(map[string]RateLimitPolicy{
		"account": {Limit: 1, Window: time.Hour},
		"ip":      {Limit: 2, Window: time.Hour},
	}, func() time.Time { return now })
	ctx := context.Background()

	allowed, _, err := limiter.Allow(ctx, "account", "same@example.test")
	if err != nil || !allowed {
		t.Fatalf("first account request = %v, %v", allowed, err)
	}
	allowed, retryAfter, err := limiter.Allow(ctx, "account", "same@example.test")
	if err != nil || allowed || retryAfter != time.Hour {
		t.Fatalf("limited account request = %v, %s, %v", allowed, retryAfter, err)
	}
	allowed, _, err = limiter.Allow(ctx, "ip", "192.0.2.1")
	if err != nil || !allowed {
		t.Fatalf("independent IP request = %v, %v", allowed, err)
	}

	now = now.Add(time.Hour)
	allowed, _, err = limiter.Allow(ctx, "account", "same@example.test")
	if err != nil || !allowed {
		t.Fatalf("expired account window = %v, %v", allowed, err)
	}
}

func TestDefaultRateLimiterHasSecurityPolicies(t *testing.T) {
	limiter := DefaultRateLimiter()
	want := map[string]RateLimitPolicy{
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
	if len(limiter.policies) != len(want) {
		t.Fatalf("policy count = %d, want %d: %#v", len(limiter.policies), len(want), limiter.policies)
	}
	for scope, expected := range want {
		if policy, ok := limiter.policies[scope]; !ok || policy != expected {
			t.Fatalf("policy %s = %#v, want %#v", scope, policy, expected)
		}
	}
}

func TestRateLimitIdentityIsNormalizedAndOpaque(t *testing.T) {
	raw := "  User.Secret+Tag@Example.Test  "
	first := rateLimitIdentity("email", raw)
	second := rateLimitIdentity("email", strings.TrimSpace(raw))
	if first != second {
		t.Fatalf("identity is not whitespace-normalized: %q != %q", first, second)
	}
	if strings.Contains(first, "User.Secret") || !strings.HasPrefix(first, "email:sha256:") || len(first) != len("email:sha256:")+64 {
		t.Fatalf("identity is not an opaque SHA-256 label: %q", first)
	}
	if first == rateLimitIdentity("ceremony", strings.TrimSpace(raw)) {
		t.Fatal("identity kind was not domain-separated")
	}
}
