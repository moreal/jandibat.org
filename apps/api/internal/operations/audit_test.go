package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewAuditEventIDReturnsUUIDv4(t *testing.T) {
	id, err := NewAuditEventID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || id[14] != '4' || (id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b') {
		t.Fatalf("audit event ID is not UUIDv4: %q", id)
	}
}

func TestAuditRecorderRedactsNestedSecretsAndCopiesMetadata(t *testing.T) {
	sink := NewMemoryAuditSink()
	recorder, err := NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"authorization": "Bearer top-secret",
		"request": map[string]any{
			"client_secret": "raw-secret",
			"url":           "https://example.test/callback?access_token=abc123&state=ok",
		},
		"tags": []string{"safe", "Bearer nested-secret"},
	}
	event := validAuditEvent("event-1")
	event.Metadata = metadata
	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	metadata["authorization"] = "changed"
	metadata["request"].(map[string]any)["client_secret"] = "changed"
	events, err := sink.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := events[0].Metadata["authorization"]; got != RedactedValue {
		t.Fatalf("authorization was not redacted: %#v", got)
	}
	nested := events[0].Metadata["request"].(map[string]any)
	if got := nested["client_secret"]; got != RedactedValue {
		t.Fatalf("nested secret was not redacted: %#v", got)
	}
	if got := nested["url"]; got != "https://example.test/callback?access_token=[REDACTED]&state=ok" {
		t.Fatalf("query secret was not redacted: %#v", got)
	}
	if got := events[0].Metadata["tags"].([]string)[1]; got != "Bearer [REDACTED]" {
		t.Fatalf("embedded bearer token was not redacted: %#v", got)
	}

	events[0].Metadata["authorization"] = "mutated-copy"
	again, _ := sink.Events(context.Background())
	if got := again[0].Metadata["authorization"]; got != RedactedValue {
		t.Fatalf("caller mutated sink state: %#v", got)
	}
}

func TestAuditRecorderSanitizesActorAndTargetIdentifiers(t *testing.T) {
	legacyGitHubPAT := "gh" + "p_" + "abcdefghijklmnopqrstuvwxyz"
	fineGrainedGitHubPAT := "github" + "_pat_" + "abcdefghijklmnopqrstuvwxyz_012345"
	rsaPrivateKeyMarker := "-----BEGIN " + "RSA PRIVATE KEY-----"
	openSSHPrivateKeyMarker := "-----BEGIN " + "OPENSSH PRIVATE KEY-----"
	tests := []struct {
		name       string
		actorID    string
		targetID   string
		wantActor  string
		wantTarget string
	}{
		{name: "stable internal IDs", actorID: "usr_018f-safe", targetID: "sub_018f-safe", wantActor: "usr_018f-safe", wantTarget: "sub_018f-safe"},
		{name: "authorization credential", actorID: "Bearer actor-secret", targetID: "Basic dGFyZ2V0OnNlY3JldA==", wantActor: RedactedValue, wantTarget: RedactedValue},
		{name: "secret query", actorID: "user?access_token=actor-secret", targetID: "subject?api_key=target-secret", wantActor: RedactedValue, wantTarget: RedactedValue},
		{name: "GitHub legacy PAT", actorID: "usr_018f-" + legacyGitHubPAT, targetID: "sub_018f-" + strings.ToUpper(legacyGitHubPAT), wantActor: RedactedValue, wantTarget: RedactedValue},
		{name: "GitHub fine-grained PAT", actorID: "usr_" + fineGrainedGitHubPAT, targetID: "sub_" + strings.ToUpper(fineGrainedGitHubPAT), wantActor: RedactedValue, wantTarget: RedactedValue},
		{name: "PEM private key", actorID: "actor" + rsaPrivateKeyMarker + "material", targetID: "target" + openSSHPrivateKeyMarker + "material", wantActor: RedactedValue, wantTarget: RedactedValue},
		{name: "OAuth code query", actorID: "user?code=12345678", targetID: "subject&token=abcdefgh", wantActor: RedactedValue, wantTarget: RedactedValue},
		{name: "ordinary query and fragment", actorID: "user-1?view=full", targetID: "subject-1#fragment", wantActor: "user-1", wantTarget: "subject-1"},
		{name: "CRLF injection", actorID: "user-1\r\nforged", targetID: "subject-1\nforged", wantActor: "user-1forged", wantTarget: "subject-1forged"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := NewMemoryAuditSink()
			recorder, err := NewAuditRecorder(sink)
			if err != nil {
				t.Fatal(err)
			}
			event := validAuditEvent("event-identifiers")
			event.Actor.ID = test.actorID
			event.Target.ID = test.targetID
			if err := recorder.Record(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			events, err := sink.Events(context.Background())
			if err != nil || len(events) != 1 {
				t.Fatalf("events = %#v, error = %v", events, err)
			}
			if events[0].Actor.ID != test.wantActor || events[0].Target.ID != test.wantTarget {
				t.Fatalf("identifiers = actor %q target %q, want actor %q target %q", events[0].Actor.ID, events[0].Target.ID, test.wantActor, test.wantTarget)
			}
		})
	}
}

func TestMutationRollbackScopeRunsCleanupOnlyOnRollback(t *testing.T) {
	for _, commit := range []bool{false, true} {
		ctx, scope := WithMutationRollbackScope(context.Background())
		calls := 0
		if !OnMutationRollback(ctx, func() { calls++ }) {
			t.Fatal("rollback hook was not registered")
		}
		if commit {
			scope.Commit()
		}
		scope.Rollback()
		scope.Rollback()
		want := 1
		if commit {
			want = 0
		}
		if calls != want {
			t.Fatalf("commit=%t rollback cleanup calls=%d want=%d", commit, calls, want)
		}
	}
}

func TestMemoryAuditSinkConcurrentWrites(t *testing.T) {
	sink := NewMemoryAuditSink()
	const writers = 64
	var group sync.WaitGroup
	group.Add(writers)
	for index := 0; index < writers; index++ {
		go func(index int) {
			defer group.Done()
			event := validAuditEvent(fmt.Sprintf("event-%03d", index))
			event.Metadata = map[string]any{"api_key": fmt.Sprintf("secret-%d", index)}
			if err := sink.WriteAuditEvent(context.Background(), event); err != nil {
				t.Errorf("write %d: %v", index, err)
			}
		}(index)
	}
	group.Wait()
	events, err := sink.SortedEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers {
		t.Fatalf("got %d events, want %d", len(events), writers)
	}
	for _, event := range events {
		if event.Metadata["api_key"] != RedactedValue {
			t.Fatalf("event %s retained its API key", event.ID)
		}
	}
}

func TestAuditSinkRejectsInvalidAndUnsupportedEvents(t *testing.T) {
	sink := NewMemoryAuditSink()
	if err := sink.WriteAuditEvent(context.Background(), AuditEvent{}); !errors.Is(err, ErrInvalidAuditEvent) {
		t.Fatalf("got %v, want invalid event", err)
	}
	event := validAuditEvent("event-unsupported")
	event.Metadata = map[string]any{"bad": make(chan int)}
	if err := sink.WriteAuditEvent(context.Background(), event); !errors.Is(err, ErrUnsupportedAuditData) {
		t.Fatalf("got %v, want unsupported metadata", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.WriteAuditEvent(ctx, validAuditEvent("event-cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want canceled", err)
	}
}

func validAuditEvent(id string) AuditEvent {
	return AuditEvent{
		ID:         id,
		OccurredAt: time.Date(2026, 8, 12, 9, 0, 0, 0, time.FixedZone("KST", 9*60*60)),
		Actor:      AuditActor{Type: AuditActorUser, ID: "user-1"},
		Action:     "provider.rotate_key",
		Target:     AuditTarget{Type: "custom_provider", ID: "provider-1"},
		Outcome:    AuditSucceeded,
		RequestID:  "request-1",
	}
}
