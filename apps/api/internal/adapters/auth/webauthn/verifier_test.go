package webauthn

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	lib "github.com/go-webauthn/webauthn/webauthn"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
)

const (
	testRPID   = "example.com"
	testOrigin = "https://example.com"
	testUserID = "user-123"
)

func TestNewValidatesRelyingPartyPolicy(t *testing.T) {
	t.Parallel()

	valid := Config{
		RPID:             testRPID,
		RPDisplayName:    "Jandibat",
		RPOrigins:        []string{testOrigin},
		UserVerification: "required",
		ResidentKey:      "required",
		Attestation:      "direct",
		Timeout:          2 * time.Minute,
	}
	verifier, err := New(valid)
	if err != nil {
		t.Fatalf("New(valid config) error = %v", err)
	}
	if verifier.webAuthn.Config.RPID != testRPID {
		t.Fatalf("RPID = %q, want %q", verifier.webAuthn.Config.RPID, testRPID)
	}
	if got := verifier.webAuthn.Config.RPOrigins; len(got) != 1 || got[0] != testOrigin {
		t.Fatalf("RPOrigins = %v, want [%q]", got, testOrigin)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty rp id", mutate: func(c *Config) { c.RPID = "" }},
		{name: "rp id with scheme", mutate: func(c *Config) { c.RPID = "https://example.com" }},
		{name: "rp id with port", mutate: func(c *Config) { c.RPID = "example.com:443" }},
		{name: "rp id with path", mutate: func(c *Config) { c.RPID = "example.com/login" }},
		{name: "unsupported ipv6 rp id", mutate: func(c *Config) { c.RPID = "::1"; c.RPOrigins = []string{"http://[::1]:3000"} }},
		{name: "missing display name", mutate: func(c *Config) { c.RPDisplayName = " " }},
		{name: "missing origin", mutate: func(c *Config) { c.RPOrigins = nil }},
		{name: "remote http origin", mutate: func(c *Config) { c.RPOrigins = []string{"http://example.com"} }},
		{name: "origin with userinfo", mutate: func(c *Config) { c.RPOrigins = []string{"https://user@example.com"} }},
		{name: "origin with path", mutate: func(c *Config) { c.RPOrigins = []string{"https://example.com/login"} }},
		{name: "origin with query", mutate: func(c *Config) { c.RPOrigins = []string{"https://example.com?next=login"} }},
		{name: "unrelated origin", mutate: func(c *Config) { c.RPOrigins = []string{"https://attacker.invalid"} }},
		{name: "duplicate origin", mutate: func(c *Config) { c.RPOrigins = []string{testOrigin, testOrigin} }},
		{name: "invalid user verification", mutate: func(c *Config) { c.UserVerification = "sometimes" }},
		{name: "invalid resident key", mutate: func(c *Config) { c.ResidentKey = "sometimes" }},
		{name: "invalid attestation", mutate: func(c *Config) { c.Attestation = "sometimes" }},
		{name: "short timeout", mutate: func(c *Config) { c.Timeout = time.Millisecond }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			config.RPOrigins = append([]string(nil), valid.RPOrigins...)
			tt.mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewAllowsOnlySecureOrLoopbackOrigins(t *testing.T) {
	t.Parallel()

	for _, config := range []Config{
		{RPID: "localhost", RPDisplayName: "local", RPOrigins: []string{"http://localhost:3000"}},
		{RPID: "127.0.0.1", RPDisplayName: "local", RPOrigins: []string{"http://127.0.0.1:3000"}},
		{RPID: "example.com", RPDisplayName: "prod", RPOrigins: []string{"https://login.example.com"}},
	} {
		if _, err := New(config); err != nil {
			t.Errorf("New(%+v) error = %v", config, err)
		}
	}
}

func TestNewAcceptsEverySupportedCeremonyPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		verification string
		residentKey  string
		attestation  string
	}{
		{name: "secure defaults"},
		{name: "required and none", verification: "required", residentKey: "required", attestation: "none"},
		{name: "preferred and indirect", verification: "preferred", residentKey: "preferred", attestation: "indirect"},
		{name: "discouraged and direct", verification: "discouraged", residentKey: "discouraged", attestation: "direct"},
		{name: "enterprise", verification: "required", residentKey: "required", attestation: "enterprise"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, err := New(Config{
				RPID:             testRPID,
				RPDisplayName:    "Jandibat",
				RPOrigins:        []string{testOrigin},
				UserVerification: tt.verification,
				ResidentKey:      tt.residentKey,
				Attestation:      tt.attestation,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if verifier.webAuthn.Config.AuthenticatorSelection.UserVerification == "" ||
				verifier.webAuthn.Config.AuthenticatorSelection.ResidentKey == "" ||
				verifier.webAuthn.Config.AttestationPreference == "" {
				t.Fatalf("New() did not materialize defaults: %+v", verifier.webAuthn.Config)
			}
		})
	}
}

func TestCreateRegistrationOptionsBindsChallengeUserAndPolicy(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	challenge := testChallenge(1)
	before := time.Now()
	options, err := verifier.CreateRegistrationOptions(context.Background(), auth.RegistrationOptionsInput{
		Challenge: challenge,
		User: auth.User{
			ID:           testUserID,
			PrimaryEmail: "person@example.com",
		},
		ExcludeCredentialIDs: [][]byte{{1, 2, 3}, {4, 5, 6}},
	})
	if err != nil {
		t.Fatalf("CreateRegistrationOptions() error = %v", err)
	}

	var browser struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
			ExcludeCredentials []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"excludeCredentials"`
			AuthenticatorSelection struct {
				RequireResidentKey bool   `json:"requireResidentKey"`
				ResidentKey        string `json:"residentKey"`
				UserVerification   string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	decodeJSON(t, options.PublicKey, &browser)
	if browser.PublicKey.Challenge != challenge {
		t.Errorf("browser challenge = %q, want %q", browser.PublicKey.Challenge, challenge)
	}
	if browser.PublicKey.RP.ID != testRPID {
		t.Errorf("browser RP ID = %q, want %q", browser.PublicKey.RP.ID, testRPID)
	}
	if browser.PublicKey.User.ID != base64.RawURLEncoding.EncodeToString([]byte(testUserID)) {
		t.Errorf("browser user ID = %q", browser.PublicKey.User.ID)
	}
	if got := browser.PublicKey.ExcludeCredentials; len(got) != 2 || got[0].Type != "public-key" || got[0].ID != "AQID" {
		t.Errorf("excludeCredentials = %+v", got)
	}
	selection := browser.PublicKey.AuthenticatorSelection
	if !selection.RequireResidentKey || selection.ResidentKey != "required" || selection.UserVerification != "required" {
		t.Errorf("authenticatorSelection = %+v", selection)
	}

	var session lib.SessionData
	decodeJSON(t, options.Session, &session)
	if session.Challenge != challenge {
		t.Errorf("session challenge = %q, want %q", session.Challenge, challenge)
	}
	if !bytes.Equal(session.UserID, []byte(testUserID)) {
		t.Errorf("session user ID = %q, want %q", session.UserID, testUserID)
	}
	if session.UserVerification != protocol.VerificationRequired {
		t.Errorf("session user verification = %q", session.UserVerification)
	}
	if session.Expires.Before(before.Add(4*time.Minute+50*time.Second)) || session.Expires.After(before.Add(5*time.Minute+10*time.Second)) {
		t.Errorf("session expiry = %v, want approximately five minutes from %v", session.Expires, before)
	}
}

func TestCreateOptionsRejectInvalidInputsAndContexts(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	validUser := auth.User{ID: testUserID, PrimaryEmail: "person@example.com"}
	longUser := auth.User{ID: strings.Repeat("u", maxUserHandle+1)}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	registrationTests := []struct {
		name  string
		ctx   context.Context
		input auth.RegistrationOptionsInput
		want  error
	}{
		{name: "nil context", input: auth.RegistrationOptionsInput{Challenge: testChallenge(1), User: validUser}, want: ErrInvalidInput},
		{name: "canceled context", ctx: canceled, input: auth.RegistrationOptionsInput{Challenge: testChallenge(1), User: validUser}, want: context.Canceled},
		{name: "empty challenge", ctx: context.Background(), input: auth.RegistrationOptionsInput{User: validUser}, want: ErrInvalidInput},
		{name: "padded challenge", ctx: context.Background(), input: auth.RegistrationOptionsInput{Challenge: testChallenge(1) + "=", User: validUser}, want: ErrInvalidInput},
		{name: "short challenge", ctx: context.Background(), input: auth.RegistrationOptionsInput{Challenge: base64.RawURLEncoding.EncodeToString([]byte("short")), User: validUser}, want: ErrInvalidInput},
		{name: "empty user", ctx: context.Background(), input: auth.RegistrationOptionsInput{Challenge: testChallenge(1)}, want: ErrInvalidInput},
		{name: "long user", ctx: context.Background(), input: auth.RegistrationOptionsInput{Challenge: testChallenge(1), User: longUser}, want: ErrInvalidInput},
		{name: "empty exclusion", ctx: context.Background(), input: auth.RegistrationOptionsInput{Challenge: testChallenge(1), User: validUser, ExcludeCredentialIDs: [][]byte{nil}}, want: ErrInvalidInput},
		{name: "duplicate exclusion", ctx: context.Background(), input: auth.RegistrationOptionsInput{Challenge: testChallenge(1), User: validUser, ExcludeCredentialIDs: [][]byte{{1}, {1}}}, want: ErrInvalidInput},
	}
	for _, tt := range registrationTests {
		t.Run("registration/"+tt.name, func(t *testing.T) {
			if _, err := verifier.CreateRegistrationOptions(tt.ctx, tt.input); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}

	authenticationTests := []struct {
		name  string
		ctx   context.Context
		input auth.AuthenticationOptionsInput
		want  error
	}{
		{name: "nil context", input: auth.AuthenticationOptionsInput{Challenge: testChallenge(2)}, want: ErrInvalidInput},
		{name: "canceled context", ctx: canceled, input: auth.AuthenticationOptionsInput{Challenge: testChallenge(2)}, want: context.Canceled},
		{name: "empty challenge", ctx: context.Background(), input: auth.AuthenticationOptionsInput{}, want: ErrInvalidInput},
		{name: "discoverable with allow list", ctx: context.Background(), input: auth.AuthenticationOptionsInput{Challenge: testChallenge(2), AllowCredentialIDs: [][]byte{{1}}}, want: ErrInvalidInput},
		{name: "bound without allow list", ctx: context.Background(), input: auth.AuthenticationOptionsInput{Challenge: testChallenge(2), User: &validUser}, want: ErrInvalidInput},
		{name: "bound empty credential", ctx: context.Background(), input: auth.AuthenticationOptionsInput{Challenge: testChallenge(2), User: &validUser, AllowCredentialIDs: [][]byte{nil}}, want: ErrInvalidInput},
		{name: "bound duplicate credential", ctx: context.Background(), input: auth.AuthenticationOptionsInput{Challenge: testChallenge(2), User: &validUser, AllowCredentialIDs: [][]byte{{1}, {1}}}, want: ErrInvalidInput},
	}
	for _, tt := range authenticationTests {
		t.Run("authentication/"+tt.name, func(t *testing.T) {
			if _, err := verifier.CreateAuthenticationOptions(tt.ctx, tt.input); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCreateAuthenticationOptionsSupportsBoundAndDiscoverableCeremonies(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	user := auth.User{ID: testUserID, PrimaryEmail: "person@example.com"}
	credentialID := []byte{1, 2, 3, 4}
	challenge := testChallenge(3)
	bound, err := verifier.CreateAuthenticationOptions(context.Background(), auth.AuthenticationOptionsInput{
		Challenge:          challenge,
		User:               &user,
		AllowCredentialIDs: [][]byte{credentialID},
	})
	if err != nil {
		t.Fatalf("CreateAuthenticationOptions(bound) error = %v", err)
	}
	assertAuthenticationOptions(t, bound, challenge, testRPID, credentialID, []byte(testUserID))

	discoverable, err := verifier.CreateAuthenticationOptions(context.Background(), auth.AuthenticationOptionsInput{Challenge: challenge})
	if err != nil {
		t.Fatalf("CreateAuthenticationOptions(discoverable) error = %v", err)
	}
	assertAuthenticationOptions(t, discoverable, challenge, testRPID, nil, nil)
}

func TestDecodeSessionRejectsTampering(t *testing.T) {
	t.Parallel()

	challenge := testChallenge(12)
	valid := lib.SessionData{Challenge: challenge, UserID: []byte(testUserID)}
	tests := []struct {
		name      string
		session   json.RawMessage
		challenge string
	}{
		{name: "missing session", challenge: challenge},
		{name: "non json session", session: json.RawMessage(`not-json`), challenge: challenge},
		{name: "empty supplied challenge", session: marshalJSON(t, valid)},
		{name: "different supplied challenge", session: marshalJSON(t, valid), challenge: testChallenge(13)},
		{name: "invalid encoded session challenge", session: marshalJSON(t, lib.SessionData{Challenge: "not-base64url", UserID: []byte(testUserID)}), challenge: "not-base64url"},
		{name: "empty non nil user handle", session: marshalJSON(t, lib.SessionData{Challenge: challenge, UserID: []byte{}}), challenge: challenge},
		{name: "oversized user handle", session: marshalJSON(t, lib.SessionData{Challenge: challenge, UserID: bytes.Repeat([]byte{'u'}, maxUserHandle+1)}), challenge: challenge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeSession(tt.session, tt.challenge); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("decodeSession() error = %v, want ErrInvalidSession", err)
			}
		})
	}
}

func TestSignedRegistrationAndAuthenticationCeremonies(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	authenticator := newVirtualAuthenticator(t)
	user := auth.User{ID: testUserID, PrimaryEmail: "person@example.com"}

	registered := registerCredential(t, verifier, authenticator, user)
	if !bytes.Equal(registered.CredentialID, authenticator.credentialID) {
		t.Errorf("credential ID = %x, want %x", registered.CredentialID, authenticator.credentialID)
	}
	if len(registered.PublicKey) == 0 || len(registered.VerifierCredential) == 0 {
		t.Fatal("verified registration omitted credential material")
	}
	if !bytes.Equal(registered.AAGUID, authenticator.aaguid) {
		t.Errorf("AAGUID = %x, want %x", registered.AAGUID, authenticator.aaguid)
	}
	if registered.SignCount != 0 {
		t.Errorf("initial sign count = %d, want 0", registered.SignCount)
	}
	if len(registered.Transports) != 1 || registered.Transports[0] != "internal" {
		t.Errorf("transports = %v, want [internal]", registered.Transports)
	}

	stored := passkeyCredential(user.ID, registered)
	boundChallenge := testChallenge(5)
	boundOptions, err := verifier.CreateAuthenticationOptions(context.Background(), auth.AuthenticationOptionsInput{
		Challenge:          boundChallenge,
		User:               &user,
		AllowCredentialIDs: [][]byte{authenticator.credentialID},
	})
	if err != nil {
		t.Fatalf("CreateAuthenticationOptions(bound) error = %v", err)
	}
	bound, err := verifier.VerifyAuthentication(context.Background(), auth.AuthenticationVerificationInput{
		Challenge:       boundChallenge,
		VerifierSession: boundOptions.Session,
		Credential:      stored,
		Response: authenticator.authenticationResponse(t, authenticationResponseConfig{
			challenge:  boundChallenge,
			origin:     testOrigin,
			rpID:       testRPID,
			signCount:  1,
			userHandle: []byte(user.ID),
		}),
	})
	if err != nil {
		t.Fatalf("VerifyAuthentication(bound signed assertion) error = %v", err)
	}
	if bound.SignCount != 1 || !bytes.Equal(bound.UserHandle, []byte(user.ID)) {
		t.Errorf("bound verification = %+v", bound)
	}
	stored.SignCount = bound.SignCount
	stored.VerifierCredential = bound.VerifierCredential

	discoverableChallenge := testChallenge(6)
	discoverableOptions, err := verifier.CreateAuthenticationOptions(context.Background(), auth.AuthenticationOptionsInput{
		Challenge: discoverableChallenge,
	})
	if err != nil {
		t.Fatalf("CreateAuthenticationOptions(discoverable) error = %v", err)
	}
	discoverable, err := verifier.VerifyAuthentication(context.Background(), auth.AuthenticationVerificationInput{
		Challenge:       discoverableChallenge,
		VerifierSession: discoverableOptions.Session,
		Credential:      stored,
		Response: authenticator.authenticationResponse(t, authenticationResponseConfig{
			challenge:  discoverableChallenge,
			origin:     testOrigin,
			rpID:       testRPID,
			signCount:  2,
			userHandle: []byte(user.ID),
		}),
	})
	if err != nil {
		t.Fatalf("VerifyAuthentication(discoverable signed assertion) error = %v", err)
	}
	if discoverable.SignCount != 2 || !bytes.Equal(discoverable.UserHandle, []byte(user.ID)) {
		t.Errorf("discoverable verification = %+v", discoverable)
	}
}

func TestVerifyRegistrationRejectsUnboundAndInvalidResponses(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	authenticator := newVirtualAuthenticator(t)
	user := auth.User{ID: testUserID, PrimaryEmail: "person@example.com"}
	challenge := testChallenge(7)
	options, err := verifier.CreateRegistrationOptions(context.Background(), auth.RegistrationOptionsInput{Challenge: challenge, User: user})
	if err != nil {
		t.Fatalf("CreateRegistrationOptions() error = %v", err)
	}
	validResponse := func() json.RawMessage {
		return authenticator.registrationResponse(t, registrationResponseConfig{
			challenge: challenge,
			origin:    testOrigin,
			rpID:      testRPID,
			flags:     byte(protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagAttestedCredentialData),
		})
	}

	tests := []struct {
		name    string
		input   func() auth.RegistrationVerificationInput
		wantErr error
	}{
		{name: "malformed session", input: func() auth.RegistrationVerificationInput {
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: json.RawMessage(`{`), Response: validResponse()}
		}, wantErr: ErrInvalidSession},
		{name: "challenge not bound to session", input: func() auth.RegistrationVerificationInput {
			return auth.RegistrationVerificationInput{Challenge: testChallenge(8), User: user, VerifierSession: options.Session, Response: validResponse()}
		}, wantErr: ErrInvalidSession},
		{name: "user not bound to session", input: func() auth.RegistrationVerificationInput {
			other := user
			other.ID = "other-user"
			return auth.RegistrationVerificationInput{Challenge: challenge, User: other, VerifierSession: options.Session, Response: validResponse()}
		}, wantErr: ErrInvalidSession},
		{name: "expired session", input: func() auth.RegistrationVerificationInput {
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: expiredSession(t, options.Session), Response: validResponse()}
		}, wantErr: ErrVerification},
		{name: "malformed response", input: func() auth.RegistrationVerificationInput {
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: json.RawMessage(`{`)}
		}, wantErr: ErrVerification},
		{name: "response challenge mismatch", input: func() auth.RegistrationVerificationInput {
			response := authenticator.registrationResponse(t, registrationResponseConfig{challenge: testChallenge(8), origin: testOrigin, rpID: testRPID, flags: 0x45})
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: response}
		}, wantErr: ErrVerification},
		{name: "origin outside exact allowlist", input: func() auth.RegistrationVerificationInput {
			response := authenticator.registrationResponse(t, registrationResponseConfig{challenge: challenge, origin: "https://login.example.com", rpID: testRPID, flags: 0x45})
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: response}
		}, wantErr: ErrVerification},
		{name: "rp id hash mismatch", input: func() auth.RegistrationVerificationInput {
			response := authenticator.registrationResponse(t, registrationResponseConfig{challenge: challenge, origin: testOrigin, rpID: "other.example.com", flags: 0x45})
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: response}
		}, wantErr: ErrVerification},
		{name: "user verification missing", input: func() auth.RegistrationVerificationInput {
			response := authenticator.registrationResponse(t, registrationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, flags: byte(protocol.FlagUserPresent | protocol.FlagAttestedCredentialData)})
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: response}
		}, wantErr: ErrVerification},
		{name: "user presence missing", input: func() auth.RegistrationVerificationInput {
			response := authenticator.registrationResponse(t, registrationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, flags: byte(protocol.FlagUserVerified | protocol.FlagAttestedCredentialData)})
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: response}
		}, wantErr: ErrVerification},
		{name: "self attestation signature invalid", input: func() auth.RegistrationVerificationInput {
			response := authenticator.registrationResponse(t, registrationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, flags: 0x45, corruptSignature: true})
			return auth.RegistrationVerificationInput{Challenge: challenge, User: user, VerifierSession: options.Session, Response: response}
		}, wantErr: ErrVerification},
	}

	if _, err := verifier.VerifyRegistration(nil, auth.RegistrationVerificationInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("VerifyRegistration(nil context) error = %v, want ErrInvalidInput", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := verifier.VerifyRegistration(context.Background(), tt.input()); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyAuthenticationRejectsInvalidSecurityBindings(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	authenticator := newVirtualAuthenticator(t)
	user := auth.User{ID: testUserID, PrimaryEmail: "person@example.com"}
	registered := registerCredential(t, verifier, authenticator, user)
	stored := passkeyCredential(user.ID, registered)
	challenge := testChallenge(9)
	options, err := verifier.CreateAuthenticationOptions(context.Background(), auth.AuthenticationOptionsInput{
		Challenge:          challenge,
		User:               &user,
		AllowCredentialIDs: [][]byte{authenticator.credentialID},
	})
	if err != nil {
		t.Fatalf("CreateAuthenticationOptions() error = %v", err)
	}
	validResponse := func() json.RawMessage {
		return authenticator.authenticationResponse(t, authenticationResponseConfig{
			challenge:  challenge,
			origin:     testOrigin,
			rpID:       testRPID,
			signCount:  1,
			userHandle: []byte(user.ID),
		})
	}

	t.Run("stored normalized credential must match opaque record", func(t *testing.T) {
		mutations := []func(*auth.PasskeyCredential){
			func(c *auth.PasskeyCredential) { c.CredentialID = []byte("different") },
			func(c *auth.PasskeyCredential) { c.PublicKey = []byte("different") },
			func(c *auth.PasskeyCredential) { c.SignCount++ },
		}
		for index, mutate := range mutations {
			credential := stored
			mutate(&credential)
			_, err := verifier.VerifyAuthentication(context.Background(), auth.AuthenticationVerificationInput{
				Challenge: challenge, VerifierSession: options.Session, Credential: credential, Response: validResponse(),
			})
			if !errors.Is(err, ErrInvalidCredential) {
				t.Errorf("mutation %d error = %v, want ErrInvalidCredential", index, err)
			}
		}
	})

	tests := []struct {
		name       string
		session    func() json.RawMessage
		credential func() auth.PasskeyCredential
		response   func() json.RawMessage
		wantErr    error
		wantAlso   error
	}{
		{name: "malformed session", session: func() json.RawMessage { return json.RawMessage(`{`) }, credential: func() auth.PasskeyCredential { return stored }, response: validResponse, wantErr: ErrInvalidSession},
		{name: "challenge not bound to session", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: validResponse, wantErr: ErrInvalidSession},
		{name: "authentication user not bound to session", session: func() json.RawMessage {
			var session lib.SessionData
			decodeJSON(t, options.Session, &session)
			session.UserID = []byte("other-user")
			return marshalJSON(t, session)
		}, credential: func() auth.PasskeyCredential { return stored }, response: validResponse, wantErr: ErrInvalidSession},
		{name: "expired session", session: func() json.RawMessage { return expiredSession(t, options.Session) }, credential: func() auth.PasskeyCredential { return stored }, response: validResponse, wantErr: ErrVerification},
		{name: "malformed credential record", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential {
			credential := stored
			credential.VerifierCredential = json.RawMessage(`{`)
			return credential
		}, response: validResponse, wantErr: ErrInvalidCredential},
		{name: "incomplete credential", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential {
			credential := stored
			credential.UserID = ""
			return credential
		}, response: validResponse, wantErr: ErrInvalidInput},
		{name: "malformed response", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage { return json.RawMessage(`{`) }, wantErr: ErrVerification},
		{name: "response challenge mismatch", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: testChallenge(10), origin: testOrigin, rpID: testRPID, signCount: 1, userHandle: []byte(user.ID)})
		}, wantErr: ErrVerification},
		{name: "origin outside exact allowlist", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: "https://login.example.com", rpID: testRPID, signCount: 1, userHandle: []byte(user.ID)})
		}, wantErr: ErrVerification},
		{name: "rp id hash mismatch", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: testOrigin, rpID: "other.example.com", signCount: 1, userHandle: []byte(user.ID)})
		}, wantErr: ErrVerification},
		{name: "user verification missing", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, flags: byte(protocol.FlagUserPresent), signCount: 1, userHandle: []byte(user.ID)})
		}, wantErr: ErrVerification},
		{name: "user presence missing", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, flags: byte(protocol.FlagUserVerified), signCount: 1, userHandle: []byte(user.ID)})
		}, wantErr: ErrVerification},
		{name: "signature invalid", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, signCount: 1, userHandle: []byte(user.ID), corruptSignature: true})
		}, wantErr: ErrVerification},
		{name: "credential id mismatch", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, signCount: 1, userHandle: []byte(user.ID), credentialID: []byte("different")})
		}, wantErr: ErrVerification},
		{name: "user handle mismatch", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential { return stored }, response: func() json.RawMessage {
			return authenticator.authenticationResponse(t, authenticationResponseConfig{challenge: challenge, origin: testOrigin, rpID: testRPID, signCount: 1, userHandle: []byte("other-user")})
		}, wantErr: ErrVerification},
		{name: "non advancing counter", session: func() json.RawMessage { return options.Session }, credential: func() auth.PasskeyCredential {
			credential := stored
			var record lib.Credential
			decodeJSON(t, credential.VerifierCredential, &record)
			record.Authenticator.SignCount = 1
			credential.SignCount = 1
			credential.VerifierCredential = marshalJSON(t, record)
			return credential
		}, response: validResponse, wantErr: ErrVerification, wantAlso: auth.ErrInvalidSignCount},
	}

	if _, err := verifier.VerifyAuthentication(nil, auth.AuthenticationVerificationInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("VerifyAuthentication(nil context) error = %v, want ErrInvalidInput", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputChallenge := challenge
			if tt.name == "challenge not bound to session" {
				inputChallenge = testChallenge(10)
			}
			_, err := verifier.VerifyAuthentication(context.Background(), auth.AuthenticationVerificationInput{
				Challenge:       inputChallenge,
				VerifierSession: tt.session(),
				Credential:      tt.credential(),
				Response:        tt.response(),
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantAlso != nil && !errors.Is(err, tt.wantAlso) {
				t.Fatalf("error = %v, also want %v", err, tt.wantAlso)
			}
		})
	}
}

func TestDiscoverableAuthenticationRejectsMissingOrForeignIdentity(t *testing.T) {
	t.Parallel()

	verifier := newTestVerifier(t)
	authenticator := newVirtualAuthenticator(t)
	user := auth.User{ID: testUserID, PrimaryEmail: "person@example.com"}
	stored := passkeyCredential(user.ID, registerCredential(t, verifier, authenticator, user))
	challenge := testChallenge(11)
	options, err := verifier.CreateAuthenticationOptions(context.Background(), auth.AuthenticationOptionsInput{Challenge: challenge})
	if err != nil {
		t.Fatalf("CreateAuthenticationOptions() error = %v", err)
	}

	for _, tt := range []struct {
		name         string
		userHandle   []byte
		credentialID []byte
	}{
		{name: "missing user handle", userHandle: nil},
		{name: "foreign user handle", userHandle: []byte("other-user")},
		{name: "foreign credential", userHandle: []byte(user.ID), credentialID: []byte("other-credential")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := authenticator.authenticationResponse(t, authenticationResponseConfig{
				challenge:    challenge,
				origin:       testOrigin,
				rpID:         testRPID,
				signCount:    1,
				userHandle:   tt.userHandle,
				credentialID: tt.credentialID,
			})
			_, err := verifier.VerifyAuthentication(context.Background(), auth.AuthenticationVerificationInput{
				Challenge: challenge, VerifierSession: options.Session, Credential: stored, Response: response,
			})
			if !errors.Is(err, ErrVerification) {
				t.Fatalf("error = %v, want ErrVerification", err)
			}
		})
	}
}

func newTestVerifier(t *testing.T) *Verifier {
	t.Helper()
	verifier, err := New(Config{
		RPID:             testRPID,
		RPDisplayName:    "Jandibat",
		RPOrigins:        []string{testOrigin},
		UserVerification: "required",
		ResidentKey:      "required",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return verifier
}

func registerCredential(t *testing.T, verifier *Verifier, authenticator *virtualAuthenticator, user auth.User) auth.RegistrationVerification {
	t.Helper()
	challenge := testChallenge(4)
	options, err := verifier.CreateRegistrationOptions(context.Background(), auth.RegistrationOptionsInput{Challenge: challenge, User: user})
	if err != nil {
		t.Fatalf("CreateRegistrationOptions() error = %v", err)
	}
	verified, err := verifier.VerifyRegistration(context.Background(), auth.RegistrationVerificationInput{
		Challenge:       challenge,
		User:            user,
		VerifierSession: options.Session,
		Response: authenticator.registrationResponse(t, registrationResponseConfig{
			challenge: challenge,
			origin:    testOrigin,
			rpID:      testRPID,
			flags:     byte(protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagAttestedCredentialData),
		}),
	})
	if err != nil {
		t.Fatalf("VerifyRegistration(signed self-attestation) error = %v", err)
	}
	return verified
}

func passkeyCredential(userID string, registration auth.RegistrationVerification) auth.PasskeyCredential {
	return auth.PasskeyCredential{
		UserID:             userID,
		CredentialID:       append([]byte(nil), registration.CredentialID...),
		PublicKey:          append([]byte(nil), registration.PublicKey...),
		AAGUID:             append([]byte(nil), registration.AAGUID...),
		SignCount:          registration.SignCount,
		Transports:         append([]string(nil), registration.Transports...),
		VerifierCredential: append(json.RawMessage(nil), registration.VerifierCredential...),
	}
}

func assertAuthenticationOptions(t *testing.T, options auth.PasskeyVerifierOptions, challenge, rpID string, credentialID, userID []byte) {
	t.Helper()
	var browser struct {
		PublicKey struct {
			Challenge        string `json:"challenge"`
			RPID             string `json:"rpId"`
			UserVerification string `json:"userVerification"`
			AllowCredentials []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	decodeJSON(t, options.PublicKey, &browser)
	if browser.PublicKey.Challenge != challenge || browser.PublicKey.RPID != rpID || browser.PublicKey.UserVerification != "required" {
		t.Errorf("browser authentication options = %+v", browser.PublicKey)
	}
	if credentialID == nil {
		if len(browser.PublicKey.AllowCredentials) != 0 {
			t.Errorf("discoverable allow credentials = %+v, want none", browser.PublicKey.AllowCredentials)
		}
	} else if got := browser.PublicKey.AllowCredentials; len(got) != 1 || got[0].Type != "public-key" || got[0].ID != base64.RawURLEncoding.EncodeToString(credentialID) {
		t.Errorf("allow credentials = %+v", got)
	}
	var session lib.SessionData
	decodeJSON(t, options.Session, &session)
	if session.Challenge != challenge || !bytes.Equal(session.UserID, userID) {
		t.Errorf("authentication session = %+v", session)
	}
	if credentialID == nil {
		if len(session.AllowedCredentialIDs) != 0 {
			t.Errorf("discoverable session allow credentials = %x, want none", session.AllowedCredentialIDs)
		}
	} else if len(session.AllowedCredentialIDs) != 1 || !bytes.Equal(session.AllowedCredentialIDs[0], credentialID) {
		t.Errorf("session allow credentials = %x, want [%x]", session.AllowedCredentialIDs, credentialID)
	}
}

func expiredSession(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var session lib.SessionData
	decodeJSON(t, raw, &session)
	session.Expires = time.Now().Add(-time.Minute)
	return marshalJSON(t, session)
}

func testChallenge(seed byte) string {
	value := bytes.Repeat([]byte{seed}, 32)
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeJSON(t *testing.T, raw []byte, output any) {
	t.Helper()
	if err := json.Unmarshal(raw, output); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", raw, err)
	}
}

func marshalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

type virtualAuthenticator struct {
	privateKey   *ecdsa.PrivateKey
	credentialID []byte
	aaguid       []byte
}

func newVirtualAuthenticator(t *testing.T) *virtualAuthenticator {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	credentialID := make([]byte, 32)
	if _, err := rand.Read(credentialID); err != nil {
		t.Fatalf("rand.Read(credential ID) error = %v", err)
	}
	aaguid := make([]byte, 16)
	if _, err := rand.Read(aaguid); err != nil {
		t.Fatalf("rand.Read(AAGUID) error = %v", err)
	}
	return &virtualAuthenticator{privateKey: privateKey, credentialID: credentialID, aaguid: aaguid}
}

type registrationResponseConfig struct {
	challenge        string
	origin           string
	rpID             string
	flags            byte
	corruptSignature bool
}

func (a *virtualAuthenticator) registrationResponse(t *testing.T, config registrationResponseConfig) json.RawMessage {
	t.Helper()
	clientData := marshalJSON(t, struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}{Type: "webauthn.create", Challenge: config.challenge, Origin: config.origin})
	credentialPublicKey := a.cosePublicKey(t)
	authenticatorData := make([]byte, 0, 37+16+2+len(a.credentialID)+len(credentialPublicKey))
	rpHash := sha256.Sum256([]byte(config.rpID))
	authenticatorData = append(authenticatorData, rpHash[:]...)
	authenticatorData = append(authenticatorData, config.flags)
	authenticatorData = binary.BigEndian.AppendUint32(authenticatorData, 0)
	authenticatorData = append(authenticatorData, a.aaguid...)
	authenticatorData = binary.BigEndian.AppendUint16(authenticatorData, uint16(len(a.credentialID)))
	authenticatorData = append(authenticatorData, a.credentialID...)
	authenticatorData = append(authenticatorData, credentialPublicKey...)
	signature := a.sign(t, authenticatorData, clientData)
	if config.corruptSignature {
		signature[len(signature)-1] ^= 0xff
	}
	attestationObject, err := cbor.Marshal(map[string]any{
		"fmt":      "packed",
		"authData": authenticatorData,
		"attStmt": map[string]any{
			"alg": int64(-7),
			"sig": signature,
		},
	})
	if err != nil {
		t.Fatalf("cbor.Marshal(attestation object) error = %v", err)
	}
	credentialID := base64.RawURLEncoding.EncodeToString(a.credentialID)
	return marshalJSON(t, map[string]any{
		"id":    credentialID,
		"rawId": credentialID,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestationObject),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
}

type authenticationResponseConfig struct {
	challenge        string
	origin           string
	rpID             string
	flags            byte
	signCount        uint32
	userHandle       []byte
	credentialID     []byte
	corruptSignature bool
}

func (a *virtualAuthenticator) authenticationResponse(t *testing.T, config authenticationResponseConfig) json.RawMessage {
	t.Helper()
	if config.flags == 0 {
		config.flags = byte(protocol.FlagUserPresent | protocol.FlagUserVerified)
	}
	credentialID := config.credentialID
	if credentialID == nil {
		credentialID = a.credentialID
	}
	clientData := marshalJSON(t, struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}{Type: "webauthn.get", Challenge: config.challenge, Origin: config.origin})
	rpHash := sha256.Sum256([]byte(config.rpID))
	authenticatorData := make([]byte, 0, 37)
	authenticatorData = append(authenticatorData, rpHash[:]...)
	authenticatorData = append(authenticatorData, config.flags)
	authenticatorData = binary.BigEndian.AppendUint32(authenticatorData, config.signCount)
	signature := a.sign(t, authenticatorData, clientData)
	if config.corruptSignature {
		signature[len(signature)-1] ^= 0xff
	}
	encodedID := base64.RawURLEncoding.EncodeToString(credentialID)
	response := map[string]any{
		"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
		"authenticatorData": base64.RawURLEncoding.EncodeToString(authenticatorData),
		"signature":         base64.RawURLEncoding.EncodeToString(signature),
	}
	if config.userHandle != nil {
		response["userHandle"] = base64.RawURLEncoding.EncodeToString(config.userHandle)
	}
	return marshalJSON(t, map[string]any{
		"id":                     encodedID,
		"rawId":                  encodedID,
		"type":                   "public-key",
		"response":               response,
		"clientExtensionResults": map[string]any{},
	})
}

func (a *virtualAuthenticator) cosePublicKey(t *testing.T) []byte {
	t.Helper()
	x := a.privateKey.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.privateKey.PublicKey.Y.FillBytes(make([]byte, 32))
	encoded, err := cbor.Marshal(map[int]any{
		1:  int64(2),
		3:  int64(-7),
		-1: int64(1),
		-2: x,
		-3: y,
	})
	if err != nil {
		t.Fatalf("cbor.Marshal(COSE public key) error = %v", err)
	}
	return encoded
}

func (a *virtualAuthenticator) sign(t *testing.T, authenticatorData, clientData []byte) []byte {
	t.Helper()
	clientHash := sha256.Sum256(clientData)
	signed := make([]byte, 0, len(authenticatorData)+len(clientHash))
	signed = append(signed, authenticatorData...)
	signed = append(signed, clientHash[:]...)
	digest := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, a.privateKey, digest[:])
	if err != nil {
		t.Fatalf("ecdsa.SignASN1() error = %v", err)
	}
	return signature
}
