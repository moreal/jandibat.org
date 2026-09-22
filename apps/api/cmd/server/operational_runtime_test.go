package main

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestBuildCredentialKeyringWritesActiveAndReadsLegacySingleKey(t *testing.T) {
	oldMaterial := bytes.Repeat([]byte{'o'}, 32)
	newMaterial := bytes.Repeat([]byte{'n'}, 32)
	keyring, err := buildCredentialKeyring(config.Config{
		Environment: config.EnvironmentDevelopment, CredentialActiveKeyID: "new",
		CredentialEncryptionKeys: map[string][]byte{"new": newMaterial},
		CredentialCipherKey:      oldMaterial,
	})
	if err != nil {
		t.Fatal(err)
	}
	written, err := keyring.Encrypt(context.Background(), []byte("new value"))
	if err != nil {
		t.Fatal(err)
	}
	if id, enveloped, parseErr := operations.CiphertextKeyID(written); parseErr != nil || !enveloped || id != "new" {
		t.Fatalf("new ciphertext key = %q, enveloped=%t, err=%v", id, enveloped, parseErr)
	}
	oldCipher, _ := integrations.NewAESGCMCipher(oldMaterial)
	legacy, _ := oldCipher.Encrypt(context.Background(), []byte("legacy value"))
	plaintext, err := keyring.Decrypt(context.Background(), legacy)
	if err != nil || string(plaintext) != "legacy value" {
		t.Fatalf("decrypt legacy = %q, %v", plaintext, err)
	}
}

func TestBuildCredentialKeyringDevelopmentFallback(t *testing.T) {
	keyring, err := buildCredentialKeyring(config.Config{Environment: config.EnvironmentDevelopment})
	if err != nil {
		t.Fatal(err)
	}
	if keyring.ActiveKeyID() != developmentCredentialKeyID {
		t.Fatalf("active key = %q", keyring.ActiveKeyID())
	}
}

func TestBuildCredentialKeyringRejectsNon256BitLegacyKey(t *testing.T) {
	if _, err := buildCredentialKeyring(config.Config{
		Environment: config.EnvironmentDevelopment, CredentialCipherKey: bytes.Repeat([]byte{'x'}, 16),
	}); !errors.Is(err, operations.ErrInvalidKeyring) {
		t.Fatalf("buildCredentialKeyring() error = %v", err)
	}
}

func TestPeriodicWorkerRunsImmediatelyAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	worker := &periodicWorker{
		name: "test", interval: time.Hour, timeout: time.Second,
		execute: func(context.Context) error {
			calls.Add(1)
			cancel()
			return nil
		},
		logf: func(string, ...any) {},
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("execute calls = %d", calls.Load())
	}
}

func TestDefaultRetentionRulesMatchProviderMetadataAndSyncJobPolicy(t *testing.T) {
	rules := defaultRetentionRules()
	retention := make(map[operations.RetentionDataset]time.Duration, len(rules))
	for _, rule := range rules {
		if _, duplicate := retention[rule.Dataset]; duplicate {
			t.Fatalf("duplicate retention rule for %q", rule.Dataset)
		}
		retention[rule.Dataset] = rule.RetainFor
	}

	day := 24 * time.Hour
	want := []operations.RetentionRule{
		{Dataset: operations.RetentionRevokedProviderMetadata, RetainFor: 30 * day},
		{Dataset: operations.RetentionOrphanedProviderEnvironments, RetainFor: 0},
		{Dataset: operations.RetentionSuccessfulSyncJobs, RetainFor: 30 * day},
		{Dataset: operations.RetentionFailedSyncJobs, RetainFor: 90 * day},
	}
	for _, rule := range want {
		if got, ok := retention[rule.Dataset]; !ok || got != rule.RetainFor {
			t.Errorf("retention[%q] = %s, %t; want %s", rule.Dataset, got, ok, rule.RetainFor)
		}
	}
}

func TestRunBackgroundPropagatesUnexpectedExitAndCancelsSiblings(t *testing.T) {
	siblingStopped := make(chan struct{})
	app := &application{background: []namedBackgroundRunner{
		{name: "failed", runner: runnerFunc(func(context.Context) error { return errors.New("boom") })},
		{name: "sibling", runner: runnerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			close(siblingStopped)
			return nil
		})},
	}}
	err := app.RunBackground(context.Background())
	if err == nil || !stringsContains(err.Error(), "failed: boom") {
		t.Fatalf("RunBackground() error = %v", err)
	}
	select {
	case <-siblingStopped:
	default:
		t.Fatal("sibling runner was not canceled")
	}
}

type runnerFunc func(context.Context) error

func (run runnerFunc) Run(ctx context.Context) error { return run(ctx) }

func stringsContains(value, substring string) bool {
	return bytes.Contains([]byte(value), []byte(substring))
}
