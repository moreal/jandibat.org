package integrations

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

type ExponentialRetryConfig struct {
	InitialDelay   time.Duration
	MaxDelay       time.Duration
	MaxAttempts    int
	JitterFraction float64
}

// ExponentialRetryPolicy provides capped exponential backoff with symmetric
// jitter. Supplying a JitterSource makes scheduling fully deterministic in
// tests; production defaults use crypto/rand.
type ExponentialRetryPolicy struct {
	config ExponentialRetryConfig
	jitter JitterSource
}

func NewExponentialRetryPolicy(config ExponentialRetryConfig, jitter JitterSource) (*ExponentialRetryPolicy, error) {
	if config.InitialDelay <= 0 || config.MaxDelay < config.InitialDelay || config.MaxAttempts <= 0 ||
		config.JitterFraction < 0 || config.JitterFraction > 1 {
		return nil, ErrInvalidRetryPolicy
	}
	if jitter == nil {
		jitter = cryptoJitterSource{}
	}
	return &ExponentialRetryPolicy{config: config, jitter: jitter}, nil
}

func (policy *ExponentialRetryPolicy) NextRetry(attempt int) (time.Duration, bool) {
	if policy == nil || attempt <= 0 || attempt >= policy.config.MaxAttempts {
		return 0, false
	}
	delay := policy.config.InitialDelay
	for current := 1; current < attempt && delay < policy.config.MaxDelay; current++ {
		if delay > policy.config.MaxDelay/2 {
			delay = policy.config.MaxDelay
		} else {
			delay *= 2
		}
	}
	if delay > policy.config.MaxDelay {
		delay = policy.config.MaxDelay
	}
	if policy.config.JitterFraction == 0 {
		return delay, true
	}
	random := policy.jitter.Float64()
	if random < 0 {
		random = 0
	} else if random > 1 {
		random = 1
	}
	multiplier := 1 + policy.config.JitterFraction*(2*random-1)
	delay = time.Duration(float64(delay) * multiplier)
	if delay < 0 {
		delay = 0
	}
	if delay > policy.config.MaxDelay {
		delay = policy.config.MaxDelay
	}
	return delay, true
}

func defaultRetryPolicy() RetryPolicy {
	policy, _ := NewExponentialRetryPolicy(ExponentialRetryConfig{
		InitialDelay:   time.Minute,
		MaxDelay:       time.Hour,
		MaxAttempts:    8,
		JitterFraction: 0.2,
	}, nil)
	return policy
}

type cryptoJitterSource struct{}

func (cryptoJitterSource) Float64() float64 {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return 0.5
	}
	return float64(binary.BigEndian.Uint64(value[:])>>11) / float64(uint64(1)<<53)
}
