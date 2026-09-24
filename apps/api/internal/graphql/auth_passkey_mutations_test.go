package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const passkeySessionID = "2ac25589-cdea-4e5f-b70f-99e92609df30"

type passkeyMutationPort struct {
	beginRegistrationFor  string
	finishRegistrationFor string
	beginSignInFor        string
	beginSignInCalls      int
	finishedSignIn        int
	finishedCeremony      string
	finishedDocument      json.RawMessage
	finishedMetadata      auth.SessionMetadata
	lookedUpSession       int
	revokedToken          string
	revokeCalls           int
	revokeErrors          []error
	revokeSawCanceled     bool
	revokeSawDeadline     bool
	beginOptions          auth.PasskeyOptions
	credential            auth.PasskeyCredential
	grant                 auth.SessionGrant
	session               auth.Session
	beginErr              error
	finishErr             error
	loginErr              error
	lookupErr             error
}

func (port *passkeyMutationPort) BeginPasskeyRegistration(_ context.Context, actor string) (auth.PasskeyOptions, error) {
	port.beginRegistrationFor = actor
	return port.beginOptions, port.beginErr
}
func (port *passkeyMutationPort) CompletePasskeyRegistrationForUser(_ context.Context, actor, _ string, _ json.RawMessage, _ string) (auth.PasskeyCredential, error) {
	port.finishRegistrationFor = actor
	return port.credential, port.finishErr
}
func (port *passkeyMutationPort) BeginPasskeyLogin(_ context.Context, actor string) (auth.PasskeyOptions, error) {
	port.beginSignInFor = actor
	port.beginSignInCalls++
	return port.beginOptions, port.beginErr
}
func (port *passkeyMutationPort) CompletePasskeyLogin(_ context.Context, ceremony string, credentialID []byte, document json.RawMessage, metadata auth.SessionMetadata) (auth.SessionGrant, error) {
	port.finishedSignIn++
	port.finishedCeremony = ceremony
	port.finishedDocument = append(json.RawMessage(nil), document...)
	port.finishedMetadata = metadata
	if !bytes.Equal(credentialID, []byte("cred")) {
		return auth.SessionGrant{}, errors.New("wrong credential ID")
	}
	return port.grant, port.loginErr
}
func (port *passkeyMutationPort) GetSessionByID(_ context.Context, _, _ string) (auth.Session, error) {
	port.lookedUpSession++
	return port.session, port.lookupErr
}
func (port *passkeyMutationPort) RevokeSession(ctx context.Context, token string) error {
	port.revokeCalls++
	port.revokedToken = token
	port.revokeSawCanceled = port.revokeSawCanceled || ctx.Err() != nil
	_, port.revokeSawDeadline = ctx.Deadline()
	if port.revokeCalls <= len(port.revokeErrors) {
		return port.revokeErrors[port.revokeCalls-1]
	}
	return nil
}

type passkeyCookieSink struct {
	token  string
	expiry time.Time
	setErr error
	clears int
}

func (sink *passkeyCookieSink) SetSessionCookie(token string, expiry time.Time) error {
	sink.token, sink.expiry = token, expiry
	return sink.setErr
}
func (sink *passkeyCookieSink) ClearSessionCookie() error {
	sink.clears++
	return nil
}

type passkeyFailureReporter struct{ compensationFailures int }

func (*passkeyFailureReporter) ReportMagicLinkRequestFailure(context.Context) {}
func (reporter *passkeyFailureReporter) ReportSessionCompensationFailure(context.Context) {
	reporter.compensationFailures++
}

func trustedPasskeyContext(ctx context.Context, port *passkeyMutationPort, sink *passkeyCookieSink, reporter *passkeyFailureReporter) context.Context {
	ctx = ContextWithPasskeyMutationService(ctx, port)
	ctx = ContextWithAuthTransport(ctx, sink)
	return ContextWithAuthMutationFailureReporter(ctx, reporter)
}

func passkeyOptions() auth.PasskeyOptions {
	return auth.PasskeyOptions{CeremonyID: "ceremony-1", PublicKey: json.RawMessage(`{"challenge":"public"}`), ExpiresAt: time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)}
}
func passkeySignInPort() *passkeyMutationPort {
	now := time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)
	return &passkeyMutationPort{
		beginOptions: passkeyOptions(),
		grant:        auth.SessionGrant{Token: "opaque-secret-token", SessionID: passkeySessionID, UserID: "owner", ExpiresAt: now.Add(time.Hour)},
		session:      auth.Session{ID: passkeySessionID, UserID: "owner", TokenHash: auth.Digest{1}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
}

func TestPasskeyRegistrationRequiresVerifiedOwnerAndProjectsOnlyPublicData(t *testing.T) {
	port := passkeySignInPort()
	ctx := ContextWithPasskeyMutationService(context.Background(), port)
	for _, denied := range []context.Context{ctx, ContextWithFailedAuthentication(ctx)} {
		payload, err := (&mutationResolver{}).resolveBeginPasskeyRegistration(denied)
		if payload != nil || !errors.Is(err, errNodeAuthentication) || port.beginRegistrationFor != "" {
			t.Fatalf("unauthorized registration = (%+v, %v), actor=%q", payload, err, port.beginRegistrationFor)
		}
	}
	ctx = ContextWithVerifiedViewer(ctx, "owner")
	begin, err := (&mutationResolver{}).resolveBeginPasskeyRegistration(ctx)
	if err != nil || begin == nil || begin.Options == nil || begin.Options.CeremonyID != "ceremony-1" || begin.Options.PublicKeyJSON != `{"challenge":"public"}` || port.beginRegistrationFor != "owner" {
		t.Fatalf("begin registration = (%+v, %v), actor=%q", begin, err, port.beginRegistrationFor)
	}
	port.credential = auth.PasskeyCredential{ID: "credential-1", UserID: "owner", PublicKey: []byte("private-material"), Label: "laptop", Transports: []string{"internal"}, CreatedAt: time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)}
	label := "laptop"
	finish, err := (&mutationResolver{}).resolveFinishPasskeyRegistration(ctx, model.FinishPasskeyRegistrationInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON, Label: &label})
	if err != nil || finish == nil || finish.Credential == nil || finish.Credential.ID != "credential-1" || port.finishRegistrationFor != "owner" {
		t.Fatalf("finish registration = (%+v, %v), actor=%q", finish, err, port.finishRegistrationFor)
	}
	encoded, _ := json.Marshal(finish)
	if strings.Contains(string(encoded), "private-material") || strings.Contains(string(encoded), "opaque-secret-token") {
		t.Fatalf("credential projection leaked sensitive data: %s", encoded)
	}
}

func TestPasskeyRegistrationRejectsMalformedCredentialAsTypedInputError(t *testing.T) {
	port := passkeySignInPort()
	ctx := ContextWithPasskeyMutationService(ContextWithVerifiedViewer(context.Background(), "owner"), port)
	payload, err := (&mutationResolver{}).resolveFinishPasskeyRegistration(ctx, model.FinishPasskeyRegistrationInput{CeremonyID: "ceremony-1", CredentialJSON: `{"rawId":"secret"}`})
	if err != nil || payload == nil || payload.Credential != nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "BAD_USER_INPUT" || port.finishRegistrationFor != "" {
		t.Fatalf("invalid registration = (%+v, %v), actor=%q", payload, err, port.finishRegistrationFor)
	}
}

func TestPasskeySignInIsDiscoverableAndFailsClosedWithoutTrustedCookieSink(t *testing.T) {
	port := passkeySignInPort()
	ctx := ContextWithPasskeyMutationService(context.Background(), port)
	begin, err := (&mutationResolver{}).resolveBeginPasskeySignIn(ctx)
	if err != nil || begin == nil || begin.Options == nil || port.beginSignInFor != "" {
		t.Fatalf("discoverable begin = (%+v, %v), actor=%q", begin, err, port.beginSignInFor)
	}
	finish, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON})
	if finish != nil || !errors.Is(err, errNodeLookup) || port.finishedSignIn != 0 {
		t.Fatalf("missing cookie sink = (%+v, %v), service calls=%d", finish, err, port.finishedSignIn)
	}
}

func TestPasskeyGraphQLMutationDispatchesToApplicationPort(t *testing.T) {
	port := passkeySignInPort()
	schema := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	graph := client.New(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := ContextWithPasskeyMutationService(request.Context(), port)
		schema.ServeHTTP(w, request.WithContext(ctx))
	}))
	response, err := graph.RawPost(`mutation BeginPasskeySignIn { beginPasskeySignIn { errors { code } options { ceremonyID publicKeyJSON } } }`)
	if err != nil || len(response.Errors) != 0 || port.beginSignInCalls != 1 {
		t.Fatalf("GraphQL dispatch = (%+v, %v)", response, err)
	}
	encoded, _ := json.Marshal(response.Data)
	if !strings.Contains(string(encoded), `"ceremonyID":"ceremony-1"`) || !strings.Contains(string(encoded), `"publicKeyJSON"`) {
		t.Fatalf("GraphQL option projection = %s", encoded)
	}
}

func TestPasskeyGraphQLFinishSetsTrustedCookieWithoutReturningBearer(t *testing.T) {
	port := passkeySignInPort()
	sink := &passkeyCookieSink{}
	reporter := &passkeyFailureReporter{}
	schema := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	graph := client.New(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := trustedPasskeyContext(request.Context(), port, sink, reporter)
		schema.ServeHTTP(w, request.WithContext(ctx))
	}))
	operation := `mutation FinishPasskeySignIn($input: FinishPasskeySignInInput!) { finishPasskeySignIn(input: $input) { errors { code } session { id createdAt expiresAt } } }`
	variable := client.Var("input", map[string]any{"ceremonyID": "ceremony-1", "credentialJSON": validPasskeyJSON})
	response, err := graph.RawPost(operation, variable)
	if err != nil || len(response.Errors) != 0 || sink.token != "opaque-secret-token" || port.finishedSignIn != 1 {
		t.Fatalf("GraphQL finish did not set trusted cookie: err=%v graphqlErrors=%d cookieSet=%t serviceCalls=%d", err, len(response.Errors), sink.token != "", port.finishedSignIn)
	}
	encoded, _ := json.Marshal(response.Data)
	if !strings.Contains(string(encoded), relayid.Encode(relayid.Session, passkeySessionID)) || strings.Contains(string(encoded), "opaque-secret-token") || strings.Contains(string(encoded), "TokenHash") {
		t.Fatalf("GraphQL session projection missing or leaked a bearer: %s", encoded)
	}
	sink.setErr = errors.New("private cookie failure")
	response, err = graph.RawPost(operation, variable)
	if err != nil || len(response.Errors) == 0 || port.revokeCalls != 1 {
		t.Fatalf("GraphQL cookie failure not rejected/compensated: err=%v graphqlErrors=%d revokes=%d", err, len(response.Errors), port.revokeCalls)
	}
	encoded, _ = json.Marshal(response)
	if strings.Contains(string(encoded), "opaque-secret-token") || strings.Contains(string(encoded), "private cookie failure") {
		t.Fatal("GraphQL error leaked bearer or transport detail")
	}
}

func TestPasskeySignInRefusesIssuanceWithoutTrustedCompensationReporter(t *testing.T) {
	port := passkeySignInPort()
	ctx := ContextWithAuthTransport(ContextWithPasskeyMutationService(context.Background(), port), &passkeyCookieSink{})
	payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON})
	if payload != nil || !errors.Is(err, errNodeLookup) || port.finishedSignIn != 0 {
		t.Fatalf("missing failure reporter must block issuance: payload=%+v err=%v calls=%d", payload, err, port.finishedSignIn)
	}
}

func TestPasskeySignInProjectsSessionWithoutBearerAndCompensatesCookieFailure(t *testing.T) {
	port := passkeySignInPort()
	sink := &passkeyCookieSink{}
	reporter := &passkeyFailureReporter{}
	ctx := trustedPasskeyContext(context.Background(), port, sink, reporter)
	ctx = ContextWithPasskeySessionMetadata(ctx, auth.SessionMetadata{IPAddress: "192.0.2.10", UserAgent: "browser"})
	input := model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON}
	payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, input)
	if err != nil || payload == nil || payload.Session == nil || payload.Session.ID != relayid.Encode(relayid.Session, passkeySessionID) || sink.token != "opaque-secret-token" || port.lookedUpSession != 1 {
		t.Fatalf("finish sign-in = (%+v, %v), cookie set=%t, lookups=%d", payload, err, sink.token != "", port.lookedUpSession)
	}
	if port.finishedCeremony != "ceremony-1" || string(port.finishedDocument) != validPasskeyJSON || port.finishedMetadata != (auth.SessionMetadata{IPAddress: "192.0.2.10", UserAgent: "browser"}) {
		t.Fatalf("trusted ceremony, document, or metadata not forwarded: ceremony=%q document forwarded=%t metadata=%+v", port.finishedCeremony, len(port.finishedDocument) != 0, port.finishedMetadata)
	}
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "opaque-secret-token") || strings.Contains(string(encoded), "TokenHash") {
		t.Fatalf("session payload leaked token or digest: %s", encoded)
	}
	port.revokedToken = ""
	sink.setErr = errors.New("private cookie failure")
	payload, err = (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, input)
	if payload != nil || !errors.Is(err, errNodeLookup) || port.revokedToken != "opaque-secret-token" || sink.clears != 1 || reporter.compensationFailures != 0 {
		t.Fatalf("cookie failure compensation = (%+v, %v), revoked=%t clears=%d reports=%d", payload, err, port.revokedToken != "", sink.clears, reporter.compensationFailures)
	}
}

func TestPasskeyCompensationUsesBoundedNonCancelledContextAndRetries(t *testing.T) {
	port := passkeySignInPort()
	port.lookupErr = errors.New("private lookup failure")
	port.revokeErrors = []error{errors.New("transient revoke failure")}
	reporter := &passkeyFailureReporter{}
	sink := &passkeyCookieSink{}
	request, cancel := context.WithCancel(context.Background())
	ctx := trustedPasskeyContext(request, port, sink, reporter)
	// The issued grant outlives a canceled request; cleanup must not inherit
	// cancellation or become unbounded.
	cancel()
	payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON})
	if payload != nil || !errors.Is(err, errNodeLookup) || port.revokeCalls != 2 || port.revokeSawCanceled || !port.revokeSawDeadline || reporter.compensationFailures != 0 {
		t.Fatalf("bounded compensation = (%+v, %v), calls=%d canceled=%t deadline=%t reports=%d", payload, err, port.revokeCalls, port.revokeSawCanceled, port.revokeSawDeadline, reporter.compensationFailures)
	}
}

func TestPasskeyCompensationReportsCriticalEventWhenRevocationUnconfirmed(t *testing.T) {
	port := passkeySignInPort()
	port.lookupErr = errors.New("private lookup failure")
	port.revokeErrors = []error{errors.New("first"), errors.New("second"), errors.New("third")}
	reporter := &passkeyFailureReporter{}
	ctx := trustedPasskeyContext(context.Background(), port, &passkeyCookieSink{}, reporter)
	payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON})
	if payload != nil || !errors.Is(err, errNodeLookup) || port.revokeCalls != 3 || reporter.compensationFailures != 1 || strings.Contains(err.Error(), "private") {
		t.Fatalf("unconfirmed compensation = (%+v, %v), calls=%d reports=%d", payload, err, port.revokeCalls, reporter.compensationFailures)
	}
}

func TestPasskeyCloneSuspicionReturnsSafeReauthenticationPayload(t *testing.T) {
	port := passkeySignInPort()
	port.loginErr = errors.Join(auth.ErrInvalidSignCount, auth.ErrMagicLinkReauthenticationRequired)
	ctx := trustedPasskeyContext(context.Background(), port, &passkeyCookieSink{}, &passkeyFailureReporter{})
	payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON})
	if err != nil || payload == nil || payload.Session != nil || len(payload.Errors) != 1 || payload.Errors[0].Code != "REAUTHENTICATION_REQUIRED" {
		t.Fatalf("clone warning = (%+v, %v)", payload, err)
	}
}

func TestPasskeySignInCommitsOnlyDeliberatePostConsumptionFailures(t *testing.T) {
	tests := []struct {
		name       string
		failure    error
		wantCommit bool
	}{
		{"invalid ceremony", auth.ErrInvalidCeremony, true},
		{"verification failure", auth.ErrPasskeyVerification, true},
		{"suspected clone", errors.Join(auth.ErrInvalidSignCount, auth.ErrMagicLinkReauthenticationRequired), true},
		{"disabled user", auth.ErrUserDisabled, true},
		{"invalid input", auth.ErrInvalidInput, false},
		{"persistence failure", errors.New("private database failure"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := passkeySignInPort()
			port.loginErr = test.failure
			ctx, marker := operations.WithMutationFailureCommitMarker(context.Background())
			ctx = trustedPasskeyContext(ctx, port, &passkeyCookieSink{}, &passkeyFailureReporter{})
			payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON})
			if port.finishedSignIn != 1 || marker.Marked() != test.wantCommit || (payload != nil && payload.Session != nil) {
				t.Fatalf("failure outcome: service calls=%d commit=%t payload=%+v err=%v", port.finishedSignIn, marker.Marked(), payload, err)
			}
		})
	}
}

func TestPasskeySignInPublishesNewAuditActorOnlyAfterTrustedCookieSucceeds(t *testing.T) {
	port := passkeySignInPort()
	sink := &passkeyCookieSink{}
	ctx := trustedPasskeyContext(context.Background(), port, sink, &passkeyFailureReporter{})
	var published []string
	var publishedBeforeCookie bool
	ctx = ContextWithPostAuthAuditActorPublisher(ctx, func(userID string) {
		published = append(published, userID)
		publishedBeforeCookie = sink.token == ""
	})
	var targets []operations.AuditTarget
	ctx = ContextWithMutationAuditTargetPublisher(ctx, func(target operations.AuditTarget) {
		targets = append(targets, target)
	})
	input := model.FinishPasskeySignInInput{CeremonyID: "ceremony-1", CredentialJSON: validPasskeyJSON}
	if payload, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, input); err != nil || payload == nil || payload.Session == nil {
		t.Fatalf("successful sign-in: payload=%+v err=%v", payload, err)
	}
	if len(published) != 1 || published[0] != "owner" || publishedBeforeCookie {
		t.Fatalf("post-auth audit actor = %v, before cookie=%t", published, publishedBeforeCookie)
	}
	if len(targets) != 1 || targets[0] != (operations.AuditTarget{Type: "session", ID: passkeySessionID}) {
		t.Fatalf("post-auth audit target = %+v", targets)
	}
	port.loginErr = auth.ErrPasskeyVerification
	if _, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, input); err != nil {
		t.Fatalf("typed verification error = %v", err)
	}
	if len(published) != 1 || len(targets) != 1 {
		t.Fatalf("failed sign-in published audit identity: actors=%v targets=%+v", published, targets)
	}
	port.loginErr = nil
	sink.setErr = errors.New("private cookie failure")
	if _, err := (&mutationResolver{}).resolveFinishPasskeySignIn(ctx, input); !errors.Is(err, errNodeLookup) {
		t.Fatalf("cookie failure error = %v", err)
	}
	if len(published) != 1 || len(targets) != 1 {
		t.Fatalf("cookie failure published audit identity: actors=%v targets=%+v", published, targets)
	}
}

const validPasskeyJSON = `{"id":"Y3JlZA","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}`

func TestDecodePasskeyCredentialJSONRejectsUnsafeEnvelopes(t *testing.T) {
	valid := `{"id":"Y3JlZA","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}`
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"null", "null"},
		{"array", "[]"},
		{"trailing JSON", valid + ` {}`},
		{"oversize", valid + strings.Repeat(" ", 1<<20)},
		{"unknown field", `{"id":"Y3JlZA","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{},"secret":"x"}`},
		{"missing raw ID", `{"id":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}`},
		{"invalid base64url", `{"id":"!","rawId":"!","type":"public-key","response":{},"clientExtensionResults":{}}`},
		{"mismatched IDs", `{"id":"b3RoZXI","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}`},
		{"duplicate raw ID", `{"id":"Y3JlZA","rawId":"b3RoZXI","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}`},
		{"wrong type", `{"id":"Y3JlZA","rawId":"Y3JlZA","type":"password","response":{},"clientExtensionResults":{}}`},
		{"non-object response", `{"id":"Y3JlZA","rawId":"Y3JlZA","type":"public-key","response":[],"clientExtensionResults":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodePasskeyCredentialJSON(test.raw)
			if err == nil {
				t.Fatal("unsafe credential accepted")
			}
			if (test.raw != "" && strings.Contains(err.Error(), test.raw)) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("credential data leaked in error: %v", err)
			}
		})
	}
}

func TestDecodePasskeyCredentialJSONReturnsRawIDAndValidatedDocument(t *testing.T) {
	const raw = `{"id":"Y3JlZA","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}`
	id, document, err := decodePasskeyCredentialJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(id, []byte("cred")) || !json.Valid(document) || string(document) != raw {
		t.Fatalf("decoded credential = (%q, %q)", id, document)
	}
}
