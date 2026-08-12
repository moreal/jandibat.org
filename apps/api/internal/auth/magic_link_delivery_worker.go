package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type MagicLinkDeliveryWorkerConfig struct {
	PollInterval time.Duration
	Lease        time.Duration
	CallTimeout  time.Duration
	TokenTTL     time.Duration
	BatchSize    int
	MaxAttempts  int
	Random       io.Reader
	Now          func() time.Time
}

type MagicLinkDeliveryWorker struct {
	repository MagicLinkDeliveryRepository
	mailer     Mailer
	observer   MagicLinkDeliveryObserver
	config     MagicLinkDeliveryWorkerConfig
	random     io.Reader
	now        func() time.Time
}

func NewMagicLinkDeliveryWorker(repository MagicLinkDeliveryRepository, mailer Mailer, observer MagicLinkDeliveryObserver, config MagicLinkDeliveryWorkerConfig) (*MagicLinkDeliveryWorker, error) {
	if repository == nil || mailer == nil || config.PollInterval <= 0 || config.Lease <= 0 || config.CallTimeout <= 0 || config.TokenTTL <= 0 || config.BatchSize <= 0 || config.MaxAttempts <= 0 || config.MaxAttempts > 5 {
		return nil, fmt.Errorf("%w: invalid magic-link delivery worker configuration", ErrInvalidInput)
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &MagicLinkDeliveryWorker{repository: repository, mailer: mailer, observer: observer, config: config, random: config.Random, now: config.Now}, nil
}

func (worker *MagicLinkDeliveryWorker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if _, err := worker.RunOnce(ctx); err != nil && ctx.Err() == nil {
				return err
			}
			timer.Reset(worker.config.PollInterval)
		}
	}
}

func (worker *MagicLinkDeliveryWorker) RunOnce(ctx context.Context) (MagicLinkDeliveryResult, error) {
	now := worker.now().UTC()
	jobs, err := worker.repository.ClaimMagicLinkDeliveries(ctx, now, worker.config.Lease, worker.config.BatchSize, worker.config.MaxAttempts)
	result := MagicLinkDeliveryResult{Claimed: len(jobs)}
	if err != nil {
		return result, fmt.Errorf("claim magic-link deliveries: %w", err)
	}
	for _, job := range jobs {
		if err := worker.deliver(ctx, job, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (worker *MagicLinkDeliveryWorker) deliver(ctx context.Context, job MagicLinkDelivery, result *MagicLinkDeliveryResult) error {
	now := worker.now().UTC()
	tokenID, err := worker.randomUUID()
	if err != nil {
		return err
	}
	token, err := worker.randomToken(32)
	if err != nil {
		return err
	}
	link := MagicLink{
		ID: tokenID, Email: job.RecipientEmail, TokenHash: tokenDigest(token), Purpose: job.Purpose,
		CreatedAt: now, ExpiresAt: now.Add(worker.config.TokenTTL),
	}
	if err := worker.repository.ActivateMagicLinkDelivery(ctx, job.ID, job.ClaimToken, link); err != nil {
		return fmt.Errorf("activate magic-link delivery %s: %w", job.ID, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, worker.config.CallTimeout)
	sendErr := worker.mailer.SendMagicLink(callCtx, MagicLinkMail{
		Email: job.RecipientEmail, Token: token, ExpiresAt: link.ExpiresAt, RedirectURI: job.RedirectURI,
	})
	cancel()
	token = ""
	if sendErr == nil {
		if err := worker.repository.CompleteMagicLinkDelivery(ctx, job.ID, job.ClaimToken, now); err != nil {
			if errors.Is(err, ErrConflict) {
				worker.observe("superseded")
				return nil
			}
			return fmt.Errorf("complete magic-link delivery %s: %w", job.ID, err)
		}
		result.Sent++
		worker.observe("sent")
		return nil
	}
	next := now.Add(deliveryRetryDelay(job.Attempts))
	status, err := worker.repository.RetryMagicLinkDelivery(ctx, job.ID, job.ClaimToken, now, next, worker.config.MaxAttempts)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			worker.observe("superseded")
			return nil
		}
		return fmt.Errorf("record magic-link delivery failure %s: %w", job.ID, err)
	}
	if status == MagicLinkDeliveryDead {
		result.Dead++
		worker.observe("dead")
	} else {
		result.Retry++
		worker.observe("retry")
	}
	return nil
}

func (worker *MagicLinkDeliveryWorker) observe(outcome string) {
	if worker.observer != nil {
		worker.observer.ObserveMagicLinkDelivery(strings.ToLower(outcome))
	}
}

func (worker *MagicLinkDeliveryWorker) randomUUID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(worker.random, value[:]); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func (worker *MagicLinkDeliveryWorker) randomToken(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	defer clear(buffer)
	if _, err := io.ReadFull(worker.random, buffer); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func deliveryRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute << min(attempt-1, 5)
	return min(delay, 30*time.Minute)
}
