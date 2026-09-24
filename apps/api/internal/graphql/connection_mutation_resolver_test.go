package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

const mutationConnectionID = "550e8400-e29b-41d4-a716-446655440101"
const mutationJobID = "550e8400-e29b-41d4-a716-446655440303"

type connectionMutationPort struct {
	publicInput       integrations.ConnectInput
	tokenInput        integrations.ConnectTokenInput
	updateInput       integrations.UpdateConnectionInput
	revokedID         string
	syncInput         integrations.ManualSyncInput
	connection        integrations.ProviderConnection
	job               integrations.SyncJob
	err               error
	revokeErr         error
	revokeCtxErr      error
	revokeHasDeadline bool
	calls             []string
}

func (p *connectionMutationPort) ConnectPublic(_ context.Context, input integrations.ConnectInput) (integrations.ProviderConnection, error) {
	p.calls = append(p.calls, "public")
	p.publicInput = input
	return p.connection, p.err
}
func (p *connectionMutationPort) ConnectToken(_ context.Context, input integrations.ConnectTokenInput) (integrations.ProviderConnection, error) {
	p.calls = append(p.calls, "token")
	p.tokenInput = input
	return p.connection, p.err
}
func (p *connectionMutationPort) Update(_ context.Context, input integrations.UpdateConnectionInput) (integrations.ProviderConnection, error) {
	p.calls = append(p.calls, "update")
	p.updateInput = input
	return p.connection, p.err
}
func (p *connectionMutationPort) Revoke(ctx context.Context, id string) (integrations.ProviderConnection, error) {
	p.calls = append(p.calls, "revoke")
	p.revokedID = id
	p.revokeCtxErr = ctx.Err()
	_, p.revokeHasDeadline = ctx.Deadline()
	revoked := p.connection
	revoked.Status = integrations.ConnectionRevoked
	if p.revokeErr != nil {
		return integrations.ProviderConnection{}, p.revokeErr
	}
	return revoked, p.err
}
func (p *connectionMutationPort) EnqueueManualSync(_ context.Context, input integrations.ManualSyncInput) (integrations.SyncJob, error) {
	p.calls = append(p.calls, "sync")
	p.syncInput = input
	return p.job, p.err
}

type ownedMutationSubjectPort struct {
	subject subjects.Subject
	owned   bool
}

func (p ownedMutationSubjectPort) GetSubject(_ context.Context, _, _ string) (subjects.Subject, error) {
	return p.subject, nil
}
func (p ownedMutationSubjectPort) OwnsSubject(_ context.Context, _, _ string) (bool, error) {
	return p.owned, nil
}

type mutationConnectionLookup struct {
	connection integrations.ProviderConnection
	err        error
}

func (p mutationConnectionLookup) Get(_ context.Context, _ string) (integrations.ProviderConnection, error) {
	return p.connection, p.err
}

type oauthMutationStarter struct {
	calls      int
	input      integrations.ConnectInput
	redirect   string
	connection integrations.ProviderConnection
	url        string
	err        error
}

type oauthCompensationReporter struct{ failures int }

func (p *oauthCompensationReporter) ReportOAuthCompensationFailure(context.Context) { p.failures++ }

func (p *oauthMutationStarter) Start(_ context.Context, input integrations.ConnectInput, redirect string) (integrations.ProviderConnection, string, error) {
	p.calls++
	p.input, p.redirect = input, redirect
	return p.connection, p.url, p.err
}

func mutationConnectionFixture() integrations.ProviderConnection {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	return integrations.ProviderConnection{ID: mutationConnectionID, SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab", AuthMethod: integrations.AuthToken, Status: integrations.ConnectionActive, CreatedAt: now, UpdatedAt: now, LastError: "internal secret"}
}

func connectionMutationContext(port *connectionMutationPort, owned bool) context.Context {
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithNodeServices(ctx, NodeServices{Subjects: ownedMutationSubjectPort{subject: subjects.Subject{ID: "subject-1", OwnerUserID: "owner", Handle: "handle"}, owned: owned}, Connections: mutationConnectionLookup{connection: mutationConnectionFixture()}})
	return ContextWithConnectionMutationServices(ctx, ConnectionMutationServices{Connections: port, Sync: port})
}

func connectionMutationAuditContext(port *connectionMutationPort, owned bool, targets *[]operations.AuditTarget) context.Context {
	return ContextWithMutationAuditTargetPublisher(connectionMutationContext(port, owned), func(target operations.AuditTarget) {
		*targets = append(*targets, target)
	})
}

func TestConnectionMutationsPublishCanonicalOwnedAuditTarget(t *testing.T) {
	want := operations.AuditTarget{Type: "provider_connection", ID: mutationConnectionID}
	global := relayid.Encode(relayid.ProviderConnection, mutationConnectionID)
	for _, tc := range []struct {
		name string
		run  func(context.Context, *connectionMutationPort) error
	}{
		{name: "connect public", run: func(ctx context.Context, _ *connectionMutationPort) error {
			_, err := resolveConnectProvider(ctx, model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodPublic})
			return err
		}},
		{name: "connect token", run: func(ctx context.Context, _ *connectionMutationPort) error {
			_, err := resolveConnectProvider(ctx, model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodToken, Token: stringPtr("private-token")})
			return err
		}},
		{name: "update", run: func(ctx context.Context, _ *connectionMutationPort) error {
			_, err := resolveUpdateProviderConnection(ctx, model.UpdateProviderConnectionInput{ID: global, Enabled: boolPtr(false)})
			return err
		}},
		{name: "revoke", run: func(ctx context.Context, _ *connectionMutationPort) error {
			_, err := resolveRevokeProviderConnection(ctx, model.RevokeProviderConnectionInput{ID: global})
			return err
		}},
		{name: "enqueue manual sync", run: func(ctx context.Context, _ *connectionMutationPort) error {
			_, err := resolveEnqueueManualSync(ctx, model.EnqueueManualSyncInput{ConnectionID: global, IdempotencyKey: "request-1234"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &connectionMutationPort{connection: mutationConnectionFixture(), job: integrations.SyncJob{ID: mutationJobID, ConnectionID: mutationConnectionID, Status: integrations.SyncJobPending, CreatedAt: mutationConnectionFixture().CreatedAt, UpdatedAt: mutationConnectionFixture().UpdatedAt}}
			var targets []operations.AuditTarget
			if err := tc.run(connectionMutationAuditContext(port, true, &targets), port); err != nil {
				t.Fatalf("mutation failed: %v", err)
			}
			if len(targets) != 1 || targets[0] != want {
				t.Fatalf("audit targets = %#v, want %#v (not opaque %q)", targets, want, global)
			}
		})
	}
}

func TestConnectionMutationsDoNotPublishUnverifiedOrFailedAuditTarget(t *testing.T) {
	global := relayid.Encode(relayid.ProviderConnection, mutationConnectionID)
	for _, tc := range []struct {
		name  string
		owned bool
		input string
		fail  bool
		run   func(context.Context, string) error
	}{
		{name: "foreign connect", owned: false, run: func(ctx context.Context, _ string) error {
			_, err := resolveConnectProvider(ctx, model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodPublic})
			return err
		}},
		{name: "invalid update ID", owned: true, input: relayid.Encode(relayid.Subject, mutationConnectionID), run: func(ctx context.Context, id string) error {
			_, err := resolveUpdateProviderConnection(ctx, model.UpdateProviderConnectionInput{ID: id})
			return err
		}},
		{name: "foreign update", owned: false, input: global, run: func(ctx context.Context, id string) error {
			_, err := resolveUpdateProviderConnection(ctx, model.UpdateProviderConnectionInput{ID: id})
			return err
		}},
		{name: "failed revoke", owned: true, input: global, fail: true, run: func(ctx context.Context, id string) error {
			_, err := resolveRevokeProviderConnection(ctx, model.RevokeProviderConnectionInput{ID: id})
			return err
		}},
		{name: "foreign manual sync", owned: false, input: global, run: func(ctx context.Context, id string) error {
			_, err := resolveEnqueueManualSync(ctx, model.EnqueueManualSyncInput{ConnectionID: id, IdempotencyKey: "request-1234"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &connectionMutationPort{connection: mutationConnectionFixture()}
			if tc.fail {
				port.revokeErr = integrations.ErrNotFound
			}
			var targets []operations.AuditTarget
			if err := tc.run(connectionMutationAuditContext(port, tc.owned, &targets), tc.input); err != nil {
				t.Fatalf("safe denial should return a typed payload: %v", err)
			}
			if len(targets) != 0 {
				t.Fatalf("unverified audit targets = %#v", targets)
			}
		})
	}
}

func TestConnectProviderRejectsNoncanonicalReturnedIdentityBeforeAudit(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	port.connection.ID = "not-a-uuid"
	var targets []operations.AuditTarget
	ctx := connectionMutationAuditContext(port, true, &targets)
	payload, err := resolveConnectProvider(ctx, model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodPublic})
	if payload != nil || err == nil || len(targets) != 0 {
		t.Fatalf("noncanonical result = (%#v, %v), audit targets=%#v", payload, err, targets)
	}
}

func TestConnectProviderRequiresSubjectOwnershipAndKeepsTokenOutOfPayload(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	input := model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodToken, Token: stringPtr("very-secret"), Scopes: []string{"read_user"}, IncludePrivate: boolPtr(true)}
	denied, err := resolveConnectProvider(connectionMutationContext(port, false), input)
	if err != nil || denied == nil || len(denied.Errors) != 1 || len(port.calls) != 0 {
		t.Fatalf("foreign connect = (%#v, %v), calls=%v", denied, err, port.calls)
	}
	got, err := resolveConnectProvider(connectionMutationContext(port, true), input)
	if err != nil || got == nil || got.Connection == nil || len(got.Errors) != 0 || got.Connection.ID != relayid.Encode(relayid.ProviderConnection, mutationConnectionID) {
		t.Fatalf("owned connect = (%#v, %v)", got, err)
	}
	if port.tokenInput.ConnectInput.SubjectID != "subject-1" || port.tokenInput.ConnectInput.ExternalAccountLogin != "handle" || port.tokenInput.Credentials.AccessToken != "very-secret" || !port.tokenInput.IncludePrivate {
		t.Fatalf("wrong token connection input: %#v", port.tokenInput)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "very-secret") || strings.Contains(string(encoded), "internal secret") {
		t.Fatalf("secret in GraphQL payload: %s", encoded)
	}
}

func TestConnectProviderOAuthNeedsTrustedCookieBoundStarter(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	input := model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodOauth2, RedirectURI: stringPtr("https://app.example.test/callback")}
	ctx := connectionMutationContext(port, true)
	_, err := resolveConnectProvider(ctx, input)
	if err == nil || len(port.calls) != 0 {
		t.Fatalf("OAuth without trusted starter = %v, calls=%v", err, port.calls)
	}
	starter := &oauthMutationStarter{connection: mutationConnectionFixture(), url: "https://gitlab.example.test/authorize"}
	starter.connection.AuthMethod = integrations.AuthOAuth2
	starter.connection.Status = integrations.ConnectionPending
	reporter := &oauthCompensationReporter{}
	ctx = ContextWithConnectionMutationServices(ctx, ConnectionMutationServices{Connections: port, Sync: port, OAuth: starter, Failures: reporter})
	_, err = resolveConnectProvider(ctx, input)
	if err == nil || starter.calls != 0 {
		t.Fatalf("OAuth without verified cookie = %v, starts=%d", err, starter.calls)
	}
	ctx = ContextWithProviderOAuthRedirects(ContextWithVerifiedOAuthCookieSession(ctx), []string{"https://app.example.test/callback"})
	got, err := resolveConnectProvider(ctx, input)
	if err != nil || got == nil || got.AuthorizationURL == nil || *got.AuthorizationURL != starter.url || starter.calls != 1 || starter.redirect != *input.RedirectURI {
		t.Fatalf("cookie-bound OAuth = (%#v, %v), starter=%#v", got, err, starter)
	}
	starter.err = errors.New("oauth state storage: hidden detail")
	failed, err := resolveConnectProvider(ctx, input)
	if failed != nil || err == nil || strings.Contains(err.Error(), "hidden detail") {
		t.Fatalf("OAuth failure must not publish pending connection or detail: (%#v, %v)", failed, err)
	}
	starter.err = nil
	starter.url = ""
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed, err = resolveConnectProvider(cancelled, input)
	if failed != nil || err == nil || port.revokedID != mutationConnectionID || port.revokeCtxErr != nil || !port.revokeHasDeadline {
		t.Fatalf("OAuth starter missing URL not compensated = (%#v, %v), revoke=%q, ctxErr=%v, deadline=%v", failed, err, port.revokedID, port.revokeCtxErr, port.revokeHasDeadline)
	}
	port.revokeErr = errors.New("sensitive revoke persistence detail")
	failed, err = resolveConnectProvider(ctx, input)
	if failed != nil || err == nil || strings.Contains(err.Error(), "sensitive revoke persistence detail") || reporter.failures != 1 {
		t.Fatalf("OAuth compensation failure not reported safely: (%#v, %v), reports=%d", failed, err, reporter.failures)
	}
}

func TestConnectProviderRejectsUnsupportedFieldsBeforeServiceCall(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	base := model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodToken}
	bad, err := resolveConnectProvider(connectionMutationContext(port, true), base)
	if err != nil || bad == nil || len(bad.Errors) != 1 || bad.Errors[0].Code != "BAD_USER_INPUT" || len(port.calls) != 0 {
		t.Fatalf("missing token = (%#v, %v), calls=%v", bad, err, port.calls)
	}
	base.AuthMethod = model.ProviderAuthMethodPublic
	base.Token = stringPtr("secret")
	bad, err = resolveConnectProvider(connectionMutationContext(port, true), base)
	if err != nil || bad == nil || len(bad.Errors) != 1 || len(port.calls) != 0 {
		t.Fatalf("public with token = (%#v, %v), calls=%v", bad, err, port.calls)
	}
	base.AuthMethod = model.ProviderAuthMethodOauth2
	base.Token = nil
	base.RedirectURI = stringPtr("https://evil.example.test")
	starter := &oauthMutationStarter{connection: mutationConnectionFixture(), url: "https://gitlab.example.test/authorize"}
	ctx := ContextWithVerifiedOAuthCookieSession(ContextWithConnectionMutationServices(connectionMutationContext(port, true), ConnectionMutationServices{Connections: port, Sync: port, OAuth: starter, Failures: &oauthCompensationReporter{}}))
	bad, err = resolveConnectProvider(ctx, base)
	if err != nil || bad == nil || len(bad.Errors) != 1 || bad.Errors[0].Code != "BAD_USER_INPUT" || starter.calls != 0 {
		t.Fatalf("unlisted OAuth redirect = (%#v, %v), starts=%d", bad, err, starter.calls)
	}
}

func TestOAuthStartRequiresCompensationReporterBeforeSideEffect(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	starter := &oauthMutationStarter{connection: mutationConnectionFixture(), url: "https://gitlab.example.test/authorize"}
	ctx := ContextWithVerifiedOAuthCookieSession(ContextWithConnectionMutationServices(connectionMutationContext(port, true), ConnectionMutationServices{Connections: port, Sync: port, OAuth: starter}))
	input := model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodOauth2}
	got, err := resolveConnectProvider(ctx, input)
	if got != nil || err == nil || starter.calls != 0 {
		t.Fatalf("OAuth without failure reporter = (%#v, %v), starts=%d", got, err, starter.calls)
	}
}

func TestOAuthStartDoesNotRevokeOrProjectMismatchedPendingConnection(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	starter := &oauthMutationStarter{connection: mutationConnectionFixture()}
	starter.connection.AuthMethod = integrations.AuthOAuth2
	starter.connection.Status = integrations.ConnectionPending
	starter.connection.ProviderID = "github"
	reporter := &oauthCompensationReporter{}
	ctx := ContextWithVerifiedOAuthCookieSession(ContextWithConnectionMutationServices(connectionMutationContext(port, true), ConnectionMutationServices{Connections: port, Sync: port, OAuth: starter, Failures: reporter}))
	input := model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodOauth2}
	got, err := resolveConnectProvider(ctx, input)
	if got != nil || err == nil || port.revokedID != "" || reporter.failures != 1 {
		t.Fatalf("mismatched empty-URL connection = (%#v, %v), revoked=%q, reports=%d", got, err, port.revokedID, reporter.failures)
	}
	starter.url = "https://gitlab.example.test/authorize"
	got, err = resolveConnectProvider(ctx, input)
	if got != nil || err == nil || port.revokedID != "" || reporter.failures != 2 {
		t.Fatalf("mismatched URL-bearing connection = (%#v, %v), revoked=%q, reports=%d", got, err, port.revokedID, reporter.failures)
	}
}

func TestConnectionUpdateAndRevokeRejectForeignOrWrongKindBeforeWrite(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	wrong := relayid.Encode(relayid.Subject, mutationConnectionID)
	bad, err := resolveUpdateProviderConnection(connectionMutationContext(port, true), model.UpdateProviderConnectionInput{ID: wrong, Enabled: boolPtr(false)})
	if err != nil || bad == nil || len(bad.Errors) != 1 || len(port.calls) != 0 {
		t.Fatalf("wrong kind = (%#v, %v), calls=%v", bad, err, port.calls)
	}
	id := relayid.Encode(relayid.ProviderConnection, mutationConnectionID)
	denied, err := resolveUpdateProviderConnection(connectionMutationContext(port, false), model.UpdateProviderConnectionInput{ID: id, Enabled: boolPtr(false)})
	if err != nil || denied == nil || len(denied.Errors) != 1 || len(port.calls) != 0 {
		t.Fatalf("foreign update = (%#v, %v), calls=%v", denied, err, port.calls)
	}
	updated, err := resolveUpdateProviderConnection(connectionMutationContext(port, true), model.UpdateProviderConnectionInput{ID: id, Enabled: boolPtr(false), Scopes: []string{}})
	if err != nil || updated == nil || updated.Connection == nil || port.updateInput.ID != mutationConnectionID || port.updateInput.Scopes == nil || len(*port.updateInput.Scopes) != 0 {
		t.Fatalf("update = (%#v, %v), input=%#v", updated, err, port.updateInput)
	}
	revoked, err := resolveRevokeProviderConnection(connectionMutationContext(port, true), model.RevokeProviderConnectionInput{ID: id})
	if err != nil || revoked == nil || revoked.RevokedConnectionID == nil || *revoked.RevokedConnectionID != id || port.revokedID != mutationConnectionID {
		t.Fatalf("revoke = (%#v, %v), id=%q", revoked, err, port.revokedID)
	}
}

func TestConnectionUpdateNeverProjectsAnotherSubjectsReturnedRow(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	ctx := connectionMutationContext(port, true)
	nodes, _ := ctx.Value(nodeServicesContextKey{}).(NodeServices)
	owned := mutationConnectionFixture()
	nodes.Connections = mutationConnectionLookup{connection: owned}
	ctx = ContextWithNodeServices(ctx, nodes)
	port.connection.SubjectID = "other-subject"
	got, err := resolveUpdateProviderConnection(ctx, model.UpdateProviderConnectionInput{ID: relayid.Encode(relayid.ProviderConnection, mutationConnectionID), Enabled: boolPtr(false)})
	if got != nil || err == nil {
		t.Fatalf("cross-subject return was projected: (%#v, %v)", got, err)
	}
}

func TestEnqueueManualSyncAuthorizesAndProjectsOnlySafeJobMetadata(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture(), job: integrations.SyncJob{ID: mutationJobID, ConnectionID: mutationConnectionID, Status: integrations.SyncJobPending, Attempt: 1, CreatedAt: mutationConnectionFixture().CreatedAt, UpdatedAt: mutationConnectionFixture().UpdatedAt, LastError: "provider secret"}}
	id := relayid.Encode(relayid.ProviderConnection, mutationConnectionID)
	input := model.EnqueueManualSyncInput{ConnectionID: id, IdempotencyKey: "request-1234", From: datePtr("2026-09-01"), Force: boolPtr(true), FailurePolicy: fetchPolicyPtr(model.FetchFailurePolicyPurge)}
	denied, err := resolveEnqueueManualSync(connectionMutationContext(port, false), input)
	if err != nil || denied == nil || len(denied.Errors) != 1 || len(port.calls) != 0 {
		t.Fatalf("foreign enqueue = (%#v, %v), calls=%v", denied, err, port.calls)
	}
	got, err := resolveEnqueueManualSync(connectionMutationContext(port, true), input)
	if err != nil || got == nil || got.Job == nil || got.Job.ID != relayid.Encode(relayid.SyncJob, mutationJobID) || port.syncInput.ConnectionID != mutationConnectionID || port.syncInput.IdempotencyKey != "request-1234" || port.syncInput.From == nil || *port.syncInput.From != activity.Date("2026-09-01") || port.syncInput.FailurePolicy != activity.FetchFailurePurge {
		t.Fatalf("enqueue = (%#v, %v), input=%#v", got, err, port.syncInput)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "provider secret") || strings.Contains(string(encoded), "request-1234") {
		t.Fatalf("sensitive data in job payload: %s", encoded)
	}
}

func TestEnqueueManualSyncRejectsInvalidKeyAndMapsReplayConflict(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	input := model.EnqueueManualSyncInput{ConnectionID: relayid.Encode(relayid.ProviderConnection, mutationConnectionID), IdempotencyKey: "short"}
	bad, err := resolveEnqueueManualSync(connectionMutationContext(port, true), input)
	if err != nil || bad == nil || len(bad.Errors) != 1 || bad.Errors[0].Code != "BAD_USER_INPUT" || len(port.calls) != 0 {
		t.Fatalf("invalid key = (%#v, %v), calls=%v", bad, err, port.calls)
	}
	input.IdempotencyKey = "replayed-request"
	port.err = integrations.ErrIdempotencyConflict
	conflict, err := resolveEnqueueManualSync(connectionMutationContext(port, true), input)
	if err != nil || conflict == nil || len(conflict.Errors) != 1 || conflict.Errors[0].Code != "CONFLICT" || conflict.Job != nil {
		t.Fatalf("replay conflict = (%#v, %v)", conflict, err)
	}
}

func TestConnectionMutationErrorsAreTypedAndRedacted(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture(), err: integrations.ErrEmptyAccessToken}
	input := model.ConnectProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), ProviderID: "gitlab", AuthMethod: model.ProviderAuthMethodToken, Token: stringPtr("x")}
	bad, err := resolveConnectProvider(connectionMutationContext(port, true), input)
	if err != nil || bad == nil || len(bad.Errors) != 1 || bad.Errors[0].Code != "BAD_USER_INPUT" {
		t.Fatalf("validation = (%#v, %v)", bad, err)
	}
	port.err = errors.New("private database detail")
	_, err = resolveConnectProvider(connectionMutationContext(port, true), input)
	if err == nil || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("internal error exposed: %v", err)
	}
}

func TestConnectionMutationGQLGenClientDoesNotEchoCredentials(t *testing.T) {
	port := &connectionMutationPort{connection: mutationConnectionFixture()}
	graph := viewerGraphQLClient(connectionMutationContext(port, true), NodeServices{Subjects: ownedMutationSubjectPort{subject: subjects.Subject{ID: "subject-1", OwnerUserID: "owner", Handle: "handle"}, owned: true}, Connections: mutationConnectionLookup{connection: mutationConnectionFixture()}})
	response, err := graph.RawPost(`mutation($subject: ID!, $token: String!) { connectProvider(input: {subjectID: $subject, providerID: "gitlab", authMethod: TOKEN, token: $token}) { errors { code } connection { id providerID status } } }`, client.Var("subject", relayid.Encode(relayid.Subject, "subject-1")), client.Var("token", "top-secret"))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("GraphQL mutation = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	if strings.Contains(string(data), "top-secret") || !strings.Contains(string(data), "providerID") {
		t.Fatalf("GraphQL payload = %s", data)
	}
}

func stringPtr(v string) *string                                          { return &v }
func boolPtr(v bool) *bool                                                { return &v }
func datePtr(v string) *scalar.Date                                       { date := scalar.Date(v); return &date }
func fetchPolicyPtr(v model.FetchFailurePolicy) *model.FetchFailurePolicy { return &v }
