package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type subjectMutationPort struct {
	updateUserInput           subjects.UpdateUserSettingsInput
	updateUserActor           string
	createInput               subjects.CreateSubjectInput
	createActor               string
	updateInput               subjects.UpdateSubjectInput
	updateActor, updateID     string
	settingsInput             subjects.UpdateSubjectSettingsInput
	settingsActor, settingsID string
	lookupActor, lookupID     string
	deleteActor, deleteID     string
	userSettings              subjects.UserSettings
	subjectSettings           subjects.SubjectSettings
	subject                   subjects.Subject
	err                       error
	calls                     []string
}

func (p *subjectMutationPort) UpdateUserSettings(_ context.Context, actor string, input subjects.UpdateUserSettingsInput) (subjects.UserSettings, error) {
	p.calls = append(p.calls, "update-user")
	p.updateUserActor, p.updateUserInput = actor, input
	return p.userSettings, p.err
}

func (p *subjectMutationPort) CreateSubject(_ context.Context, actor string, input subjects.CreateSubjectInput) (subjects.Subject, error) {
	p.calls = append(p.calls, "create")
	p.createActor, p.createInput = actor, input
	return p.subject, p.err
}

func (p *subjectMutationPort) UpdateSubject(_ context.Context, actor, id string, input subjects.UpdateSubjectInput) (subjects.Subject, error) {
	p.calls = append(p.calls, "update")
	p.updateActor, p.updateID, p.updateInput = actor, id, input
	return p.subject, p.err
}

func (p *subjectMutationPort) UpdateSubjectSettings(_ context.Context, actor, id string, input subjects.UpdateSubjectSettingsInput) (subjects.SubjectSettings, error) {
	p.calls = append(p.calls, "update-settings")
	p.settingsActor, p.settingsID, p.settingsInput = actor, id, input
	return p.subjectSettings, p.err
}

func (p *subjectMutationPort) AuthorizeSubjectDeletion(_ context.Context, actor, id string) (subjects.Subject, error) {
	p.calls = append(p.calls, "authorize-delete")
	p.deleteActor, p.deleteID = actor, id
	return p.subject, p.err
}

func (p *subjectMutationPort) GetSubject(_ context.Context, actor, id string) (subjects.Subject, error) {
	p.calls = append(p.calls, "get")
	p.lookupActor, p.lookupID = actor, id
	return p.subject, p.err
}

type subjectDeletionPort struct {
	requestID, targetID string
	targetType          operations.DeletionTargetType
	result              operations.DeletionRequest
	err                 error
	calls               int
}

func (p *subjectDeletionPort) Request(_ context.Context, requestID string, targetType operations.DeletionTargetType, targetID string) (operations.DeletionRequest, error) {
	p.calls++
	p.requestID, p.targetType, p.targetID = requestID, targetType, targetID
	result := p.result
	if result.RequestID == "" {
		result.RequestID = requestID
	}
	return result, p.err
}

func mutationSubjectFixture() subjects.Subject {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	name := "Visible"
	return subjects.Subject{ID: "subject-1", OwnerUserID: "owner", Handle: "visible", DisplayName: &name, Timezone: "UTC", IsPublic: true, CreatedAt: now, UpdatedAt: now}
}

func mutationContext(port *subjectMutationPort, deletion *subjectDeletionPort) context.Context {
	return ContextWithSubjectMutationServices(ContextWithVerifiedViewer(context.Background(), "owner"), SubjectMutationServices{Subjects: port, Deletions: deletion})
}

func TestSubjectMutationsRequireVerifiedViewerBeforeAnyPortCall(t *testing.T) {
	for _, ctx := range []context.Context{context.Background(), ContextWithFailedAuthentication(context.Background())} {
		port := &subjectMutationPort{}
		deletion := &subjectDeletionPort{}
		ctx = ContextWithSubjectMutationServices(ctx, SubjectMutationServices{Subjects: port, Deletions: deletion})
		if _, err := resolveCreateSubject(ctx, model.CreateSubjectInput{Handle: "visible", Timezone: "UTC"}); !errors.Is(err, errNodeAuthentication) {
			t.Fatalf("anonymous create error = %v", err)
		}
		if _, err := resolveRequestSubjectDeletion(ctx, model.RequestSubjectDeletionInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1")}); !errors.Is(err, errNodeAuthentication) {
			t.Fatalf("anonymous deletion error = %v", err)
		}
		if len(port.calls) != 0 || deletion.calls != 0 {
			t.Fatalf("ports called before authentication: %v, %d", port.calls, deletion.calls)
		}
	}
}

func TestUpdateUserSettingsForwardsTrustedActorAndProjectsSafeValues(t *testing.T) {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	port := &subjectMutationPort{userSettings: subjects.UserSettings{Locale: "ko-KR", Timezone: "Asia/Seoul", Theme: subjects.ThemeDark, UpdatedAt: now}}
	locale := "ko-KR"
	payload, err := resolveUpdateUserSettings(mutationContext(port, nil), model.UpdateUserSettingsInput{Locale: &locale})
	if err != nil || payload == nil || len(payload.Errors) != 0 || payload.Settings == nil || payload.Settings.Locale != "ko-KR" || string(payload.Settings.Theme) != "DARK" {
		t.Fatalf("settings payload = (%+v, %v)", payload, err)
	}
	if port.updateUserActor != "owner" || port.updateUserInput.Locale == nil || *port.updateUserInput.Locale != locale {
		t.Fatalf("actor/input = %q, %+v", port.updateUserActor, port.updateUserInput)
	}
}

func TestUpdateUserSettingsTranslatesGraphQLEnumToDomainValue(t *testing.T) {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	port := &subjectMutationPort{userSettings: subjects.UserSettings{Locale: "en-US", Timezone: "UTC", Theme: subjects.ThemeDark, UpdatedAt: now}}
	theme := model.HeatmapThemeDark
	_, err := resolveUpdateUserSettings(mutationContext(port, nil), model.UpdateUserSettingsInput{Theme: &theme})
	if err != nil || port.updateUserInput.Theme == nil || *port.updateUserInput.Theme != subjects.ThemeDark {
		t.Fatalf("theme forwarding = (%+v, %v)", port.updateUserInput, err)
	}
}

func TestUpdateSubjectSettingsTranslatesEnumsAndReturnsAffectedNode(t *testing.T) {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	port := &subjectMutationPort{
		subject: mutationSubjectFixture(),
		subjectSettings: subjects.SubjectSettings{SubjectID: "subject-1", Timezone: "UTC", IsPublic: false, DefaultTheme: subjects.ThemeGitHubDark,
			WeekStart: subjects.WeekStartMonday, SyncEnabled: true, SyncIntervalMinutes: 60, FailurePolicy: subjects.FailurePurge, UpdatedAt: now},
	}
	theme, week, policy := model.HeatmapThemeGithubDark, model.WeekStartMonday, model.FetchFailurePolicyPurge
	zone := scalar.TimeZone("Asia/Seoul")
	payload, err := resolveUpdateSubjectSettings(mutationContext(port, nil), model.UpdateSubjectSettingsInput{
		SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Timezone: &zone,
		DefaultTheme: &theme, WeekStart: &week, FailurePolicy: &policy,
	})
	if err != nil || payload == nil || len(payload.Errors) != 0 || payload.Settings == nil || payload.Subject == nil || payload.Subject.ID != relayid.Encode(relayid.Subject, "subject-1") {
		t.Fatalf("settings update = (%+v, %v)", payload, err)
	}
	if port.settingsActor != "owner" || port.settingsID != "subject-1" || port.lookupActor != "owner" || port.lookupID != "subject-1" ||
		port.settingsInput.DefaultTheme == nil || *port.settingsInput.DefaultTheme != subjects.ThemeGitHubDark ||
		port.settingsInput.WeekStart == nil || *port.settingsInput.WeekStart != subjects.WeekStartMonday ||
		port.settingsInput.FailurePolicy == nil || *port.settingsInput.FailurePolicy != subjects.FailurePurge ||
		port.settingsInput.Timezone == nil || *port.settingsInput.Timezone != "Asia/Seoul" {
		t.Fatalf("settings forwarding = %+v, lookup=(%q,%q)", port.settingsInput, port.lookupActor, port.lookupID)
	}
}

func TestCreateSubjectReturnsNormalizedNode(t *testing.T) {
	port := &subjectMutationPort{subject: mutationSubjectFixture()}
	payload, err := resolveCreateSubject(mutationContext(port, nil), model.CreateSubjectInput{Handle: "visible", Timezone: "UTC"})
	if err != nil || payload == nil || len(payload.Errors) != 0 || payload.Subject == nil || payload.Subject.ID != relayid.Encode(relayid.Subject, "subject-1") || payload.Subject.Handle != "visible" {
		t.Fatalf("create payload = (%+v, %v)", payload, err)
	}
	if port.createActor != "owner" || port.createInput.Handle != "visible" {
		t.Fatalf("create actor/input = %q, %+v", port.createActor, port.createInput)
	}
}

func TestGraphQLCreateSubjectReturnsRelayNormalizableNode(t *testing.T) {
	port := &subjectMutationPort{subject: mutationSubjectFixture()}
	graph := viewerGraphQLClient(mutationContext(port, nil), NodeServices{})
	response, err := graph.RawPost(`mutation CreateSubject($handle: String!) { createSubject(input: {handle: $handle, timezone: "UTC"}) { errors { code } subject { id handle displayName } } }`, client.Var("handle", "visible"))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("GraphQL create = (%+v, %v)", response, err)
	}
	encoded, _ := json.Marshal(response.Data)
	if !strings.Contains(string(encoded), relayid.Encode(relayid.Subject, "subject-1")) || !strings.Contains(string(encoded), `"handle":"visible"`) || !strings.Contains(string(encoded), `"errors":[]`) {
		t.Fatalf("GraphQL create payload = %s", encoded)
	}
}

func TestUpdateSubjectClearDisplayNameIsExplicitAndExclusive(t *testing.T) {
	port := &subjectMutationPort{subject: mutationSubjectFixture()}
	clear := true
	globalID := relayid.Encode(relayid.Subject, "subject-1")
	payload, err := resolveUpdateSubject(mutationContext(port, nil), model.UpdateSubjectInput{ID: globalID, ClearDisplayName: &clear})
	if err != nil || payload == nil || len(payload.Errors) != 0 || port.updateActor != "owner" || port.updateID != "subject-1" || !port.updateInput.DisplayNameSet || port.updateInput.DisplayName != nil {
		t.Fatalf("clear forwarding = (%+v, %v), %+v", payload, err, port)
	}
	name := "conflicting"
	port.calls = nil
	payload, err = resolveUpdateSubject(mutationContext(port, nil), model.UpdateSubjectInput{ID: globalID, DisplayName: &name, ClearDisplayName: &clear})
	if err != nil || payload == nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "BAD_USER_INPUT" || len(port.calls) != 0 {
		t.Fatalf("conflicting clear = (%+v, %v), calls=%v", payload, err, port.calls)
	}
}

func TestSubjectMutationRejectsWrongKindBeforeServiceCall(t *testing.T) {
	port := &subjectMutationPort{}
	payload, err := resolveUpdateSubject(mutationContext(port, nil), model.UpdateSubjectInput{ID: relayid.Encode(relayid.Session, "session-1")})
	if err != nil || payload == nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "BAD_USER_INPUT" || len(port.calls) != 0 {
		t.Fatalf("wrong-kind result = (%+v, %v), calls=%v", payload, err, port.calls)
	}
}

func TestSubjectMutationHidesDeniedAndMissingTargetIdentically(t *testing.T) {
	globalID := relayid.Encode(relayid.Subject, "subject-1")
	var serialized []string
	for _, serviceErr := range []error{subjects.ErrForbidden, subjects.ErrNotFound} {
		port := &subjectMutationPort{err: serviceErr}
		payload, err := resolveUpdateSubject(mutationContext(port, nil), model.UpdateSubjectInput{ID: globalID, Handle: stringPointer("renamed")})
		if err != nil || payload == nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "NOT_FOUND" || payload.Subject != nil {
			t.Fatalf("denied/missing result = (%+v, %v)", payload, err)
		}
		encoded, _ := json.Marshal(payload)
		serialized = append(serialized, string(encoded))
	}
	if serialized[0] != serialized[1] {
		t.Fatalf("denied/missing response differs: %q vs %q", serialized[0], serialized[1])
	}
}

func TestSubjectMutationRedactsInternalServiceError(t *testing.T) {
	port := &subjectMutationPort{err: errors.New("sensitive database credentials")}
	payload, err := resolveCreateSubject(mutationContext(port, nil), model.CreateSubjectInput{Handle: "visible", Timezone: "UTC"})
	if payload != nil || !errors.Is(err, errNodeLookup) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("internal failure = (%+v, %v)", payload, err)
	}
}

func TestUpdateSubjectSettingsRejectsMismatchedReturnedOwner(t *testing.T) {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	subject := mutationSubjectFixture()
	subject.OwnerUserID = "other-user"
	port := &subjectMutationPort{subject: subject, subjectSettings: subjects.SubjectSettings{SubjectID: "subject-1", Timezone: "UTC", IsPublic: true,
		DefaultTheme: subjects.ThemeSystem, WeekStart: subjects.WeekStartSunday, SyncEnabled: true, SyncIntervalMinutes: 60,
		FailurePolicy: subjects.FailureKeepStale, UpdatedAt: now}}
	visible := true
	payload, err := resolveUpdateSubjectSettings(mutationContext(port, nil), model.UpdateSubjectSettingsInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), IsPublic: &visible})
	if payload != nil || !errors.Is(err, errNodeLookup) {
		t.Fatalf("mismatched owner = (%+v, %v)", payload, err)
	}
}

func stringPointer(value string) *string { return &value }

func TestRequestSubjectDeletionAuthorizesBeforeEnqueueAndRevalidatesResult(t *testing.T) {
	port := &subjectMutationPort{subject: mutationSubjectFixture()}
	deletion := &subjectDeletionPort{result: operations.DeletionRequest{TargetType: operations.DeletionTargetSubject, TargetID: "subject-1", Status: operations.DeletionRequested}}
	ctx := mutationContext(port, deletion)
	payload, err := resolveRequestSubjectDeletion(ctx, model.RequestSubjectDeletionInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1")})
	if err != nil || payload == nil || len(payload.Errors) != 0 || payload.Request == nil || payload.Request.RequestID == "" || payload.Request.Status != string(operations.DeletionRequested) {
		t.Fatalf("deletion result = (%+v, %v)", payload, err)
	}
	if len(port.calls) != 1 || port.calls[0] != "authorize-delete" || port.deleteActor != "owner" || port.deleteID != "subject-1" || deletion.calls != 1 || deletion.requestID == "" || deletion.targetType != operations.DeletionTargetSubject || deletion.targetID != "subject-1" {
		t.Fatalf("deletion flow: calls=%v auth=(%q,%q), queue=%+v", port.calls, port.deleteActor, port.deleteID, deletion)
	}
	deletion.result.TargetID = "other-subject"
	payload, err = resolveRequestSubjectDeletion(ctx, model.RequestSubjectDeletionInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1")})
	if payload != nil || !errors.Is(err, errNodeLookup) {
		t.Fatalf("mismatched durable request = (%+v, %v)", payload, err)
	}
}
