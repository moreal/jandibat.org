package integrations

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fixedClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fixedClock) set(value time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = value
}

type sequentialIDs struct {
	mu   sync.Mutex
	next int
}

func (ids *sequentialIDs) NewID() (string, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return fmt.Sprintf("id-%d", ids.next), nil
}

type recordingActivitySink struct {
	mu           sync.Mutex
	environments []activity.Environment
	facts        []activity.Fact
	failFacts    error
	replacements []activity.LoadFactsInput
}

func (sink *recordingActivitySink) SaveEnvironments(_ context.Context, input activity.SaveEnvironmentsInput) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.environments = append(sink.environments, input.Environments...)
	return nil
}

func (sink *recordingActivitySink) SaveFacts(_ context.Context, input activity.SaveFactsInput) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.failFacts != nil {
		return sink.failFacts
	}
	sink.facts = append(sink.facts, input.Facts...)
	return nil
}

func (sink *recordingActivitySink) ReplaceFacts(_ context.Context, input activity.LoadFactsInput, _ []activity.EnvironmentID, _ []activity.Fact) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.replacements = append(sink.replacements, input)
	return nil
}

func mustTestCipher() *AESGCMCipher {
	cipher, err := NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		panic(err)
	}
	return cipher
}

func mustConnectionService(store ConnectionStore, clock Clock, ids IDGenerator) *ConnectionService {
	service, err := NewConnectionService(store, mustTestCipher(), clock, ids)
	if err != nil {
		panic(err)
	}
	return service
}
