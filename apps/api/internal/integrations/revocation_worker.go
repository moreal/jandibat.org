package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultOAuthTokenRevocationMaxAttempts = 8
	maxOAuthTokenRevocationMaxAttempts     = 100
	revocationInitialRetryDelay            = time.Minute
	revocationMaximumRetryDelay            = 24 * time.Hour
	revocationRetryJitterFraction          = 0.2
)

var ErrOAuthTokenRevocationDeadLettered = errors.New("integrations: OAuth token revocation job entered terminal dead-letter state")

type OAuthTokenRevocationWorkerConfig struct {
	PollInterval time.Duration
	Lease        time.Duration
	CallTimeout  time.Duration
	BatchSize    int
	// MaxAttempts bounds provider/decryption attempts before a job becomes
	// terminal. Zero selects the conservative production default.
	MaxAttempts int
	// Jitter is injectable only to make retry scheduling deterministic in
	// tests. Production uses the cryptographic source shared by retry policy.
	Jitter JitterSource
}

// OAuthTokenRevocationDeadLetterStore is the terminal extension of the
// durable revocation store. A dead-letter transition must retain the encrypted
// job for operator inspection while atomically releasing its active lease.
type OAuthTokenRevocationDeadLetterStore interface {
	OAuthTokenRevocationStore
	DeadLetterOAuthTokenRevocation(context.Context, string, string, time.Time) error
}

type OAuthTokenRevocationWorker struct {
	store   OAuthTokenRevocationStore
	cipher  SecretCipher
	revoker OAuthTokenRevoker
	clock   Clock
	config  OAuthTokenRevocationWorkerConfig
}

func NewOAuthTokenRevocationWorker(store OAuthTokenRevocationStore, cipher SecretCipher, revoker OAuthTokenRevoker, clock Clock, config OAuthTokenRevocationWorkerConfig) (*OAuthTokenRevocationWorker, error) {
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaultOAuthTokenRevocationMaxAttempts
	}
	if store == nil || cipher == nil || revoker == nil || config.PollInterval <= 0 || config.Lease <= 0 || config.CallTimeout <= 0 || config.CallTimeout >= config.Lease || config.BatchSize <= 0 || config.BatchSize > 100 || config.MaxAttempts < 1 || config.MaxAttempts > maxOAuthTokenRevocationMaxAttempts {
		return nil, ErrInvalidRevocationConfig
	}
	if _, ok := store.(OAuthTokenRevocationDeadLetterStore); !ok {
		return nil, ErrInvalidRevocationConfig
	}
	if config.Jitter == nil {
		config.Jitter = cryptoJitterSource{}
	}
	return &OAuthTokenRevocationWorker{store: store, cipher: cipher, revoker: revoker, clock: clockOrDefault(clock), config: config}, nil
}

func (worker *OAuthTokenRevocationWorker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if err := worker.RunOnce(ctx); err != nil {
				return err
			}
			timer.Reset(worker.config.PollInterval)
		}
	}
}

func (worker *OAuthTokenRevocationWorker) RunOnce(ctx context.Context) error {
	now := worker.clock.Now()
	jobs, err := worker.store.ClaimOAuthTokenRevocations(ctx, now, now.Add(worker.config.Lease), worker.config.BatchSize)
	if err != nil {
		return fmt.Errorf("claim OAuth token revocations: %w", err)
	}
	var failures []error
	for _, job := range jobs {
		if err := worker.process(ctx, job); err != nil {
			failures = append(failures, fmt.Errorf("OAuth token revocation job %s: %w", job.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (worker *OAuthTokenRevocationWorker) process(ctx context.Context, job OAuthTokenRevocationJob) error {
	// A worker may crash after consuming its last permitted attempt but before
	// persisting disposition. Reclaims increment attempts, so fence them before
	// any further provider call and move them directly to the terminal state.
	if job.Attempts > worker.config.MaxAttempts {
		return worker.deadLetter(ctx, job)
	}
	plaintext, err := worker.cipher.Decrypt(ctx, job.TokenCiphertext)
	if err == nil {
		callCtx, cancel := context.WithTimeout(ctx, worker.config.CallTimeout)
		err = worker.revoker.RevokeOAuthToken(callCtx, job.ProviderID, plaintext)
		cancel()
	}
	clearBytes(plaintext)
	if err == nil {
		return worker.store.CompleteOAuthTokenRevocation(ctx, job.ID, job.ClaimToken)
	}
	if job.Attempts >= worker.config.MaxAttempts {
		return worker.deadLetter(ctx, job)
	}
	availableAt := worker.clock.Now().Add(revocationRetryDelay(job.Attempts, worker.config.Jitter))
	if retryErr := worker.store.RetryOAuthTokenRevocation(ctx, job.ID, job.ClaimToken, availableAt); retryErr != nil {
		return errors.Join(err, retryErr)
	}
	// Provider/decryption failures are durably scheduled and are not fatal to
	// the long-running worker. A later maintenance rotation can repair legacy
	// envelopes without allowing concurrent claims during the active lease.
	return nil
}

func (worker *OAuthTokenRevocationWorker) deadLetter(ctx context.Context, job OAuthTokenRevocationJob) error {
	terminalAt := worker.clock.Now()
	store := worker.store.(OAuthTokenRevocationDeadLetterStore)
	if err := store.DeadLetterOAuthTokenRevocation(ctx, job.ID, job.ClaimToken, terminalAt); err != nil {
		return err
	}
	// A successful durable transition still escapes RunOnce so process health
	// and paging can report the newly terminal job immediately. The original
	// provider/decryption error is deliberately excluded from this signal.
	return ErrOAuthTokenRevocationDeadLettered
}

func revocationRetryDelay(attempt int, jitter JitterSource) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := revocationInitialRetryDelay
	for current := 1; current < attempt && delay < revocationMaximumRetryDelay; current++ {
		if delay > revocationMaximumRetryDelay/2 {
			delay = revocationMaximumRetryDelay
		} else {
			delay *= 2
		}
	}
	random := jitter.Float64()
	if random < 0 {
		random = 0
	} else if random > 1 {
		random = 1
	}
	multiplier := 1 + revocationRetryJitterFraction*(2*random-1)
	delay = time.Duration(float64(delay) * multiplier)
	if delay > revocationMaximumRetryDelay {
		return revocationMaximumRetryDelay
	}
	return delay
}
