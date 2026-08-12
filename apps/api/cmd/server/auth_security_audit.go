package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

type authSecurityAuditRecorder struct {
	recorder *operations.AuditRecorder
}

func (recorder authSecurityAuditRecorder) RecordSecurityEvent(ctx context.Context, event auth.SecurityEvent) error {
	if recorder.recorder == nil {
		return fmt.Errorf("record auth security event: audit recorder is unavailable")
	}
	if event.Kind != auth.SecurityEventPasskeyCloneSuspected || strings.TrimSpace(event.UserID) == "" ||
		strings.TrimSpace(event.PasskeyID) == "" || event.OccurredAt.IsZero() {
		return fmt.Errorf("record auth security event: invalid event")
	}
	id, err := operations.NewAuditEventID()
	if err != nil {
		return err
	}
	requestID := strings.TrimSpace(middleware.GetReqID(ctx))
	if requestID == "" {
		requestID = "security:" + id
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return recorder.recorder.Record(auditCtx, operations.AuditEvent{
		ID:         id,
		OccurredAt: event.OccurredAt.UTC(),
		Actor:      operations.AuditActor{Type: operations.AuditActorUser, ID: event.UserID},
		Action:     "auth.passkey.clone_suspected",
		Target:     operations.AuditTarget{Type: "passkey", ID: event.PasskeyID},
		Outcome:    operations.AuditDenied,
		RequestID:  requestID,
	})
}

var _ auth.SecurityEventRecorder = authSecurityAuditRecorder{}
