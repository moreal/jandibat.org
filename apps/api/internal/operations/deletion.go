package operations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const BackupRetentionWindow = 35 * 24 * time.Hour

var (
	ErrInvalidDeletionRequest = errors.New("operations: invalid deletion request")
	ErrDeletionNotFound       = errors.New("operations: deletion request not found")
	ErrLegalHoldActive        = errors.New("operations: active legal hold prevents deletion")
	ErrDeletionResiduals      = errors.New("operations: deletion verification found residual data")
	ErrDeletionLeaseLost      = errors.New("operations: deletion request lease was lost")
)

type DeletionTargetType string
type DeletionStatus string
type DeletionStage string

const (
	DeletionTargetAccount DeletionTargetType = "account"
	DeletionTargetSubject DeletionTargetType = "subject"

	DeletionRequested       DeletionStatus = "requested"
	DeletionRevoking        DeletionStatus = "revoking"
	DeletionDeletingPrimary DeletionStatus = "deleting_primary"
	DeletionVerifying       DeletionStatus = "verifying"
	DeletionCompleted       DeletionStatus = "completed"
	DeletionFailed          DeletionStatus = "failed"

	DeletionStageRequested          DeletionStage = "requested"
	DeletionStageCredentialsRevoked DeletionStage = "credentials_revoked"
	DeletionStagePrimaryDeleted     DeletionStage = "primary_deleted"
	DeletionStageVerified           DeletionStage = "verified"
	DeletionStageCompleted          DeletionStage = "completed"
)

type DeletionRequest struct {
	ID                 string
	RequestID          string
	TargetType         DeletionTargetType
	TargetID           string
	Status             DeletionStatus
	LastCompletedStage DeletionStage
	ErrorCode          string
	SubjectIDs         []string
	RequestedAt        time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
	BackupExpiryAt     *time.Time
	AuditEventID       string
	Attempts           int64
	AvailableAt        time.Time
	LeaseUntil         *time.Time
	ClaimToken         string
}

type DeletionResiduals struct {
	Subjects            int64
	Passkeys            int64
	Sessions            int64
	MagicLinks          int64
	AuthChallenges      int64
	ProviderConnections int64
	PrivateConsents     int64
	CustomProviders     int64
	ActivityFacts       int64
	TimelineCache       int64
	ActivityRefresh     int64
	ProviderSyncJobs    int64
}

func (residuals DeletionResiduals) Total() int64 {
	return residuals.Subjects + residuals.Passkeys + residuals.Sessions + residuals.MagicLinks + residuals.AuthChallenges +
		residuals.ProviderConnections + residuals.PrivateConsents + residuals.CustomProviders + residuals.ActivityFacts +
		residuals.TimelineCache + residuals.ActivityRefresh + residuals.ProviderSyncJobs
}

type DeletionStore interface {
	CreateOrLoadDeletion(context.Context, DeletionRequest) (DeletionRequest, error)
	LoadDeletion(context.Context, string) (DeletionRequest, error)
	RevokeDeletionCredentials(context.Context, DeletionRequest, time.Time) (DeletionRequest, error)
	DeletePrimaryData(context.Context, DeletionRequest, time.Time) (DeletionRequest, error)
	VerifyDeletion(context.Context, DeletionRequest) (DeletionResiduals, error)
	CompleteDeletionWithAudit(context.Context, DeletionRequest, AuditEvent, time.Time, time.Time) (DeletionRequest, error)
	FailDeletion(context.Context, DeletionRequest, string, time.Time) error
	DeferDeletionForLegalHold(context.Context, DeletionRequest, time.Time) error
}

type DeletionRequestStore interface {
	EnqueueDeletion(context.Context, DeletionRequest) (DeletionRequest, error)
}

type DeletionClaimStore interface {
	ClaimDeletions(context.Context, time.Time, time.Time, int) ([]DeletionRequest, error)
}

type DeletionExecutionResult struct {
	Claimed   int
	Completed int
	Failed    int
}

type DeletionExecutor struct {
	claims    DeletionClaimStore
	workflow  *DeletionWorkflow
	clock     Clock
	batchSize int
	lease     time.Duration
}

func NewDeletionExecutor(claims DeletionClaimStore, workflow *DeletionWorkflow, clock Clock, batchSize int, lease time.Duration) (*DeletionExecutor, error) {
	if claims == nil || workflow == nil || clock == nil || batchSize <= 0 || lease <= 0 {
		return nil, fmt.Errorf("%w: claim store, workflow, clock, batch size, and lease are required", ErrInvalidDeletionRequest)
	}
	return &DeletionExecutor{claims: claims, workflow: workflow, clock: clock, batchSize: batchSize, lease: lease}, nil
}

// Run claims a bounded batch with durable leases. Each claimed request carries
// a fencing token, so an executor that resumes after its lease was reclaimed
// cannot overwrite the newer attempt's state.
func (executor *DeletionExecutor) Run(ctx context.Context) (DeletionExecutionResult, error) {
	now := executor.clock.Now().UTC()
	claimed, err := executor.claims.ClaimDeletions(ctx, now, now.Add(executor.lease), executor.batchSize)
	result := DeletionExecutionResult{Claimed: len(claimed)}
	if err != nil {
		return result, fmt.Errorf("claim deletion requests: %w", err)
	}
	var failures []error
	for _, request := range claimed {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		if _, runErr := executor.workflow.RunClaimed(ctx, request); runErr != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("run deletion request %s: %w", request.RequestID, runErr))
			continue
		}
		result.Completed++
	}
	return result, errors.Join(failures...)
}

type DeletionRequester struct {
	store DeletionRequestStore
	clock Clock
}

func NewDeletionRequester(store DeletionRequestStore, clock Clock) (*DeletionRequester, error) {
	if store == nil || clock == nil {
		return nil, fmt.Errorf("%w: request store and clock are required", ErrInvalidDeletionRequest)
	}
	return &DeletionRequester{store: store, clock: clock}, nil
}

func (requester *DeletionRequester) Request(ctx context.Context, requestID string, targetType DeletionTargetType, targetID string) (DeletionRequest, error) {
	now := requester.clock.Now().UTC()
	request := DeletionRequest{
		RequestID: strings.TrimSpace(requestID), TargetType: targetType, TargetID: strings.TrimSpace(targetID),
		Status: DeletionRequested, LastCompletedStage: DeletionStageRequested, RequestedAt: now, UpdatedAt: now,
	}
	if err := ValidateDeletionRequest(request); err != nil {
		return DeletionRequest{}, err
	}
	return requester.store.EnqueueDeletion(ctx, request)
}

type DeletionWorkflow struct {
	store        DeletionStore
	clock        Clock
	audit        *AuditRecorder
	pseudonymKey []byte
}

func NewDeletionWorkflow(store DeletionStore, clock Clock, audit *AuditRecorder, pseudonymKey []byte) (*DeletionWorkflow, error) {
	if store == nil || clock == nil || audit == nil || len(pseudonymKey) < 32 {
		return nil, fmt.Errorf("%w: store, clock, audit recorder, and 32-byte pseudonym key are required", ErrInvalidDeletionRequest)
	}
	return &DeletionWorkflow{store: store, clock: clock, audit: audit, pseudonymKey: append([]byte(nil), pseudonymKey...)}, nil
}

func (workflow *DeletionWorkflow) Request(ctx context.Context, requestID string, targetType DeletionTargetType, targetID string) (DeletionRequest, error) {
	now := workflow.clock.Now().UTC()
	request := DeletionRequest{
		RequestID: strings.TrimSpace(requestID), TargetType: targetType, TargetID: strings.TrimSpace(targetID),
		Status: DeletionRequested, LastCompletedStage: DeletionStageRequested, RequestedAt: now, UpdatedAt: now,
	}
	if err := ValidateDeletionRequest(request); err != nil {
		return DeletionRequest{}, err
	}
	return workflow.store.CreateOrLoadDeletion(ctx, request)
}

// Run advances a deletion request through every remaining durable stage. Each
// store transition is idempotent and transactional. A failed request resumes
// from LastCompletedStage rather than repeating an earlier destructive step.
func (workflow *DeletionWorkflow) Run(ctx context.Context, requestID string) (DeletionRequest, error) {
	request, err := workflow.store.LoadDeletion(ctx, strings.TrimSpace(requestID))
	if err != nil {
		return DeletionRequest{}, err
	}
	if request.Status == DeletionCompleted {
		return request, nil
	}
	return request, ErrDeletionLeaseLost
}

func (workflow *DeletionWorkflow) RunClaimed(ctx context.Context, claim DeletionRequest) (DeletionRequest, error) {
	if strings.TrimSpace(claim.RequestID) == "" || strings.TrimSpace(claim.ClaimToken) == "" {
		return DeletionRequest{}, ErrInvalidDeletionRequest
	}
	request, err := workflow.store.LoadDeletion(ctx, claim.RequestID)
	if err != nil {
		return DeletionRequest{}, err
	}
	if request.ClaimToken != claim.ClaimToken {
		return request, ErrDeletionLeaseLost
	}
	return workflow.run(ctx, request)
}

func (workflow *DeletionWorkflow) run(ctx context.Context, request DeletionRequest) (DeletionRequest, error) {
	if request.Status == DeletionCompleted {
		return request, nil
	}
	var err error

	if stageBefore(request.LastCompletedStage, DeletionStageCredentialsRevoked) {
		request, err = workflow.store.RevokeDeletionCredentials(ctx, request, workflow.clock.Now().UTC())
		if err != nil {
			return workflow.fail(ctx, request, err)
		}
	}
	if stageBefore(request.LastCompletedStage, DeletionStagePrimaryDeleted) {
		request, err = workflow.store.DeletePrimaryData(ctx, request, workflow.clock.Now().UTC())
		if err != nil {
			return workflow.fail(ctx, request, err)
		}
	}
	if stageBefore(request.LastCompletedStage, DeletionStageVerified) {
		residuals, verifyErr := workflow.store.VerifyDeletion(ctx, request)
		if verifyErr != nil {
			return workflow.fail(ctx, request, verifyErr)
		}
		if residuals.Total() != 0 {
			return workflow.fail(ctx, request, fmt.Errorf("%w: %d rows remain", ErrDeletionResiduals, residuals.Total()))
		}
		request.Status = DeletionVerifying
		request.LastCompletedStage = DeletionStageVerified
	}

	now := workflow.clock.Now().UTC()
	backupExpiry := now.Add(BackupRetentionWindow)
	auditID, err := NewAuditEventID()
	if err != nil {
		return workflow.fail(ctx, request, err)
	}
	event := AuditEvent{
		ID: auditID, OccurredAt: now, Actor: AuditActor{Type: AuditActorSystem},
		Action: "deletion.completed", Target: AuditTarget{Type: string(request.TargetType), ID: workflow.pseudonymousDeletionTarget(request.TargetType, request.TargetID)},
		Outcome: AuditSucceeded, RequestID: request.RequestID,
		Metadata: map[string]any{"backup_expiry_at": backupExpiry.Format(time.RFC3339Nano), "subject_count": len(request.SubjectIDs)},
	}
	redacted, err := workflow.audit.redactor.RedactEvent(event)
	if err != nil {
		return workflow.fail(ctx, request, err)
	}
	completed, err := workflow.store.CompleteDeletionWithAudit(ctx, request, redacted, now, backupExpiry)
	if err != nil {
		return workflow.fail(ctx, request, err)
	}
	return completed, nil
}

func (workflow *DeletionWorkflow) fail(ctx context.Context, request DeletionRequest, cause error) (DeletionRequest, error) {
	code := deletionErrorCode(cause)
	now := workflow.clock.Now().UTC()
	var failErr error
	if errors.Is(cause, ErrLegalHoldActive) {
		failErr = workflow.store.DeferDeletionForLegalHold(ctx, request, now)
	} else {
		failErr = workflow.store.FailDeletion(ctx, request, code, now)
	}
	if failErr != nil {
		return request, errors.Join(cause, failErr)
	}
	request.Status = DeletionFailed
	request.ErrorCode = code
	return request, cause
}

func deletionErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrLegalHoldActive):
		return "legal_hold_active"
	case errors.Is(err, ErrDeletionResiduals):
		return "residual_data"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "operation_failed"
	}
}

func stageBefore(current, target DeletionStage) bool {
	rank := map[DeletionStage]int{
		"": 0, DeletionStageRequested: 1, DeletionStageCredentialsRevoked: 2,
		DeletionStagePrimaryDeleted: 3, DeletionStageVerified: 4, DeletionStageCompleted: 5,
	}
	return rank[current] < rank[target]
}

func ValidateDeletionRequest(request DeletionRequest) error {
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.TargetID) == "" ||
		len(request.RequestID) > 255 || len(request.TargetID) > 255 || request.RequestedAt.IsZero() || request.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: request ID, target ID, and timestamps are required", ErrInvalidDeletionRequest)
	}
	if request.TargetType != DeletionTargetAccount && request.TargetType != DeletionTargetSubject {
		return fmt.Errorf("%w: target type %q", ErrInvalidDeletionRequest, request.TargetType)
	}
	switch request.Status {
	case DeletionRequested, DeletionRevoking, DeletionDeletingPrimary, DeletionVerifying, DeletionCompleted, DeletionFailed:
	default:
		return fmt.Errorf("%w: status %q", ErrInvalidDeletionRequest, request.Status)
	}
	return nil
}

func (workflow *DeletionWorkflow) pseudonymousDeletionTarget(targetType DeletionTargetType, targetID string) string {
	digest := hmac.New(sha256.New, workflow.pseudonymKey)
	_, _ = digest.Write([]byte(string(targetType)))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(targetID))
	return string(targetType) + ":" + hex.EncodeToString(digest.Sum(nil))
}
