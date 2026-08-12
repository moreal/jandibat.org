package main

import (
	"context"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestAuthSecurityAuditRecordsOnlyStablePasskeyTarget(t *testing.T) {
	t.Parallel()
	sink := operations.NewMemoryAuditSink()
	audit, err := operations.NewAuditRecorder(sink)
	if err != nil {
		t.Fatal(err)
	}
	recorder := authSecurityAuditRecorder{recorder: audit}
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "request-1")
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

	if err := recorder.RecordSecurityEvent(ctx, auth.SecurityEvent{
		Kind: auth.SecurityEventPasskeyCloneSuspected, UserID: "user-1", PasskeyID: "pk-1", OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := sink.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.Action != "auth.passkey.clone_suspected" || event.Outcome != operations.AuditDenied ||
		event.Actor != (operations.AuditActor{Type: operations.AuditActorUser, ID: "user-1"}) ||
		event.Target != (operations.AuditTarget{Type: "passkey", ID: "pk-1"}) || event.RequestID != "request-1" {
		t.Fatalf("event = %#v", event)
	}
	if len(event.Metadata) != 0 || event.SourceIP != "" {
		t.Fatalf("security event carried unexpected data: %#v", event)
	}
}
