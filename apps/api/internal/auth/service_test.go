package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMagicLinkCreatesHashedSingleUseSession(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)

	redirectURI := "https://app.example/?auth=magic#auth"
	if err := fixture.service.RequestMagicLink(context.Background(), "  PERSON@Example.COM ", redirectURI); err != nil {
		t.Fatalf("RequestMagicLink() error = %v", err)
	}
	messages := fixture.mailer.Messages()
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	message := messages[0]
	if message.Email != "person@example.com" {
		t.Fatalf("message email = %q", message.Email)
	}
	if message.RedirectURI != redirectURI {
		t.Fatalf("message redirect URI = %q, want %q", message.RedirectURI, redirectURI)
	}

	fixture.store.mu.RLock()
	if len(fixture.store.magicLinks) != 1 {
		fixture.store.mu.RUnlock()
		t.Fatalf("stored magic links len = %d, want 1", len(fixture.store.magicLinks))
	}
	var stored MagicLink
	for _, stored = range fixture.store.magicLinks {
	}
	fixture.store.mu.RUnlock()
	if !digestEqual(stored.TokenHash, tokenDigest(message.Token)) {
		t.Fatal("stored digest does not match delivered token")
	}
	if bytes.Contains(stored.TokenHash[:], []byte(message.Token)) {
		t.Fatal("raw magic-link token was stored")
	}
	if stored.Purpose != MagicLinkPurposeSignIn {
		t.Fatalf("stored purpose = %q, want %q", stored.Purpose, MagicLinkPurposeSignIn)
	}

	grant, err := fixture.service.CompleteMagicLink(context.Background(), message.Token, SessionMetadata{
		IPAddress: "127.0.0.1",
		UserAgent: "test-agent",
	})
	if err != nil {
		t.Fatalf("CompleteMagicLink() error = %v", err)
	}
	if grant.Token == "" || grant.UserID == "" || grant.SessionID == "" {
		t.Fatalf("incomplete grant: %+v", grant)
	}
	if grant.Token == message.Token {
		t.Fatal("session token reused the magic-link token")
	}

	user, err := fixture.service.AuthenticateSession(context.Background(), grant.Token)
	if err != nil {
		t.Fatalf("AuthenticateSession() error = %v", err)
	}
	if user.ID != grant.UserID || user.PrimaryEmail != "person@example.com" || user.EmailVerifiedAt == nil {
		t.Fatalf("authenticated user = %+v", user)
	}
	if _, err := fixture.service.CompleteMagicLink(context.Background(), message.Token, SessionMetadata{}); !errors.Is(err, ErrInvalidMagicLink) {
		t.Fatalf("replayed CompleteMagicLink() error = %v, want ErrInvalidMagicLink", err)
	}

	if err := fixture.service.RevokeSession(context.Background(), grant.Token); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, err := fixture.service.AuthenticateSession(context.Background(), grant.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked AuthenticateSession() error = %v, want ErrInvalidSession", err)
	}
}

func TestMagicLinkSignInRejectsDifferentPurposeWithoutConsumingIt(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
		t.Fatal(err)
	}
	token := fixture.mailer.Messages()[0].Token
	digest := tokenDigest(token)

	fixture.store.mu.Lock()
	link := fixture.store.magicLinks[digestKey(digest)]
	link.Purpose = MagicLinkPurposeVerifyEmail
	fixture.store.magicLinks[digestKey(digest)] = link
	fixture.store.mu.Unlock()

	if _, err := fixture.service.CompleteMagicLink(context.Background(), token, SessionMetadata{}); !errors.Is(err, ErrInvalidMagicLink) {
		t.Fatalf("cross-purpose CompleteMagicLink() error = %v, want ErrInvalidMagicLink", err)
	}
	claimed, err := fixture.store.ConsumeMagicLink(context.Background(), digest, MagicLinkPurposeVerifyEmail, fixture.clock.Now())
	if err != nil || claimed.Purpose != MagicLinkPurposeVerifyEmail {
		t.Fatalf("purpose-correct ConsumeMagicLink() = (%#v, %v)", claimed, err)
	}
	if _, err := fixture.store.ConsumeMagicLink(context.Background(), digest, MagicLinkPurposeVerifyEmail, fixture.clock.Now()); !errors.Is(err, ErrConsumed) {
		t.Fatalf("replayed purpose-correct ConsumeMagicLink() error = %v, want ErrConsumed", err)
	}
}

func TestMagicLinkExpiryAndReplacement(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)

	if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
		t.Fatal(err)
	}
	first := fixture.mailer.Messages()[0].Token
	if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
		t.Fatal(err)
	}
	second := fixture.mailer.Messages()[1].Token
	if _, err := fixture.service.CompleteMagicLink(context.Background(), first, SessionMetadata{}); !errors.Is(err, ErrInvalidMagicLink) {
		t.Fatalf("replaced token error = %v, want ErrInvalidMagicLink", err)
	}

	fixture.clock.Advance(16 * time.Minute)
	if _, err := fixture.service.CompleteMagicLink(context.Background(), second, SessionMetadata{}); !errors.Is(err, ErrInvalidMagicLink) {
		t.Fatalf("expired token error = %v, want ErrInvalidMagicLink", err)
	}
}

func TestMagicLinkMailerFailureInvalidatesToken(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.mailer.SetError(errors.New("smtp unavailable"))
	redirectURI := "https://app.example/?auth=magic#auth"

	if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", redirectURI); err == nil {
		t.Fatal("RequestMagicLink() error = nil, want mailer error")
	}
	if messages := fixture.mailer.Messages(); len(messages) != 0 {
		t.Fatalf("failed mailer recorded messages = %+v", messages)
	}
	fixture.store.mu.RLock()
	defer fixture.store.mu.RUnlock()
	if len(fixture.store.magicLinks) != 1 {
		t.Fatalf("stored magic links len = %d, want 1", len(fixture.store.magicLinks))
	}
	for _, link := range fixture.store.magicLinks {
		if link.ConsumedAt == nil {
			t.Fatal("unsent magic link remains usable")
		}
	}
}

func TestConcurrentMagicLinkConsumptionIssuesOneSession(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
		t.Fatal(err)
	}
	token := fixture.mailer.Messages()[0].Token

	const attempts = 32
	var succeeded atomic.Int32
	var wait sync.WaitGroup
	wait.Add(attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			if _, err := fixture.service.CompleteMagicLink(context.Background(), token, SessionMetadata{}); err == nil {
				succeeded.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := succeeded.Load(); got != 1 {
		t.Fatalf("successful consumptions = %d, want 1", got)
	}
}

func TestSessionExpiresAtBoundary(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.CompleteMagicLink(context.Background(), fixture.mailer.Messages()[0].Token, SessionMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(30 * 24 * time.Hour)
	if _, err := fixture.service.AuthenticateSession(context.Background(), grant.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session error = %v, want ErrInvalidSession", err)
	}
}

func TestNonActiveUsersCannotCreateOrUseAuthSessions(t *testing.T) {
	t.Parallel()
	statuses := []UserStatus{UserStatusDisabled, UserStatusPending, UserStatusDeletionPending}
	for _, status := range statuses {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			fixture := newFixture(t, true)
			fixture.saveUser(t, "user-1", "person@example.com")
			credential := PasskeyCredential{
				ID: "pk-1", UserID: "user-1", CredentialID: []byte("credential-one"), PublicKey: []byte("public-key"),
				VerifierCredential: json.RawMessage(`{"id":"credential-one"}`), CreatedAt: fixture.clock.Now(),
			}
			if err := fixture.store.SaveCredential(context.Background(), credential); err != nil {
				t.Fatal(err)
			}
			login, err := fixture.service.BeginPasskeyLogin(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}

			if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
				t.Fatal(err)
			}
			activeToken := fixture.mailer.Messages()[0].Token
			activeGrant, err := fixture.service.CompleteMagicLink(context.Background(), activeToken, SessionMetadata{})
			if err != nil {
				t.Fatal(err)
			}

			user, err := fixture.store.GetUserByID(context.Background(), "user-1")
			if err != nil {
				t.Fatal(err)
			}
			user.Status = status
			if err := fixture.store.SaveUser(context.Background(), user); err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.service.AuthenticateSession(context.Background(), activeGrant.Token); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("AuthenticateSession() error = %v, want ErrInvalidSession", err)
			}
			if _, err := fixture.service.BeginPasskeyRegistration(context.Background(), user.ID); !errors.Is(err, ErrUserDisabled) {
				t.Fatalf("BeginPasskeyRegistration() error = %v, want ErrUserDisabled", err)
			}
			if _, err := fixture.service.BeginPasskeyLogin(context.Background(), user.ID); !errors.Is(err, ErrUserDisabled) {
				t.Fatalf("BeginPasskeyLogin(bound) error = %v, want ErrUserDisabled", err)
			}

			fixture.verifier.authenticationResult = AuthenticationVerification{
				SignCount: 1, VerifierCredential: json.RawMessage(`{"id":"credential-one","signCount":1}`),
			}
			if _, err := fixture.service.CompletePasskeyLogin(
				context.Background(), login.CeremonyID, credential.CredentialID, json.RawMessage(`{"assertion":"opaque"}`), SessionMetadata{},
			); !errors.Is(err, ErrUserDisabled) {
				t.Fatalf("CompletePasskeyLogin(discoverable) error = %v, want ErrUserDisabled", err)
			}
			if fixture.verifier.authenticationCalls != 0 {
				t.Fatal("inactive passkey login reached verifier")
			}

			if err := fixture.service.RequestMagicLink(context.Background(), "person@example.com", ""); err != nil {
				t.Fatal(err)
			}
			inactiveToken := fixture.mailer.Messages()[1].Token
			if _, err := fixture.service.CompleteMagicLink(context.Background(), inactiveToken, SessionMetadata{}); !errors.Is(err, ErrUserDisabled) {
				t.Fatalf("CompleteMagicLink() error = %v, want ErrUserDisabled", err)
			}
			fixture.store.mu.RLock()
			sessionCount := len(fixture.store.sessions)
			fixture.store.mu.RUnlock()
			if sessionCount != 1 {
				t.Fatalf("sessions = %d, want only pre-status-change session", sessionCount)
			}
		})
	}
}

func TestPasskeyRegistrationLifecycle(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	fixture.verifier.registrationResult = RegistrationVerification{
		CredentialID:       []byte("credential-one"),
		PublicKey:          []byte("real-verifier-parsed-public-key"),
		AAGUID:             []byte("aaguid"),
		SignCount:          4,
		Transports:         []string{"internal"},
		VerifierCredential: json.RawMessage(`{"id":"credential-one"}`),
	}

	options, err := fixture.service.BeginPasskeyRegistration(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("BeginPasskeyRegistration() error = %v", err)
	}
	if options.CeremonyID == "" || !json.Valid(options.PublicKey) {
		t.Fatalf("invalid options: %+v", options)
	}
	if fixture.verifier.registrationOptions.Challenge == "" || fixture.verifier.registrationOptions.User.ID != "user-1" {
		t.Fatalf("verifier registration input = %+v", fixture.verifier.registrationOptions)
	}

	credential, err := fixture.service.CompletePasskeyRegistration(
		context.Background(), options.CeremonyID, json.RawMessage(`{"attestation":"opaque"}`), "Laptop",
	)
	if err != nil {
		t.Fatalf("CompletePasskeyRegistration() error = %v", err)
	}
	if credential.UserID != "user-1" || credential.Label != "Laptop" || credential.SignCount != 4 {
		t.Fatalf("credential = %+v", credential)
	}
	if fixture.verifier.registrationVerification.Challenge != fixture.verifier.registrationOptions.Challenge {
		t.Fatal("registration verification did not receive the stored challenge")
	}
	stored, err := fixture.store.GetCredentialByCredentialID(context.Background(), []byte("credential-one"))
	if err != nil {
		t.Fatalf("GetCredentialByCredentialID() error = %v", err)
	}
	if !bytes.Equal(stored.PublicKey, fixture.verifier.registrationResult.PublicKey) {
		t.Fatalf("stored public key = %q", stored.PublicKey)
	}
	if _, err := fixture.service.CompletePasskeyRegistration(context.Background(), options.CeremonyID, json.RawMessage(`{}`), ""); !errors.Is(err, ErrInvalidCeremony) {
		t.Fatalf("replayed registration error = %v, want ErrInvalidCeremony", err)
	}
}

func TestPasskeyRegistrationNormalizesAndLimitsLabel(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	fixture.verifier.registrationResult = RegistrationVerification{
		CredentialID:       []byte("credential-one"),
		PublicKey:          []byte("public-key"),
		VerifierCredential: json.RawMessage(`{"id":"credential-one"}`),
	}

	options, err := fixture.service.BeginPasskeyRegistration(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	tooLong := "  " + strings.Repeat("가", maxPasskeyLabelRunes+1) + "  "
	if _, err := fixture.service.CompletePasskeyRegistration(context.Background(), options.CeremonyID, json.RawMessage(`{}`), tooLong); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized label error = %v, want ErrInvalidInput", err)
	}

	// Label validation happens before the single-use ceremony is consumed, so
	// a client can correct this ordinary request error without re-enrolling.
	want := strings.Repeat("가", maxPasskeyLabelRunes)
	credential, err := fixture.service.CompletePasskeyRegistration(context.Background(), options.CeremonyID, json.RawMessage(`{}`), "  "+want+"  ")
	if err != nil {
		t.Fatalf("retry with valid label: %v", err)
	}
	if credential.Label != want {
		t.Fatalf("normalized label = %q, want %q", credential.Label, want)
	}
}

func TestFailedPasskeyVerificationConsumesCeremonyWithoutSavingCredential(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	options, err := fixture.service.BeginPasskeyRegistration(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	fixture.verifier.registrationErr = errors.New("bad attestation signature")

	if _, err := fixture.service.CompletePasskeyRegistration(context.Background(), options.CeremonyID, json.RawMessage(`{}`), ""); !errors.Is(err, ErrPasskeyVerification) {
		t.Fatalf("verification error = %v, want ErrPasskeyVerification", err)
	}
	credentials, err := fixture.store.ListCredentialsByUser(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 0 {
		t.Fatalf("credentials len = %d, want 0", len(credentials))
	}
	fixture.verifier.registrationErr = nil
	if _, err := fixture.service.CompletePasskeyRegistration(context.Background(), options.CeremonyID, json.RawMessage(`{}`), ""); !errors.Is(err, ErrInvalidCeremony) {
		t.Fatalf("retry error = %v, want ErrInvalidCeremony", err)
	}
}

func TestPasskeyRegistrationFinishBindsAuthenticatedUserAndConsumesMismatch(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "one@example.com")
	fixture.saveUser(t, "user-2", "two@example.com")
	fixture.verifier.registrationResult = RegistrationVerification{
		CredentialID:       []byte("credential-one"),
		PublicKey:          []byte("public-key"),
		VerifierCredential: json.RawMessage(`{"id":"credential-one"}`),
	}
	options, err := fixture.service.BeginPasskeyRegistration(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.CompletePasskeyRegistrationForUser(
		context.Background(), "user-2", options.CeremonyID, json.RawMessage(`{}`), "",
	); !errors.Is(err, ErrInvalidCeremony) {
		t.Fatalf("cross-user registration finish error = %v, want ErrInvalidCeremony", err)
	}
	if fixture.verifier.registrationVerification.Challenge != "" {
		t.Fatal("cross-user registration reached the cryptographic verifier")
	}
	if _, err := fixture.service.CompletePasskeyRegistrationForUser(
		context.Background(), "user-1", options.CeremonyID, json.RawMessage(`{}`), "",
	); !errors.Is(err, ErrInvalidCeremony) {
		t.Fatalf("owner retry after stolen finish error = %v, want ErrInvalidCeremony", err)
	}
	credentials, err := fixture.store.ListCredentialsByUser(context.Background(), "user-1")
	if err != nil || len(credentials) != 0 {
		t.Fatalf("credentials after cross-user finish = %#v, error = %v", credentials, err)
	}
}

func TestPasskeyLoginUpdatesCounterAndIssuesSession(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	credential := PasskeyCredential{
		ID:                 "pk-1",
		UserID:             "user-1",
		CredentialID:       []byte("credential-one"),
		PublicKey:          []byte("public-key"),
		SignCount:          8,
		CreatedAt:          fixture.clock.Now(),
		VerifierCredential: json.RawMessage(`{"id":"credential-one"}`),
	}
	if err := fixture.store.SaveCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	fixture.verifier.authenticationResult = AuthenticationVerification{SignCount: 9, VerifierCredential: json.RawMessage(`{"id":"credential-one","signCount":9}`)}

	options, err := fixture.service.BeginPasskeyLogin(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("BeginPasskeyLogin() error = %v", err)
	}
	if len(fixture.verifier.authenticationOptions.AllowCredentialIDs) != 1 {
		t.Fatalf("allowed credentials len = %d, want 1", len(fixture.verifier.authenticationOptions.AllowCredentialIDs))
	}
	grant, err := fixture.service.CompletePasskeyLogin(
		context.Background(), options.CeremonyID, []byte("credential-one"), json.RawMessage(`{"assertion":"opaque"}`), SessionMetadata{},
	)
	if err != nil {
		t.Fatalf("CompletePasskeyLogin() error = %v", err)
	}
	if grant.UserID != "user-1" || grant.Token == "" {
		t.Fatalf("grant = %+v", grant)
	}
	stored, err := fixture.store.GetCredentialByCredentialID(context.Background(), []byte("credential-one"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.SignCount != 9 || stored.LastUsedAt == nil {
		t.Fatalf("stored credential = %+v", stored)
	}
	if _, err := fixture.service.AuthenticateSession(context.Background(), grant.Token); err != nil {
		t.Fatalf("AuthenticateSession() error = %v", err)
	}
	if _, err := fixture.service.CompletePasskeyLogin(context.Background(), options.CeremonyID, []byte("credential-one"), json.RawMessage(`{}`), SessionMetadata{}); !errors.Is(err, ErrInvalidCeremony) {
		t.Fatalf("replayed login error = %v, want ErrInvalidCeremony", err)
	}
}

func TestPasskeyLoginRejectsCounterRegression(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	if err := fixture.store.SaveCredential(context.Background(), PasskeyCredential{
		ID: "pk-1", UserID: "user-1", CredentialID: []byte("credential-one"), PublicKey: []byte("public-key"), SignCount: 8,
		VerifierCredential: json.RawMessage(`{"id":"credential-one"}`),
	}); err != nil {
		t.Fatal(err)
	}
	fixture.verifier.authenticationResult = AuthenticationVerification{SignCount: 8, VerifierCredential: json.RawMessage(`{"id":"credential-one","signCount":8}`)}
	options, err := fixture.service.BeginPasskeyLogin(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.service.CompletePasskeyLogin(context.Background(), options.CeremonyID, []byte("credential-one"), json.RawMessage(`{}`), SessionMetadata{})
	if !errors.Is(err, ErrInvalidSignCount) || !errors.Is(err, ErrMagicLinkReauthenticationRequired) {
		t.Fatalf("counter regression error = %v, want clone rejection and magic-link reauthentication", err)
	}
	events := fixture.securityEvents.Events()
	if len(events) != 1 || events[0].Kind != SecurityEventPasskeyCloneSuspected || events[0].UserID != "user-1" || events[0].PasskeyID != "pk-1" {
		t.Fatalf("security events = %#v", events)
	}
}

func TestPasskeyLoginAuditsVerifierCloneWarningWithoutIssuingSession(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	if err := fixture.store.SaveCredential(context.Background(), PasskeyCredential{
		ID: "pk-1", UserID: "user-1", CredentialID: []byte("credential-one"), PublicKey: []byte("public-key"), SignCount: 8,
		VerifierCredential: json.RawMessage(`{"id":"credential-one"}`),
	}); err != nil {
		t.Fatal(err)
	}
	fixture.verifier.authenticationErr = errors.Join(errors.New("clone warning"), ErrInvalidSignCount)
	options, err := fixture.service.BeginPasskeyLogin(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.service.CompletePasskeyLogin(context.Background(), options.CeremonyID, []byte("credential-one"), json.RawMessage(`{}`), SessionMetadata{}); !errors.Is(err, ErrMagicLinkReauthenticationRequired) {
		t.Fatalf("clone warning error = %v, want ErrMagicLinkReauthenticationRequired", err)
	}
	if got := len(fixture.securityEvents.Events()); got != 1 {
		t.Fatalf("security event count = %d, want 1", got)
	}
	if sessions, listErr := fixture.store.ListSessionsByUser(context.Background(), "user-1"); listErr != nil || len(sessions) != 0 {
		t.Fatalf("sessions after clone warning = %#v, error = %v", sessions, listErr)
	}
}

func TestUserBoundPasskeyLoginRejectsAnotherUsersCredential(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "one@example.com")
	fixture.saveUser(t, "user-2", "two@example.com")
	if err := fixture.store.SaveCredential(context.Background(), PasskeyCredential{
		ID: "pk-2", UserID: "user-2", CredentialID: []byte("credential-two"), PublicKey: []byte("public-key"),
		VerifierCredential: json.RawMessage(`{"id":"credential-two"}`),
	}); err != nil {
		t.Fatal(err)
	}
	options, err := fixture.service.BeginPasskeyLogin(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CompletePasskeyLogin(context.Background(), options.CeremonyID, []byte("credential-two"), json.RawMessage(`{}`), SessionMetadata{}); !errors.Is(err, ErrPasskeyVerification) {
		t.Fatalf("wrong-user credential error = %v, want ErrPasskeyVerification", err)
	}
	if fixture.verifier.authenticationCalls != 0 {
		t.Fatalf("verifier calls = %d, want 0", fixture.verifier.authenticationCalls)
	}
}

func TestPasskeyOperationsRequireRealVerifierAdapter(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "user-1", "person@example.com")

	if _, err := fixture.service.BeginPasskeyRegistration(context.Background(), "user-1"); !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("BeginPasskeyRegistration() error = %v, want ErrVerifierUnavailable", err)
	}
	if _, err := fixture.service.BeginPasskeyLogin(context.Background(), "user-1"); !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("BeginPasskeyLogin() error = %v, want ErrVerifierUnavailable", err)
	}
}

func TestPasskeyCeremonyExpiresAtBoundary(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, true)
	fixture.saveUser(t, "user-1", "person@example.com")
	options, err := fixture.service.BeginPasskeyRegistration(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(5 * time.Minute)
	if _, err := fixture.service.CompletePasskeyRegistration(context.Background(), options.CeremonyID, json.RawMessage(`{}`), ""); !errors.Is(err, ErrInvalidCeremony) {
		t.Fatalf("expired ceremony error = %v, want ErrInvalidCeremony", err)
	}
}

func TestMemoryStoreDefensiveCopiesCredentialBytes(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if err := store.SaveUser(context.Background(), User{ID: "user-1", PrimaryEmail: "person@example.com", Status: UserStatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	credentialID := []byte("credential")
	publicKey := []byte("public-key")
	if err := store.SaveCredential(context.Background(), PasskeyCredential{
		ID: "pk-1", UserID: "user-1", CredentialID: credentialID, PublicKey: publicKey, CreatedAt: now,
		VerifierCredential: json.RawMessage(`{"id":"credential"}`),
	}); err != nil {
		t.Fatal(err)
	}
	credentialID[0] = 'X'
	publicKey[0] = 'X'
	loaded, err := store.GetCredentialByCredentialID(context.Background(), []byte("credential"))
	if err != nil {
		t.Fatal(err)
	}
	loaded.PublicKey[0] = 'Y'
	again, err := store.GetCredentialByCredentialID(context.Background(), []byte("credential"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again.CredentialID) != "credential" || string(again.PublicKey) != "public-key" {
		t.Fatalf("stored credential was mutated: %+v", again)
	}
}

func TestMemoryStoreDeletionTombstonePreventsIdentityResurrection(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	user, err := store.GetOrCreateUserByEmail(context.Background(), "Person@Example.com", "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), Session{
		ID: "session-1", UserID: user.ID, TokenHash: sha256.Sum256([]byte("session")),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneUserByEmail(context.Background(), "person@example.com", now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOrCreateUserByEmail(context.Background(), "person@example.com", "user-2", now.Add(time.Minute)); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("retained tombstone error = %v, want ErrUserDisabled", err)
	}
	if _, err := store.UseSession(context.Background(), sha256.Sum256([]byte("session")), now.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user's session error = %v, want ErrNotFound", err)
	}
	recreated, err := store.GetOrCreateUserByEmail(context.Background(), "person@example.com", "user-3", now.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("expired tombstone should permit recreation: %v", err)
	}
	if recreated.ID != "user-3" {
		t.Fatalf("recreated user = %#v", recreated)
	}
}

func TestSecureRandomFailureStopsBeforeIssuance(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	mailer := NewMemoryMailer()
	service, err := NewService(Dependencies{Repository: store, Mailer: mailer}, Config{Random: errorReader{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequestMagicLink(context.Background(), "person@example.com", ""); err == nil {
		t.Fatal("RequestMagicLink() error = nil, want entropy error")
	}
	if len(mailer.Messages()) != 0 {
		t.Fatal("message sent despite entropy failure")
	}
}

type testFixture struct {
	service        *Service
	store          *MemoryStore
	mailer         *MemoryMailer
	verifier       *scriptedVerifier
	clock          *testClock
	securityEvents *testSecurityEventRecorder
}

func newFixture(t *testing.T, withVerifier bool) *testFixture {
	t.Helper()
	store := NewMemoryStore()
	mailer := NewMemoryMailer()
	clock := &testClock{value: time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)}
	verifier := &scriptedVerifier{}
	securityEvents := &testSecurityEventRecorder{}
	var verifierPort PasskeyVerifier
	if withVerifier {
		verifierPort = verifier
	}
	service, err := NewService(Dependencies{
		Repository:     store,
		Mailer:         mailer,
		Verifier:       verifierPort,
		SecurityEvents: securityEvents,
	}, Config{
		Random: &sequenceReader{},
		Now:    clock.Now,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return &testFixture{service: service, store: store, mailer: mailer, verifier: verifier, clock: clock, securityEvents: securityEvents}
}

type testSecurityEventRecorder struct {
	mu     sync.Mutex
	events []SecurityEvent
}

func (recorder *testSecurityEventRecorder) RecordSecurityEvent(_ context.Context, event SecurityEvent) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *testSecurityEventRecorder) Events() []SecurityEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]SecurityEvent(nil), recorder.events...)
}

func (f *testFixture) saveUser(t *testing.T, id, email string) {
	t.Helper()
	now := f.clock.Now()
	if err := f.store.SaveUser(context.Background(), User{
		ID: id, PrimaryEmail: email, Status: UserStatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveUser() error = %v", err)
	}
}

type testClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *testClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = c.value.Add(duration)
}

type sequenceReader struct {
	mu   sync.Mutex
	next byte
}

func (r *sequenceReader) Read(target []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range target {
		r.next++
		target[index] = r.next
	}
	return len(target), nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

// scriptedVerifier is test-only. It does not claim to perform WebAuthn; tests
// use it to assert that the application layer delegates all cryptography and
// propagates only an adapter's verified outputs.
type scriptedVerifier struct {
	mu sync.Mutex

	registrationOptions      RegistrationOptionsInput
	registrationVerification RegistrationVerificationInput
	registrationResult       RegistrationVerification
	registrationErr          error

	authenticationOptions      AuthenticationOptionsInput
	authenticationVerification AuthenticationVerificationInput
	authenticationResult       AuthenticationVerification
	authenticationErr          error
	authenticationCalls        int
}

func (v *scriptedVerifier) CreateRegistrationOptions(_ context.Context, input RegistrationOptionsInput) (PasskeyVerifierOptions, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.registrationOptions = input
	return PasskeyVerifierOptions{
		PublicKey: json.RawMessage(fmt.Sprintf(`{"challenge":%q}`, input.Challenge)),
		Session:   json.RawMessage(`{"session":"registration"}`),
	}, nil
}

func (v *scriptedVerifier) VerifyRegistration(_ context.Context, input RegistrationVerificationInput) (RegistrationVerification, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.registrationVerification = input
	return v.registrationResult, v.registrationErr
}

func (v *scriptedVerifier) CreateAuthenticationOptions(_ context.Context, input AuthenticationOptionsInput) (PasskeyVerifierOptions, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.authenticationOptions = input
	return PasskeyVerifierOptions{
		PublicKey: json.RawMessage(fmt.Sprintf(`{"challenge":%q}`, input.Challenge)),
		Session:   json.RawMessage(`{"session":"authentication"}`),
	}, nil
}

func (v *scriptedVerifier) VerifyAuthentication(_ context.Context, input AuthenticationVerificationInput) (AuthenticationVerification, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.authenticationCalls++
	v.authenticationVerification = input
	return v.authenticationResult, v.authenticationErr
}
