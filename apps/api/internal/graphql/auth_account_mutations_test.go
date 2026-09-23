package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
)

const accountSessionID = "158f21a1-3fa1-4706-a2a6-c99a1caf40f0"

type accountAuthPort struct {
	requestErr error
	lookupErr  error
	revokeErr  error
	otherErr   error
	session    auth.Session
	requests   []struct{ email, redirect string }
	lookups    []struct{ actor, id string }
	revokes    []struct{ actor, id string }
	others     []struct{ actor, id string }
}

func (port *accountAuthPort) RequestMagicLink(_ context.Context, email, redirect string) error {
	port.requests = append(port.requests, struct{ email, redirect string }{email, redirect})
	return port.requestErr
}

func (port *accountAuthPort) GetSessionByID(_ context.Context, actor, id string) (auth.Session, error) {
	port.lookups = append(port.lookups, struct{ actor, id string }{actor, id})
	return port.session, port.lookupErr
}

func (port *accountAuthPort) RevokeSessionByID(_ context.Context, actor, id string) error {
	port.revokes = append(port.revokes, struct{ actor, id string }{actor, id})
	return port.revokeErr
}

func (port *accountAuthPort) RevokeOtherSessionsExceptID(_ context.Context, actor, id string) error {
	port.others = append(port.others, struct{ actor, id string }{actor, id})
	return port.otherErr
}

type accountCookieSink struct {
	clearErr error
	clears   int
	sets     int
}

type accountFailureReporter struct {
	magicLinkFailures  int
	compensationErrors int
}

func (reporter *accountFailureReporter) ReportMagicLinkRequestFailure(context.Context) {
	reporter.magicLinkFailures++
}

func (reporter *accountFailureReporter) ReportSessionCompensationFailure(context.Context) {
	reporter.compensationErrors++
}

func (sink *accountCookieSink) SetSessionCookie(string, time.Time) error {
	sink.sets++
	return nil
}

func (sink *accountCookieSink) ClearSessionCookie() error {
	sink.clears++
	return sink.clearErr
}

func accountSession() auth.Session {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	return auth.Session{ID: accountSessionID, UserID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour), TokenHash: auth.Digest{1}}
}

func accountContext(port *accountAuthPort) context.Context {
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithVerifiedSessionID(ctx, accountSessionID)
	return ContextWithAuthAccountService(ctx, port)
}

func TestRequestMagicLinkAcceptedResponseDoesNotEnumerateAccounts(t *testing.T) {
	for _, serviceErr := range []error{nil, auth.ErrNotFound, errors.New("private SMTP recipient detail")} {
		port := &accountAuthPort{requestErr: serviceErr}
		reporter := &accountFailureReporter{}
		ctx := ContextWithAuthMutationFailureReporter(ContextWithAuthAccountService(context.Background(), port), reporter)
		payload, err := resolveRequestMagicLink(ctx, "Person@Example.test", nil)
		if err != nil || payload == nil || !payload.Accepted || len(payload.Errors) != 0 {
			t.Fatalf("service error %v produced distinguishable response: %+v, %v", serviceErr, payload, err)
		}
		if len(port.requests) != 1 || port.requests[0].email != "person@example.test" || port.requests[0].redirect != "" {
			t.Fatalf("request forwarding = %+v", port.requests)
		}
		if (serviceErr == nil && reporter.magicLinkFailures != 0) || (serviceErr != nil && reporter.magicLinkFailures != 1) {
			t.Fatalf("failure events = %d for error %v", reporter.magicLinkFailures, serviceErr)
		}
	}
}

func TestRequestMagicLinkRequiresSafeFailureReporterBeforeDelivery(t *testing.T) {
	port := &accountAuthPort{}
	ctx := ContextWithAuthAccountService(context.Background(), port)
	payload, err := resolveRequestMagicLink(ctx, "person@example.test", nil)
	if payload != nil || !errors.Is(err, errNodeLookup) || len(port.requests) != 0 {
		t.Fatalf("missing reporter = (%+v, %v), delivery=%+v", payload, err, port.requests)
	}
}

func TestRequestMagicLinkReportsFailureWithoutExposingRecipientOrServiceDetail(t *testing.T) {
	port := &accountAuthPort{requestErr: errors.New("private recipient and SMTP detail")}
	reporter := &accountFailureReporter{}
	ctx := ContextWithAuthMutationFailureReporter(ContextWithAuthAccountService(context.Background(), port), reporter)
	payload, err := resolveRequestMagicLink(ctx, "person@example.test", nil)
	if err != nil || payload == nil || !payload.Accepted || len(payload.Errors) != 0 || reporter.magicLinkFailures != 1 {
		t.Fatalf("delivery failure = (%+v, %v), failure events=%d", payload, err, reporter.magicLinkFailures)
	}
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "person@example.test") || strings.Contains(string(encoded), "private recipient") {
		t.Fatalf("delivery response leaked details: %s", encoded)
	}
}

func TestGraphQLRequestMagicLinkMissingReporterFailsBeforeDelivery(t *testing.T) {
	port := &accountAuthPort{}
	ctx := ContextWithAuthAccountService(context.Background(), port)
	response, err := viewerGraphQLClient(ctx, NodeServices{}).RawPost(`mutation RequestMagicLink($email: String!) { requestMagicLink(input: {email: $email}) { accepted errors { code field } } }`, client.Var("email", "person@example.test"))
	if err != nil || len(response.Errors) == 0 || len(port.requests) != 0 {
		t.Fatalf("missing reporter GraphQL result = (%+v, %v), requests=%+v", response, err, port.requests)
	}
}

func TestGraphQLRequestMagicLinkSerializesAcceptedPayloadWithoutRecipientDetails(t *testing.T) {
	port := &accountAuthPort{requestErr: errors.New("private delivery detail")}
	reporter := &accountFailureReporter{}
	ctx := ContextWithAuthMutationFailureReporter(ContextWithAuthAccountService(context.Background(), port), reporter)
	response, err := viewerGraphQLClient(ctx, NodeServices{}).RawPost(`mutation RequestMagicLink($email: String!) { requestMagicLink(input: {email: $email}) { accepted errors { code field } } }`, client.Var("email", "person@example.test"))
	if err != nil || len(response.Errors) != 0 || reporter.magicLinkFailures != 1 {
		t.Fatalf("magic link GraphQL result = (%+v, %v), failure events=%d", response, err, reporter.magicLinkFailures)
	}
	encoded, _ := json.Marshal(response.Data)
	if string(encoded) != `{"requestMagicLink":{"accepted":true,"errors":[]}}` || strings.Contains(string(encoded), "person@example.test") || strings.Contains(string(encoded), "private delivery detail") {
		t.Fatalf("magic link payload = %s", encoded)
	}
}

func TestRequestMagicLinkRejectsInvalidInputAndUnapprovedRedirectBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		email    string
		redirect *string
		field    string
	}{
		{email: "not-an-email", field: "email"},
		{email: "person@example.test", redirect: ptrString("https://evil.example/"), field: "redirectURI"},
	} {
		port := &accountAuthPort{}
		ctx := ContextWithAuthAccountService(context.Background(), port)
		payload, err := resolveRequestMagicLink(ctx, tc.email, tc.redirect)
		if err != nil || payload == nil || payload.Accepted || len(payload.Errors) != 1 || payload.Errors[0].Code != "BAD_USER_INPUT" || payload.Errors[0].Field == nil || *payload.Errors[0].Field != tc.field || len(port.requests) != 0 {
			t.Fatalf("invalid %s = (%+v, %v), calls=%+v", tc.field, payload, err, port.requests)
		}
	}
	port := &accountAuthPort{}
	ctx := ContextWithMagicLinkRedirects(ContextWithAuthMutationFailureReporter(ContextWithAuthAccountService(context.Background(), port), &accountFailureReporter{}), []string{"https://app.example/callback"})
	redirect := "https://app.example/callback"
	payload, err := resolveRequestMagicLink(ctx, "person@example.test", &redirect)
	if err != nil || payload == nil || !payload.Accepted || len(port.requests) != 1 || port.requests[0].redirect != redirect {
		t.Fatalf("allowed redirect = (%+v, %v), calls=%+v", payload, err, port.requests)
	}
}

func TestRequestMagicLinkDoesNotDowngradeFailedAuthenticationToAnonymous(t *testing.T) {
	port := &accountAuthPort{}
	ctx := ContextWithAuthAccountService(ContextWithFailedAuthentication(context.Background()), port)
	payload, err := resolveRequestMagicLink(ctx, "person@example.test", nil)
	if payload != nil || !errors.Is(err, errNodeAuthentication) || len(port.requests) != 0 {
		t.Fatalf("failed authentication = (%+v, %v), requests=%+v", payload, err, port.requests)
	}
}

func TestSignOutRequiresTrustedSessionAndCookieSinkBeforeRevocation(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	ctx := accountContext(port)
	if _, err := resolveSignOut(ctx); err == nil || len(port.revokes) != 0 {
		t.Fatalf("missing sink revoked session: err=%v revokes=%+v", err, port.revokes)
	}
	sink := &accountCookieSink{}
	ctx = ContextWithAuthTransport(ctx, sink)
	payload, err := resolveSignOut(ctx)
	if err != nil || payload == nil || payload.Session == nil || payload.Session.ID != relayid.Encode(relayid.Session, accountSessionID) || len(port.revokes) != 1 || port.revokes[0].actor != "owner" || port.revokes[0].id != accountSessionID || sink.clears != 1 || sink.sets != 0 {
		t.Fatalf("sign out = (%+v, %v), revokes=%+v, sink=%+v", payload, err, port.revokes, sink)
	}
}

func TestSignOutCookieFailureLeavesServerSessionRevokedAndRedactsError(t *testing.T) {
	const secret = "private cookie writer detail"
	port := &accountAuthPort{session: accountSession()}
	sink := &accountCookieSink{clearErr: errors.New(secret)}
	ctx := ContextWithAuthTransport(accountContext(port), sink)
	payload, err := resolveSignOut(ctx)
	if payload != nil || err == nil || strings.Contains(err.Error(), secret) || len(port.revokes) != 1 || sink.clears != 1 {
		t.Fatalf("cookie failure = (%+v, %v), revokes=%+v, sink=%+v", payload, err, port.revokes, sink)
	}
}

func TestGraphQLSignOutReturnsOnlyRevokedSessionMetadata(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	sink := &accountCookieSink{}
	ctx := ContextWithAuthTransport(accountContext(port), sink)
	response, err := viewerGraphQLClient(ctx, NodeServices{}).RawPost(`mutation SignOut { signOut { errors { code } session { id createdAt revokedAt } } }`)
	if err != nil || len(response.Errors) != 0 || len(port.revokes) != 1 || sink.clears != 1 {
		t.Fatalf("sign out GraphQL result = (%+v, %v), revokes=%+v, clears=%d", response, err, port.revokes, sink.clears)
	}
	encoded, _ := json.Marshal(response.Data)
	if !strings.Contains(string(encoded), relayid.Encode(relayid.Session, accountSessionID)) || strings.Contains(string(encoded), "TokenHash") || strings.Contains(string(encoded), "opaque-secret") {
		t.Fatalf("sign out payload = %s", encoded)
	}
}

func TestRevokeSessionRequiresSessionRelayIDAndOwnerScopedMetadata(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	ctx := accountContext(port)
	wrongKind := relayid.Encode(relayid.Subject, accountSessionID)
	payload, err := resolveRevokeSession(ctx, wrongKind)
	if err != nil || payload == nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "BAD_USER_INPUT" || len(port.lookups) != 0 || len(port.revokes) != 0 {
		t.Fatalf("wrong-kind ID = (%+v, %v), lookups=%+v", payload, err, port.lookups)
	}
	port.session.UserID = "other"
	payload, err = resolveRevokeSession(ctx, relayid.Encode(relayid.Session, accountSessionID))
	if err != nil || payload == nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "NOT_FOUND" || len(port.revokes) != 0 {
		t.Fatalf("nonowner session = (%+v, %v), revokes=%+v", payload, err, port.revokes)
	}
	port.session.UserID = "owner"
	sink := &accountCookieSink{}
	ctx = ContextWithAuthTransport(ctx, sink)
	payload, err = resolveRevokeSession(ctx, relayid.Encode(relayid.Session, accountSessionID))
	if err != nil || payload == nil || payload.Session == nil || payload.Session.ID != relayid.Encode(relayid.Session, accountSessionID) || len(port.revokes) != 1 || port.revokes[0].actor != "owner" || port.revokes[0].id != accountSessionID || sink.clears != 1 {
		t.Fatalf("owner revoke = (%+v, %v), lookups=%+v revokes=%+v session=%+v", payload, err, port.lookups, port.revokes, port.session)
	}
}

func TestRevokeSessionMasksMissingAndForeignTargetsIdentically(t *testing.T) {
	globalID := relayid.Encode(relayid.Session, accountSessionID)
	for _, tc := range []struct {
		name string
		port accountAuthPort
	}{
		{name: "missing", port: accountAuthPort{lookupErr: auth.ErrNotFound}},
		{name: "foreign", port: accountAuthPort{session: auth.Session{ID: accountSessionID, UserID: "other"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &tc.port
			payload, err := resolveRevokeSession(accountContext(port), globalID)
			if err != nil || payload == nil || payload.Session != nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "NOT_FOUND" || len(port.revokes) != 0 {
				t.Fatalf("masked target = (%+v, %v), revokes=%+v", payload, err, port.revokes)
			}
		})
	}
}

func TestRevokeSessionRejectsRevokedTargetWithoutRepeatingSideEffect(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	revoked := time.Date(2026, 9, 24, 0, 15, 0, 0, time.UTC)
	port.session.RevokedAt = &revoked
	payload, err := resolveRevokeSession(accountContext(port), relayid.Encode(relayid.Session, accountSessionID))
	if err != nil || payload == nil || payload.Session != nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "CONFLICT" || len(port.revokes) != 0 {
		t.Fatalf("revoked target = (%+v, %v), revokes=%+v", payload, err, port.revokes)
	}
}

func TestRevokeCurrentSessionRequiresCookieSinkBeforeSideEffect(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	payload, err := resolveRevokeSession(accountContext(port), relayid.Encode(relayid.Session, accountSessionID))
	if payload != nil || err == nil || len(port.revokes) != 0 {
		t.Fatalf("missing cookie sink = (%+v, %v), revokes=%+v", payload, err, port.revokes)
	}
}

func TestGraphQLRevokeSessionReturnsTypedIDErrorAndOwnerMetadata(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	sink := &accountCookieSink{}
	ctx := ContextWithAuthTransport(accountContext(port), sink)
	graph := viewerGraphQLClient(ctx, NodeServices{})
	operation := `mutation RevokeSession($id: ID!) { revokeSession(input: {id: $id}) { errors { code field } session { id createdAt } } }`
	invalid, err := graph.RawPost(operation, client.Var("id", relayid.Encode(relayid.Subject, accountSessionID)))
	if err != nil || len(invalid.Errors) != 0 || len(port.revokes) != 0 {
		t.Fatalf("invalid ID GraphQL result = (%+v, %v), revokes=%+v", invalid, err, port.revokes)
	}
	encoded, _ := json.Marshal(invalid.Data)
	if !strings.Contains(string(encoded), `"BAD_USER_INPUT"`) || !strings.Contains(string(encoded), `"id"`) {
		t.Fatalf("invalid ID typed payload = %s", encoded)
	}
	valid, err := graph.RawPost(operation, client.Var("id", relayid.Encode(relayid.Session, accountSessionID)))
	if err != nil || len(valid.Errors) != 0 || len(port.revokes) != 1 || sink.clears != 1 {
		t.Fatalf("valid revoke GraphQL result = (%+v, %v), revokes=%+v, clears=%d", valid, err, port.revokes, sink.clears)
	}
	encoded, _ = json.Marshal(valid.Data)
	if !strings.Contains(string(encoded), relayid.Encode(relayid.Session, accountSessionID)) || strings.Contains(string(encoded), "TokenHash") {
		t.Fatalf("revoke payload = %s", encoded)
	}
}

func TestRevokeOtherSessionsUsesOnlyVerifiedCurrentSession(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	payload, err := resolveRevokeOtherSessions(accountContext(port))
	if err != nil || payload == nil || payload.CurrentSession == nil || payload.CurrentSession.ID != relayid.Encode(relayid.Session, accountSessionID) || len(port.others) != 1 || port.others[0].actor != "owner" || port.others[0].id != accountSessionID {
		t.Fatalf("revoke others = (%+v, %v), calls=%+v", payload, err, port.others)
	}
	port = &accountAuthPort{session: accountSession()}
	ctx := ContextWithAuthAccountService(ContextWithVerifiedViewer(context.Background(), "owner"), port)
	if _, err := resolveRevokeOtherSessions(ctx); err == nil || len(port.others) != 0 {
		t.Fatalf("missing trusted current session revoked peers: err=%v calls=%+v", err, port.others)
	}
}

func TestGraphQLRevokeOtherSessionsProjectsTrustedCurrentSessionOnly(t *testing.T) {
	port := &accountAuthPort{session: accountSession()}
	response, err := viewerGraphQLClient(accountContext(port), NodeServices{}).RawPost(`mutation RevokeOtherSessions { revokeOtherSessions { errors { code } currentSession { id createdAt } } }`)
	if err != nil || len(response.Errors) != 0 || len(port.others) != 1 || port.others[0].id != accountSessionID {
		t.Fatalf("revoke others GraphQL result = (%+v, %v), calls=%+v", response, err, port.others)
	}
	encoded, _ := json.Marshal(response.Data)
	if !strings.Contains(string(encoded), relayid.Encode(relayid.Session, accountSessionID)) || strings.Contains(string(encoded), "TokenHash") {
		t.Fatalf("revoke others payload = %s", encoded)
	}
}

func ptrString(value string) *string { return &value }
