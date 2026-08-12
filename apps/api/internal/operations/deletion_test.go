package operations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeletionWorkflowRunsDurableStagesAndRecordsKeyedPseudonym(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	sink := NewMemoryAuditSink()
	store := &deletionStoreStub{auditSink: sink}
	audit, _ := NewAuditRecorder(sink)
	workflow, err := NewDeletionWorkflow(store, retentionClock{now}, audit, []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	request, err := workflow.Request(context.Background(), "request-1", DeletionTargetAccount, "user-sensitive")
	if err != nil {
		t.Fatal(err)
	}
	request.ClaimToken = "018f0000-0000-7000-8000-000000000001"
	store.request = request
	completed, err := workflow.RunClaimed(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != DeletionCompleted || completed.LastCompletedStage != DeletionStageCompleted || completed.AuditEventID == "" ||
		completed.CompletedAt == nil || completed.BackupExpiryAt == nil || !completed.BackupExpiryAt.Equal(completed.CompletedAt.Add(BackupRetentionWindow)) {
		t.Fatalf("completed = %#v", completed)
	}
	if strings.Join(store.calls, ",") != "create,load,revoke,delete,verify,complete_with_audit" {
		t.Fatalf("calls = %#v", store.calls)
	}
	events, _ := sink.Events(context.Background())
	if len(events) != 1 || events[0].Target.ID == "account:deleted" || strings.Contains(events[0].Target.ID, "user-sensitive") ||
		!strings.HasPrefix(events[0].Target.ID, "account:") {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestDeletionRequesterEnqueuesWithoutAuditKey(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := &deletionStoreStub{}
	requester, err := NewDeletionRequester(store, retentionClock{now})
	if err != nil {
		t.Fatal(err)
	}
	request, err := requester.Request(context.Background(), "request-1", DeletionTargetSubject, "subject-1")
	if err != nil || request.TargetID != "subject-1" || strings.Join(store.calls, ",") != "create" {
		t.Fatalf("Request() = %#v, calls=%#v error=%v", request, store.calls, err)
	}
}

func TestDeletionWorkflowPersistsFailureAndResumesFromLastStage(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := &deletionStoreStub{deleteErr: ErrLegalHoldActive}
	audit, _ := NewAuditRecorder(NewMemoryAuditSink())
	workflow, _ := NewDeletionWorkflow(store, retentionClock{now}, audit, []byte(strings.Repeat("p", 32)))
	request, _ := workflow.Request(context.Background(), "request-1", DeletionTargetSubject, "subject-1")
	request.ClaimToken = "018f0000-0000-7000-8000-000000000001"
	store.request = request
	failed, err := workflow.RunClaimed(context.Background(), request)
	if !errors.Is(err, ErrLegalHoldActive) || failed.Status != DeletionFailed || failed.ErrorCode != "legal_hold_active" {
		t.Fatalf("failed = %#v, %v", failed, err)
	}
	store.deleteErr = nil
	store.calls = nil
	store.request.ClaimToken = request.ClaimToken
	completed, err := workflow.RunClaimed(context.Background(), store.request)
	if err != nil || completed.Status != DeletionCompleted {
		t.Fatalf("resumed = %#v, %v", completed, err)
	}
	if strings.Join(store.calls, ",") != "load,delete,verify,complete_with_audit" {
		t.Fatalf("resume repeated completed credentials stage: %#v", store.calls)
	}
}

func TestDeletionCompletionAuditFailureDoesNotCommitCompletedState(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := &deletionStoreStub{completeErr: errors.New("audit transaction rolled back")}
	audit, _ := NewAuditRecorder(NewMemoryAuditSink())
	workflow, _ := NewDeletionWorkflow(store, retentionClock{now}, audit, []byte(strings.Repeat("p", 32)))
	request, _ := workflow.Request(context.Background(), "request-1", DeletionTargetSubject, "subject-1")
	request.ClaimToken = "018f0000-0000-7000-8000-000000000001"
	store.request = request
	failed, err := workflow.RunClaimed(context.Background(), request)
	if err == nil || failed.Status != DeletionFailed || store.request.Status == DeletionCompleted || store.request.AuditEventID != "" {
		t.Fatalf("failure committed contradictory completion: result=%#v stored=%#v err=%v", failed, store.request, err)
	}
	if strings.Count(strings.Join(store.calls, ","), "complete_with_audit") != 1 {
		t.Fatalf("calls = %#v", store.calls)
	}
}

func TestDeletionExecutorRunsClaimedBatchAndPreservesClaimFence(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := &deletionStoreStub{request: DeletionRequest{
		ID: "deletion-1", RequestID: "request-1", TargetType: DeletionTargetSubject, TargetID: "subject-1",
		Status: DeletionRequested, LastCompletedStage: DeletionStageRequested, RequestedAt: now, UpdatedAt: now,
		ClaimToken: "018f0000-0000-7000-8000-000000000001",
	}}
	audit, _ := NewAuditRecorder(NewMemoryAuditSink())
	workflow, _ := NewDeletionWorkflow(store, retentionClock{now}, audit, []byte(strings.Repeat("p", 32)))
	claims := deletionClaimStoreStub{requests: []DeletionRequest{store.request}}
	executor, err := NewDeletionExecutor(claims, workflow, retentionClock{now}, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Run(context.Background())
	if err != nil || result.Claimed != 1 || result.Completed != 1 || result.Failed != 0 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if strings.Join(store.calls, ",") != "load,revoke,delete,verify,complete_with_audit" {
		t.Fatalf("calls = %#v", store.calls)
	}
}

type deletionClaimStoreStub struct {
	requests []DeletionRequest
	err      error
}

func (store deletionClaimStoreStub) ClaimDeletions(context.Context, time.Time, time.Time, int) ([]DeletionRequest, error) {
	return append([]DeletionRequest(nil), store.requests...), store.err
}

type deletionStoreStub struct {
	request     DeletionRequest
	calls       []string
	deleteErr   error
	residuals   DeletionResiduals
	auditSink   AuditEventSink
	completeErr error
	deferCalls  int
}

func (store *deletionStoreStub) CreateOrLoadDeletion(_ context.Context, request DeletionRequest) (DeletionRequest, error) {
	store.calls = append(store.calls, "create")
	request.ID = "deletion-1"
	store.request = request
	return request, nil
}

func (store *deletionStoreStub) EnqueueDeletion(_ context.Context, request DeletionRequest) (DeletionRequest, error) {
	return store.CreateOrLoadDeletion(context.Background(), request)
}

func (store *deletionStoreStub) LoadDeletion(_ context.Context, _ string) (DeletionRequest, error) {
	store.calls = append(store.calls, "load")
	return store.request, nil
}

func (store *deletionStoreStub) RevokeDeletionCredentials(_ context.Context, request DeletionRequest, now time.Time) (DeletionRequest, error) {
	store.calls = append(store.calls, "revoke")
	request.Status = DeletionDeletingPrimary
	request.LastCompletedStage = DeletionStageCredentialsRevoked
	request.SubjectIDs = []string{"subject-1"}
	request.UpdatedAt = now
	store.request = request
	return request, nil
}

func (store *deletionStoreStub) DeletePrimaryData(_ context.Context, request DeletionRequest, now time.Time) (DeletionRequest, error) {
	store.calls = append(store.calls, "delete")
	if store.deleteErr != nil {
		return request, store.deleteErr
	}
	request.Status = DeletionVerifying
	request.LastCompletedStage = DeletionStagePrimaryDeleted
	request.UpdatedAt = now
	store.request = request
	return request, nil
}

func (store *deletionStoreStub) VerifyDeletion(_ context.Context, request DeletionRequest) (DeletionResiduals, error) {
	store.calls = append(store.calls, "verify")
	request.LastCompletedStage = DeletionStageVerified
	store.request = request
	return store.residuals, nil
}

func (store *deletionStoreStub) CompleteDeletionWithAudit(_ context.Context, request DeletionRequest, event AuditEvent, completedAt, backupExpiryAt time.Time) (DeletionRequest, error) {
	store.calls = append(store.calls, "complete_with_audit")
	if store.completeErr != nil {
		return request, store.completeErr
	}
	request.Status = DeletionCompleted
	request.LastCompletedStage = DeletionStageCompleted
	request.CompletedAt = &completedAt
	request.BackupExpiryAt = &backupExpiryAt
	request.ErrorCode = ""
	request.AuditEventID = event.ID
	store.request = request
	if store.auditSink != nil {
		_ = store.auditSink.WriteAuditEvent(context.Background(), event)
	}
	return request, nil
}

func (store *deletionStoreStub) FailDeletion(_ context.Context, request DeletionRequest, code string, now time.Time) error {
	store.calls = append(store.calls, "fail")
	request.Status = DeletionFailed
	request.ErrorCode = code
	request.UpdatedAt = now
	store.request = request
	return nil
}

func (store *deletionStoreStub) DeferDeletionForLegalHold(_ context.Context, request DeletionRequest, now time.Time) error {
	store.calls = append(store.calls, "defer_hold")
	store.deferCalls++
	request.Status = DeletionFailed
	request.ErrorCode = "legal_hold_active"
	request.UpdatedAt = now
	store.request = request
	return nil
}
