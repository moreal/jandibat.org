// Package webauthn implements auth.PasskeyVerifier with the conformant
// go-webauthn relying-party library.
package webauthn

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	lib "github.com/go-webauthn/webauthn/webauthn"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
)

const (
	defaultTimeout = 5 * time.Minute
	maxUserHandle  = 64
)

var (
	ErrInvalidConfig     = errors.New("webauthn: invalid configuration")
	ErrInvalidInput      = errors.New("webauthn: invalid input")
	ErrInvalidSession    = errors.New("webauthn: invalid verifier session")
	ErrInvalidCredential = errors.New("webauthn: invalid verifier credential")
	ErrVerification      = errors.New("webauthn: verification failed")
)

// Config is the relying-party policy enforced for both registration and
// authentication. RPOrigins must contain exact browser origins, including the
// scheme and any non-default port. RPID is a host name without scheme or port.
type Config struct {
	RPID             string
	RPDisplayName    string
	RPOrigins        []string
	UserVerification string
	ResidentKey      string
	Attestation      string
	Timeout          time.Duration
}

type Verifier struct {
	webAuthn *lib.WebAuthn
}

// New validates and fixes the RP policy once. The resulting verifier never
// derives origin or RP ID from an untrusted request.
func New(config Config) (*Verifier, error) {
	rpID, origins, err := validateRelyingParty(config.RPID, config.RPOrigins)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(config.RPDisplayName)
	if displayName == "" {
		return nil, fmt.Errorf("%w: RP display name is required", ErrInvalidConfig)
	}

	verification, err := userVerification(config.UserVerification)
	if err != nil {
		return nil, err
	}
	residentKey, err := residentKeyRequirement(config.ResidentKey)
	if err != nil {
		return nil, err
	}
	attestation, err := attestationPreference(config.Attestation)
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < time.Second {
		return nil, fmt.Errorf("%w: timeout must be at least one second", ErrInvalidConfig)
	}

	selection := protocol.AuthenticatorSelection{
		UserVerification: verification,
		ResidentKey:      residentKey,
	}
	switch residentKey {
	case protocol.ResidentKeyRequirementRequired:
		selection.RequireResidentKey = protocol.ResidentKeyRequired()
	default:
		selection.RequireResidentKey = protocol.ResidentKeyNotRequired()
	}

	implementation, err := lib.New(&lib.Config{
		RPID:                   rpID,
		RPDisplayName:          displayName,
		RPOrigins:              origins,
		AttestationPreference:  attestation,
		AuthenticatorSelection: selection,
		Timeouts: lib.TimeoutsConfig{
			Registration: lib.TimeoutConfig{Enforce: true, Timeout: timeout, TimeoutUVD: timeout},
			Login:        lib.TimeoutConfig{Enforce: true, Timeout: timeout, TimeoutUVD: timeout},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return &Verifier{webAuthn: implementation}, nil
}

func (v *Verifier) CreateRegistrationOptions(ctx context.Context, input auth.RegistrationOptionsInput) (auth.PasskeyVerifierOptions, error) {
	if err := checkContext(ctx); err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}
	challenge, err := decodeChallenge(input.Challenge)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}
	user, err := registrationUser(input.User)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}
	exclusions, err := credentialDescriptors(input.ExcludeCredentialIDs)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}

	creation, session, err := v.webAuthn.BeginRegistration(
		user,
		lib.WithExclusions(exclusions),
		func(options *protocol.PublicKeyCredentialCreationOptions) {
			options.Challenge = protocol.URLEncodedBase64(challenge)
		},
	)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, fmt.Errorf("create registration options: %w", err)
	}
	// BeginRegistration creates its own challenge before applying options. Keep
	// the opaque session exactly aligned with the challenge sent to the browser.
	session.Challenge = input.Challenge
	return marshalOptions(creation, session)
}

func (v *Verifier) VerifyRegistration(ctx context.Context, input auth.RegistrationVerificationInput) (auth.RegistrationVerification, error) {
	if err := checkContext(ctx); err != nil {
		return auth.RegistrationVerification{}, err
	}
	user, err := registrationUser(input.User)
	if err != nil {
		return auth.RegistrationVerification{}, err
	}
	session, err := decodeSession(input.VerifierSession, input.Challenge)
	if err != nil {
		return auth.RegistrationVerification{}, err
	}
	if !bytes.Equal(session.UserID, user.WebAuthnID()) {
		return auth.RegistrationVerification{}, fmt.Errorf("%w: registration user handle does not match session", ErrInvalidSession)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(input.Response))
	if err != nil {
		return auth.RegistrationVerification{}, fmt.Errorf("%w: parse registration response: %v", ErrVerification, err)
	}
	credential, err := v.webAuthn.CreateCredential(user, session, parsed)
	if err != nil {
		return auth.RegistrationVerification{}, fmt.Errorf("%w: registration: %v", ErrVerification, err)
	}
	credentialRecord, err := json.Marshal(credential)
	if err != nil {
		return auth.RegistrationVerification{}, fmt.Errorf("encode credential record: %w", err)
	}
	return auth.RegistrationVerification{
		CredentialID:       append([]byte(nil), credential.ID...),
		PublicKey:          append([]byte(nil), credential.PublicKey...),
		AAGUID:             append([]byte(nil), credential.Authenticator.AAGUID...),
		SignCount:          credential.Authenticator.SignCount,
		Transports:         transportStrings(credential.Transport),
		VerifierCredential: credentialRecord,
	}, nil
}

func (v *Verifier) CreateAuthenticationOptions(ctx context.Context, input auth.AuthenticationOptionsInput) (auth.PasskeyVerifierOptions, error) {
	if err := checkContext(ctx); err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}
	challenge, err := decodeChallenge(input.Challenge)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}
	descriptors, err := credentialDescriptors(input.AllowCredentialIDs)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, err
	}

	challengeOption := func(options *protocol.PublicKeyCredentialRequestOptions) {
		options.Challenge = protocol.URLEncodedBase64(challenge)
	}
	var assertion *protocol.CredentialAssertion
	var session *lib.SessionData
	if input.User == nil {
		if len(descriptors) != 0 {
			return auth.PasskeyVerifierOptions{}, fmt.Errorf("%w: discoverable login cannot have an allow list", ErrInvalidInput)
		}
		assertion, session, err = v.webAuthn.BeginDiscoverableLogin(challengeOption)
	} else {
		user, userErr := loginOptionsUser(*input.User, input.AllowCredentialIDs)
		if userErr != nil {
			return auth.PasskeyVerifierOptions{}, userErr
		}
		assertion, session, err = v.webAuthn.BeginLogin(user, challengeOption)
	}
	if err != nil {
		return auth.PasskeyVerifierOptions{}, fmt.Errorf("create authentication options: %w", err)
	}
	session.Challenge = input.Challenge
	return marshalOptions(assertion, session)
}

func (v *Verifier) VerifyAuthentication(ctx context.Context, input auth.AuthenticationVerificationInput) (auth.AuthenticationVerification, error) {
	if err := checkContext(ctx); err != nil {
		return auth.AuthenticationVerification{}, err
	}
	if err := validateStoredCredential(input.Credential); err != nil {
		return auth.AuthenticationVerification{}, err
	}
	session, err := decodeSession(input.VerifierSession, input.Challenge)
	if err != nil {
		return auth.AuthenticationVerification{}, err
	}
	credential, err := decodeCredential(input.Credential)
	if err != nil {
		return auth.AuthenticationVerification{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(input.Response))
	if err != nil {
		return auth.AuthenticationVerification{}, fmt.Errorf("%w: parse authentication response: %v", ErrVerification, err)
	}
	user := &libraryUser{
		id:          []byte(input.Credential.UserID),
		name:        input.Credential.UserID,
		displayName: input.Credential.UserID,
		credentials: []lib.Credential{credential},
	}

	var verified *lib.Credential
	if session.UserID == nil {
		verified, err = v.webAuthn.ValidateDiscoverableLogin(
			func(rawID, userHandle []byte) (lib.User, error) {
				if !bytes.Equal(rawID, input.Credential.CredentialID) {
					return nil, fmt.Errorf("credential ID does not match")
				}
				if !bytes.Equal(userHandle, user.id) {
					return nil, fmt.Errorf("user handle does not match credential owner")
				}
				return user, nil
			},
			session,
			parsed,
		)
	} else {
		if !bytes.Equal(session.UserID, user.id) {
			return auth.AuthenticationVerification{}, fmt.Errorf("%w: authentication user handle does not match session", ErrInvalidSession)
		}
		verified, err = v.webAuthn.ValidateLogin(user, session, parsed)
	}
	if err != nil {
		return auth.AuthenticationVerification{}, fmt.Errorf("%w: authentication: %v", ErrVerification, err)
	}
	if verified.Authenticator.CloneWarning {
		return auth.AuthenticationVerification{}, errors.Join(
			ErrVerification,
			fmt.Errorf("%w: authenticator signature counter did not advance", auth.ErrInvalidSignCount),
		)
	}
	credentialRecord, err := json.Marshal(verified)
	if err != nil {
		return auth.AuthenticationVerification{}, fmt.Errorf("encode updated credential record: %w", err)
	}
	return auth.AuthenticationVerification{
		SignCount:          verified.Authenticator.SignCount,
		UserHandle:         append([]byte(nil), parsed.Response.UserHandle...),
		VerifierCredential: credentialRecord,
	}, nil
}

func marshalOptions(value any, session *lib.SessionData) (auth.PasskeyVerifierOptions, error) {
	publicKey, err := json.Marshal(value)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, fmt.Errorf("encode browser options: %w", err)
	}
	verifierSession, err := json.Marshal(session)
	if err != nil {
		return auth.PasskeyVerifierOptions{}, fmt.Errorf("encode verifier session: %w", err)
	}
	return auth.PasskeyVerifierOptions{PublicKey: publicKey, Session: verifierSession}, nil
}

func decodeSession(raw json.RawMessage, challenge string) (lib.SessionData, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return lib.SessionData{}, fmt.Errorf("%w: malformed JSON", ErrInvalidSession)
	}
	var session lib.SessionData
	if err := json.Unmarshal(raw, &session); err != nil {
		return lib.SessionData{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	if challenge == "" || subtle.ConstantTimeCompare([]byte(session.Challenge), []byte(challenge)) != 1 {
		return lib.SessionData{}, fmt.Errorf("%w: challenge mismatch", ErrInvalidSession)
	}
	if _, err := decodeChallenge(session.Challenge); err != nil {
		return lib.SessionData{}, fmt.Errorf("%w: invalid challenge", ErrInvalidSession)
	}
	if session.UserID != nil && (len(session.UserID) == 0 || len(session.UserID) > maxUserHandle) {
		return lib.SessionData{}, fmt.Errorf("%w: invalid user handle", ErrInvalidSession)
	}
	return session, nil
}

func decodeCredential(stored auth.PasskeyCredential) (lib.Credential, error) {
	var credential lib.Credential
	if err := json.Unmarshal(stored.VerifierCredential, &credential); err != nil {
		return lib.Credential{}, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	if !bytes.Equal(credential.ID, stored.CredentialID) ||
		!bytes.Equal(credential.PublicKey, stored.PublicKey) ||
		credential.Authenticator.SignCount != stored.SignCount {
		return lib.Credential{}, fmt.Errorf("%w: normalized fields do not match credential record", ErrInvalidCredential)
	}
	if len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
		return lib.Credential{}, fmt.Errorf("%w: incomplete credential record", ErrInvalidCredential)
	}
	return credential, nil
}

func validateStoredCredential(credential auth.PasskeyCredential) error {
	if strings.TrimSpace(credential.UserID) == "" || len(credential.CredentialID) == 0 || len(credential.PublicKey) == 0 {
		return fmt.Errorf("%w: incomplete credential", ErrInvalidInput)
	}
	if len(credential.UserID) > maxUserHandle {
		return fmt.Errorf("%w: user handle exceeds 64 bytes", ErrInvalidInput)
	}
	if len(credential.VerifierCredential) == 0 || !json.Valid(credential.VerifierCredential) {
		return fmt.Errorf("%w: malformed JSON", ErrInvalidCredential)
	}
	return nil
}

func registrationUser(user auth.User) (*libraryUser, error) {
	id := []byte(strings.TrimSpace(user.ID))
	if len(id) == 0 || len(id) > maxUserHandle {
		return nil, fmt.Errorf("%w: user ID must contain 1 to 64 bytes", ErrInvalidInput)
	}
	name := strings.TrimSpace(user.PrimaryEmail)
	if name == "" {
		name = user.ID
	}
	return &libraryUser{id: id, name: name, displayName: name}, nil
}

func loginOptionsUser(user auth.User, credentialIDs [][]byte) (*libraryUser, error) {
	result, err := registrationUser(user)
	if err != nil {
		return nil, err
	}
	if len(credentialIDs) == 0 {
		return nil, fmt.Errorf("%w: user-bound login requires at least one credential", ErrInvalidInput)
	}
	result.credentials = make([]lib.Credential, len(credentialIDs))
	for index, id := range credentialIDs {
		result.credentials[index] = lib.Credential{ID: append([]byte(nil), id...)}
	}
	return result, nil
}

func credentialDescriptors(ids [][]byte) ([]protocol.CredentialDescriptor, error) {
	descriptors := make([]protocol.CredentialDescriptor, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		if len(id) == 0 {
			return nil, fmt.Errorf("%w: credential ID cannot be empty", ErrInvalidInput)
		}
		key := string(id)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicate credential ID", ErrInvalidInput)
		}
		seen[key] = struct{}{}
		descriptors[index] = protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: append([]byte(nil), id...),
		}
	}
	return descriptors, nil
}

func decodeChallenge(value string) ([]byte, error) {
	if strings.TrimSpace(value) != value || value == "" {
		return nil, fmt.Errorf("%w: challenge is required", ErrInvalidInput)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < 16 {
		return nil, fmt.Errorf("%w: challenge must be at least 16 base64url-encoded bytes", ErrInvalidInput)
	}
	return decoded, nil
}

func validateRelyingParty(rawID string, rawOrigins []string) (string, []string, error) {
	rpID := strings.ToLower(strings.TrimSpace(rawID))
	if rpID == "" || strings.Contains(rpID, "://") || strings.ContainsAny(rpID, "/?#@") {
		return "", nil, fmt.Errorf("%w: RP ID must be a host without scheme, port, path, query, or fragment", ErrInvalidConfig)
	}
	if net.ParseIP(rpID) == nil && strings.Contains(rpID, ":") {
		return "", nil, fmt.Errorf("%w: RP ID must not contain a port", ErrInvalidConfig)
	}
	if len(rawOrigins) == 0 {
		return "", nil, fmt.Errorf("%w: at least one RP origin is required", ErrInvalidConfig)
	}
	origins := make([]string, 0, len(rawOrigins))
	seen := make(map[string]struct{}, len(rawOrigins))
	for _, rawOrigin := range rawOrigins {
		parsed, err := url.ParseRequestURI(strings.TrimSpace(rawOrigin))
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", nil, fmt.Errorf("%w: RP origin %q must contain only scheme, host, and optional port", ErrInvalidConfig, rawOrigin)
		}
		host := strings.ToLower(parsed.Hostname())
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(host)) {
			return "", nil, fmt.Errorf("%w: RP origin %q must use HTTPS except on loopback", ErrInvalidConfig, rawOrigin)
		}
		if !rpIDMatchesHost(rpID, host) {
			return "", nil, fmt.Errorf("%w: RP ID %q is not an effective domain of origin host %q", ErrInvalidConfig, rpID, host)
		}
		origin, err := protocol.FullyQualifiedOrigin(parsed.String())
		if err != nil {
			return "", nil, fmt.Errorf("%w: invalid RP origin %q", ErrInvalidConfig, rawOrigin)
		}
		origin = strings.ToLower(origin)
		if _, exists := seen[origin]; exists {
			return "", nil, fmt.Errorf("%w: duplicate RP origin %q", ErrInvalidConfig, rawOrigin)
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return rpID, origins, nil
}

func rpIDMatchesHost(rpID, host string) bool {
	if parsed := net.ParseIP(rpID); parsed != nil {
		return parsed.Equal(net.ParseIP(host))
	}
	return host == rpID || strings.HasSuffix(host, "."+rpID)
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func userVerification(value string) (protocol.UserVerificationRequirement, error) {
	switch protocol.UserVerificationRequirement(strings.TrimSpace(value)) {
	case "", protocol.VerificationRequired:
		return protocol.VerificationRequired, nil
	case protocol.VerificationPreferred:
		return protocol.VerificationPreferred, nil
	case protocol.VerificationDiscouraged:
		return protocol.VerificationDiscouraged, nil
	default:
		return "", fmt.Errorf("%w: invalid user verification requirement", ErrInvalidConfig)
	}
}

func residentKeyRequirement(value string) (protocol.ResidentKeyRequirement, error) {
	switch protocol.ResidentKeyRequirement(strings.TrimSpace(value)) {
	case "", protocol.ResidentKeyRequirementRequired:
		return protocol.ResidentKeyRequirementRequired, nil
	case protocol.ResidentKeyRequirementPreferred:
		return protocol.ResidentKeyRequirementPreferred, nil
	case protocol.ResidentKeyRequirementDiscouraged:
		return protocol.ResidentKeyRequirementDiscouraged, nil
	default:
		return "", fmt.Errorf("%w: invalid resident-key requirement", ErrInvalidConfig)
	}
}

func attestationPreference(value string) (protocol.ConveyancePreference, error) {
	switch protocol.ConveyancePreference(strings.TrimSpace(value)) {
	case "", protocol.PreferNoAttestation:
		return protocol.PreferNoAttestation, nil
	case protocol.PreferIndirectAttestation:
		return protocol.PreferIndirectAttestation, nil
	case protocol.PreferDirectAttestation:
		return protocol.PreferDirectAttestation, nil
	case protocol.PreferEnterpriseAttestation:
		return protocol.PreferEnterpriseAttestation, nil
	default:
		return "", fmt.Errorf("%w: invalid attestation preference", ErrInvalidConfig)
	}
}

func transportStrings(transports []protocol.AuthenticatorTransport) []string {
	result := make([]string, len(transports))
	for index, transport := range transports {
		result[index] = string(transport)
	}
	return result
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidInput)
	}
	return ctx.Err()
}

type libraryUser struct {
	id          []byte
	name        string
	displayName string
	credentials []lib.Credential
}

func (u *libraryUser) WebAuthnID() []byte                    { return u.id }
func (u *libraryUser) WebAuthnName() string                  { return u.name }
func (u *libraryUser) WebAuthnDisplayName() string           { return u.displayName }
func (u *libraryUser) WebAuthnCredentials() []lib.Credential { return u.credentials }
func (u *libraryUser) WebAuthnIcon() string                  { return "" }

var _ auth.PasskeyVerifier = (*Verifier)(nil)
