package apihttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type deletionWorkflowStub struct {
	requestedID     string
	targetType      operations.DeletionTargetType
	targetID        string
	requestErr      error
	returnTargetID  string
	returnRequestID string
}

func (workflow *deletionWorkflowStub) Request(_ context.Context, requestID string, targetType operations.DeletionTargetType, targetID string) (operations.DeletionRequest, error) {
	workflow.requestedID = requestID
	workflow.targetType = targetType
	workflow.targetID = targetID
	if workflow.requestErr != nil {
		return operations.DeletionRequest{}, workflow.requestErr
	}
	returnedID := requestID
	if workflow.returnRequestID != "" {
		returnedID = workflow.returnRequestID
	}
	returnedTarget := targetID
	if workflow.returnTargetID != "" {
		returnedTarget = workflow.returnTargetID
	}
	return operations.DeletionRequest{
		RequestID: returnedID, TargetType: targetType, TargetID: returnedTarget, Status: operations.DeletionRequested,
	}, nil
}

func TestSubjectDeleteUsesOwnedStableIDAndDurableWorkflow(t *testing.T) {
	t.Parallel()
	service, repository := deletionSubjectService(t)
	workflow := &deletionWorkflowStub{}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, Subjects: service, SubjectDeletions: workflow,
	})
	request := httptest.NewRequest(http.MethodDelete, "/v1/subjects/delete-me", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if workflow.requestedID == "" || workflow.requestedID == "uniform-http-request" || workflow.targetType != operations.DeletionTargetSubject || workflow.targetID != "subject-stable-id" {
		t.Fatalf("workflow calls = %#v", workflow)
	}
	var payload struct {
		RequestID string                    `json:"requestId"`
		Status    operations.DeletionStatus `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.RequestID != workflow.requestedID || payload.Status != operations.DeletionRequested {
		t.Fatalf("deletion response = %#v, error=%v", payload, err)
	}
	// The transport must not call the subjects repository's direct delete path.
	if subject, err := repository.GetSubject(context.Background(), "subject-stable-id"); err != nil || subject.Handle != "delete-me" {
		t.Fatalf("subject was directly deleted: %#v, %v", subject, err)
	}
}

func TestSubjectDeleteRejectsCrossTargetRequestCollision(t *testing.T) {
	t.Parallel()
	service, repository := deletionSubjectService(t)
	workflow := &deletionWorkflowStub{returnTargetID: "other-users-subject"}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, Subjects: service, SubjectDeletions: workflow,
	})
	request := httptest.NewRequest(http.MethodDelete, "/v1/subjects/delete-me", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if response.Code != http.StatusConflict || problem.Status != http.StatusConflict || problem.Code != "deletion_request_conflict" {
		t.Fatalf("response = %d %#v: %s", response.Code, problem, response.Body.String())
	}
	if _, err := repository.GetSubject(context.Background(), "subject-stable-id"); err != nil {
		t.Fatalf("subject was deleted after collision: %v", err)
	}
}

func TestSubjectDeleteReturnsExistingSameTargetRequest(t *testing.T) {
	t.Parallel()
	service, _ := deletionSubjectService(t)
	workflow := &deletionWorkflowStub{returnRequestID: "existing-server-request"}
	router := apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}, Subjects: service, SubjectDeletions: workflow})
	request := httptest.NewRequest(http.MethodDelete, "/v1/subjects/delete-me", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"requestId":"existing-server-request"`) {
		t.Fatalf("response = %d: %s", response.Code, response.Body.String())
	}
}

func TestSubjectDeleteMapsActiveLegalHoldToStableConflict(t *testing.T) {
	t.Parallel()
	service, repository := deletionSubjectService(t)
	workflow := &deletionWorkflowStub{requestErr: operations.ErrLegalHoldActive}
	router := apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}, Subjects: service, SubjectDeletions: workflow})
	request := httptest.NewRequest(http.MethodDelete, "/v1/subjects/delete-me", nil)
	request.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"legal_hold_active"`) {
		t.Fatalf("response = %d: %s", response.Code, response.Body.String())
	}
	if _, err := repository.GetSubject(context.Background(), "subject-stable-id"); err != nil {
		t.Fatalf("held subject was deleted: %v", err)
	}
}

func deletionSubjectService(t *testing.T) (*subjects.Service, *subjects.MemoryRepository) {
	t.Helper()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	repository := subjects.NewMemoryRepository()
	service, err := subjects.NewService(repository, subjects.Config{
		Now: func() time.Time { return now }, NewID: func() (string, error) { return "subject-stable-id", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	user := subjects.User{
		ID: "user-1", PrimaryEmail: "user@example.com", Status: subjects.UserStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := service.ProvisionUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSubject(context.Background(), user.ID, subjects.CreateSubjectInput{Handle: "delete-me", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	return service, repository
}

type magicLinkRequestErrorAuth struct {
	fakeAuth
	err error
}

func (service magicLinkRequestErrorAuth) RequestMagicLink(context.Context, string, string) error {
	return service.err
}

func TestMagicLinkRequestDoesNotExposeSMTPRecipientOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "accepted recipient"},
		{name: "rejected recipient", err: errors.New("SMTP 550 mailbox does not exist: recipient-secret@example.test")},
	}
	var snapshot string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := apihttp.NewRouter(apihttp.Dependencies{Auth: magicLinkRequestErrorAuth{err: test.err}})
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(`{"email":"person@example.test"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Request-ID", "uniform-magic-link-request")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			if snapshot == "" {
				snapshot = response.Body.String()
			} else if response.Body.String() != snapshot {
				t.Fatalf("SMTP outcome changed response: %q vs %q", snapshot, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "mailbox") || strings.Contains(response.Body.String(), "recipient") {
				t.Fatalf("SMTP detail leaked: %s", response.Body.String())
			}
		})
	}
}

func TestMagicLinkRequestReturnsBeforeSMTPDelivery(t *testing.T) {
	issuer := &httpDeliveryIssuer{}
	mailer := &slowMagicLinkMailer{delay: time.Second}
	service, err := auth.NewService(auth.Dependencies{
		Repository: auth.NewMemoryStore(), Deliveries: issuer, Mailer: mailer,
	}, auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	router := apihttp.NewRouter(apihttp.Dependencies{Auth: service})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/request", strings.NewReader(`{"email":"person@example.test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(response, request)
	elapsed := time.Since(started)
	if response.Code != http.StatusAccepted || elapsed >= 500*time.Millisecond {
		t.Fatalf("response = %d in %s: %s", response.Code, elapsed, response.Body.String())
	}
	if mailer.calls != 0 || issuer.intent.RecipientEmail != "person@example.test" {
		t.Fatalf("request path mail calls=%d intent=%#v", mailer.calls, issuer.intent)
	}
}

type httpDeliveryIssuer struct{ intent auth.MagicLinkDelivery }

func (issuer *httpDeliveryIssuer) SaveMagicLinkDeliveryIntent(_ context.Context, intent auth.MagicLinkDelivery) error {
	issuer.intent = intent
	return nil
}

type slowMagicLinkMailer struct {
	delay time.Duration
	calls int
}

func (mailer *slowMagicLinkMailer) SendMagicLink(context.Context, auth.MagicLinkMail) error {
	mailer.calls++
	time.Sleep(mailer.delay)
	return errors.New("SMTP recipient response")
}
