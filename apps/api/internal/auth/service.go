package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moreal/jandibat.org/apps/api/internal/identity"
)

const (
	defaultMagicLinkTTL  = 15 * time.Minute
	defaultSessionTTL    = 30 * 24 * time.Hour
	defaultCeremonyTTL   = 5 * time.Minute
	maxBearerTokenBytes  = 512
	maxPasskeyLabelRunes = 100
)

type Config struct {
	MagicLinkTTL time.Duration
	SessionTTL   time.Duration
	CeremonyTTL  time.Duration
	Random       io.Reader
	Now          func() time.Time
}

type Dependencies struct {
	Repository     Repository
	Mailer         Mailer
	Verifier       PasskeyVerifier
	SecurityEvents SecurityEventRecorder
	Deliveries     MagicLinkDeliveryIssuer
}

type Service struct {
	repository     Repository
	mailer         Mailer
	verifier       PasskeyVerifier
	securityEvents SecurityEventRecorder
	deliveries     MagicLinkDeliveryIssuer
	magicLinkTTL   time.Duration
	sessionTTL     time.Duration
	ceremonyTTL    time.Duration
	random         io.Reader
	now            func() time.Time
}

func NewService(deps Dependencies, config Config) (*Service, error) {
	if deps.Repository == nil || deps.Deliveries == nil && deps.Mailer == nil {
		return nil, fmt.Errorf("%w: repository and either mailer or async delivery issuer are required", ErrInvalidInput)
	}

	if config.MagicLinkTTL == 0 {
		config.MagicLinkTTL = defaultMagicLinkTTL
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.CeremonyTTL == 0 {
		config.CeremonyTTL = defaultCeremonyTTL
	}
	if config.MagicLinkTTL < 0 || config.SessionTTL < 0 || config.CeremonyTTL < 0 {
		return nil, fmt.Errorf("%w: TTLs must be positive", ErrInvalidInput)
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Service{
		repository:     deps.Repository,
		mailer:         deps.Mailer,
		verifier:       deps.Verifier,
		securityEvents: deps.SecurityEvents,
		deliveries:     deps.Deliveries,
		magicLinkTTL:   config.MagicLinkTTL,
		sessionTTL:     config.SessionTTL,
		ceremonyTTL:    config.CeremonyTTL,
		random:         config.Random,
		now:            config.Now,
	}, nil
}

// RequestMagicLink persists only a secret-free delivery intent when an async
// issuer is configured. The worker later mints the single-use token and stores
// only its SHA-256 digest. The direct-mail fallback is development-only and
// hands its raw token straight to the in-process mailer.
func (s *Service) RequestMagicLink(ctx context.Context, email, redirectURI string) error {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return err
	}

	now := s.now().UTC()
	if s.deliveries != nil {
		deliveryID, err := s.randomUUID()
		if err != nil {
			return err
		}
		delivery := MagicLinkDelivery{
			ID: deliveryID, RecipientEmail: normalized, RedirectURI: redirectURI, Purpose: MagicLinkPurposeSignIn,
			Status: MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.deliveries.SaveMagicLinkDeliveryIntent(ctx, delivery); err != nil {
			return fmt.Errorf("save magic link delivery intent: %w", err)
		}
		return nil
	}
	id, err := s.randomUUID()
	if err != nil {
		return err
	}
	token, err := s.randomToken(32)
	if err != nil {
		return err
	}
	link := MagicLink{
		ID: id, Email: normalized, TokenHash: tokenDigest(token), Purpose: MagicLinkPurposeSignIn,
		CreatedAt: now, ExpiresAt: now.Add(s.magicLinkTTL),
	}
	if err := s.repository.SaveMagicLink(ctx, link); err != nil {
		return fmt.Errorf("save magic link: %w", err)
	}

	if err := s.mailer.SendMagicLink(ctx, MagicLinkMail{
		Email:       normalized,
		Token:       token,
		ExpiresAt:   link.ExpiresAt,
		RedirectURI: redirectURI,
	}); err != nil {
		invalidateErr := s.repository.InvalidateMagicLink(ctx, link.TokenHash, now)
		if invalidateErr != nil {
			return errors.Join(fmt.Errorf("send magic link: %w", err), fmt.Errorf("invalidate unsent magic link: %w", invalidateErr))
		}
		return fmt.Errorf("send magic link: %w", err)
	}
	return nil
}

func (s *Service) CompleteMagicLink(ctx context.Context, token string, metadata SessionMetadata) (SessionGrant, error) {
	if !validBearerToken(token) {
		return SessionGrant{}, ErrInvalidMagicLink
	}

	// Prepare all randomness before consuming the one-time token. Repository
	// failures can still occur, but entropy-source failures cannot strand it.
	userID, err := s.randomID("usr_", 18)
	if err != nil {
		return SessionGrant{}, err
	}
	pending, grant, err := s.prepareSession(metadata)
	if err != nil {
		return SessionGrant{}, err
	}

	now := s.now().UTC()
	link, err := s.repository.ConsumeMagicLink(ctx, tokenDigest(token), MagicLinkPurposeSignIn, now)
	if err != nil {
		return SessionGrant{}, ErrInvalidMagicLink
	}
	user, err := s.repository.GetOrCreateUserByEmail(ctx, link.Email, userID, now)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("resolve magic-link user: %w", err)
	}
	if user.Status != UserStatusActive {
		return SessionGrant{}, ErrUserDisabled
	}

	pending.UserID = user.ID
	grant.UserID = user.ID
	if err := s.repository.SaveSession(ctx, pending); err != nil {
		return SessionGrant{}, fmt.Errorf("save session: %w", err)
	}
	return grant, nil
}

func (s *Service) AuthenticateSession(ctx context.Context, token string) (User, error) {
	if !validBearerToken(token) {
		return User{}, ErrInvalidSession
	}
	session, err := s.repository.UseSession(ctx, tokenDigest(token), s.now().UTC())
	if err != nil {
		return User{}, ErrInvalidSession
	}
	user, err := s.repository.GetUserByID(ctx, session.UserID)
	if err != nil {
		return User{}, ErrInvalidSession
	}
	if user.Status != UserStatusActive {
		return User{}, ErrUserDisabled
	}
	return user, nil
}

func (s *Service) RevokeSession(ctx context.Context, token string) error {
	if !validBearerToken(token) {
		return ErrInvalidSession
	}
	if err := s.repository.RevokeSession(ctx, tokenDigest(token), s.now().UTC()); err != nil {
		return ErrInvalidSession
	}
	return nil
}

func (s *Service) CurrentSession(ctx context.Context, token string) (Session, error) {
	if !validBearerToken(token) {
		return Session{}, ErrInvalidSession
	}
	session, err := s.repository.GetSession(ctx, tokenDigest(token))
	if err != nil || session.RevokedAt != nil || !s.now().UTC().Before(session.ExpiresAt) {
		return Session{}, ErrInvalidSession
	}
	return cloneSession(session), nil
}

func (s *Service) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, ErrInvalidSession
	}
	items, err := s.repository.ListSessionsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return items, nil
}

func (s *Service) RevokeOtherSessions(ctx context.Context, userID, currentToken string) error {
	if strings.TrimSpace(userID) == "" || !validBearerToken(currentToken) {
		return ErrInvalidSession
	}
	if err := s.repository.RevokeOtherSessions(ctx, userID, tokenDigest(currentToken), s.now().UTC()); err != nil {
		return ErrInvalidSession
	}
	return nil
}

func (s *Service) RevokeSessionByID(ctx context.Context, userID, sessionID string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" {
		return ErrInvalidInput
	}
	if err := s.repository.RevokeSessionByID(ctx, userID, sessionID, s.now().UTC()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (s *Service) BeginPasskeyRegistration(ctx context.Context, userID string) (PasskeyOptions, error) {
	if s.verifier == nil {
		return PasskeyOptions{}, ErrVerifierUnavailable
	}
	if strings.TrimSpace(userID) == "" {
		return PasskeyOptions{}, ErrInvalidInput
	}
	user, err := s.activeUser(ctx, userID)
	if err != nil {
		return PasskeyOptions{}, err
	}
	credentials, err := s.repository.ListCredentialsByUser(ctx, user.ID)
	if err != nil {
		return PasskeyOptions{}, fmt.Errorf("list passkeys: %w", err)
	}

	ceremony, err := s.newCeremony(CeremonyRegistration, user.ID)
	if err != nil {
		return PasskeyOptions{}, err
	}
	excluded := make([][]byte, 0, len(credentials))
	for _, credential := range credentials {
		excluded = append(excluded, append([]byte(nil), credential.CredentialID...))
	}
	options, err := s.verifier.CreateRegistrationOptions(ctx, RegistrationOptionsInput{
		Challenge:            ceremony.Challenge,
		User:                 user,
		ExcludeCredentialIDs: excluded,
	})
	if err != nil {
		return PasskeyOptions{}, fmt.Errorf("create passkey registration options: %w", err)
	}
	if !validJSONObject(options.PublicKey) || !validJSONObject(options.Session) {
		return PasskeyOptions{}, fmt.Errorf("%w: verifier returned invalid registration options", ErrPasskeyVerification)
	}
	ceremony.VerifierSession = append(json.RawMessage(nil), options.Session...)
	if err := s.repository.SaveCeremony(ctx, ceremony); err != nil {
		return PasskeyOptions{}, fmt.Errorf("save passkey ceremony: %w", err)
	}
	return PasskeyOptions{CeremonyID: ceremony.ID, PublicKey: append(json.RawMessage(nil), options.PublicKey...), ExpiresAt: ceremony.ExpiresAt}, nil
}

func (s *Service) CompletePasskeyRegistration(ctx context.Context, ceremonyID string, response json.RawMessage, label string) (PasskeyCredential, error) {
	return s.completePasskeyRegistration(ctx, "", ceremonyID, response, label)
}

// CompletePasskeyRegistrationForUser additionally binds the authenticated
// actor to the registration ceremony. HTTP transports must use this form.
func (s *Service) CompletePasskeyRegistrationForUser(ctx context.Context, userID, ceremonyID string, response json.RawMessage, label string) (PasskeyCredential, error) {
	if strings.TrimSpace(userID) == "" {
		return PasskeyCredential{}, ErrInvalidSession
	}
	return s.completePasskeyRegistration(ctx, userID, ceremonyID, response, label)
}

func (s *Service) completePasskeyRegistration(ctx context.Context, actorUserID, ceremonyID string, response json.RawMessage, label string) (PasskeyCredential, error) {
	if s.verifier == nil {
		return PasskeyCredential{}, ErrVerifierUnavailable
	}
	if strings.TrimSpace(ceremonyID) == "" || !validJSONObject(response) {
		return PasskeyCredential{}, ErrInvalidInput
	}
	label = strings.TrimSpace(label)
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > maxPasskeyLabelRunes {
		return PasskeyCredential{}, fmt.Errorf("%w: passkey label must be at most %d characters", ErrInvalidInput, maxPasskeyLabelRunes)
	}

	recordID, err := s.randomUUID()
	if err != nil {
		return PasskeyCredential{}, err
	}
	now := s.now().UTC()
	ceremony, err := s.repository.ConsumeCeremony(ctx, ceremonyID, CeremonyRegistration, now)
	if err != nil {
		return PasskeyCredential{}, ErrInvalidCeremony
	}
	if actorUserID != "" && ceremony.UserID != actorUserID {
		return PasskeyCredential{}, ErrInvalidCeremony
	}
	user, err := s.activeUser(ctx, ceremony.UserID)
	if err != nil {
		return PasskeyCredential{}, err
	}
	verified, err := s.verifier.VerifyRegistration(ctx, RegistrationVerificationInput{
		Challenge:       ceremony.Challenge,
		User:            user,
		VerifierSession: append(json.RawMessage(nil), ceremony.VerifierSession...),
		Response:        append(json.RawMessage(nil), response...),
	})
	if err != nil {
		return PasskeyCredential{}, fmt.Errorf("%w: %v", ErrPasskeyVerification, err)
	}
	if len(verified.CredentialID) == 0 || len(verified.PublicKey) == 0 || !validJSONObject(verified.VerifierCredential) {
		return PasskeyCredential{}, fmt.Errorf("%w: verifier returned incomplete credential", ErrPasskeyVerification)
	}

	credential := PasskeyCredential{
		ID:                 recordID,
		UserID:             user.ID,
		CredentialID:       append([]byte(nil), verified.CredentialID...),
		PublicKey:          append([]byte(nil), verified.PublicKey...),
		AAGUID:             append([]byte(nil), verified.AAGUID...),
		SignCount:          verified.SignCount,
		Transports:         append([]string(nil), verified.Transports...),
		VerifierCredential: append(json.RawMessage(nil), verified.VerifierCredential...),
		Label:              label,
		CreatedAt:          now,
	}
	if err := s.repository.SaveCredential(ctx, credential); err != nil {
		if errors.Is(err, ErrConflict) {
			return PasskeyCredential{}, ErrCredentialExists
		}
		return PasskeyCredential{}, fmt.Errorf("save passkey: %w", err)
	}
	return cloneCredential(credential), nil
}

// BeginPasskeyLogin accepts an empty userID for discoverable credentials.
func (s *Service) BeginPasskeyLogin(ctx context.Context, userID string) (PasskeyOptions, error) {
	if s.verifier == nil {
		return PasskeyOptions{}, ErrVerifierUnavailable
	}
	var user *User
	var allowed [][]byte
	if userID = strings.TrimSpace(userID); userID != "" {
		found, err := s.activeUser(ctx, userID)
		if err != nil {
			return PasskeyOptions{}, err
		}
		user = &found
		credentials, err := s.repository.ListCredentialsByUser(ctx, found.ID)
		if err != nil {
			return PasskeyOptions{}, fmt.Errorf("list passkeys: %w", err)
		}
		allowed = make([][]byte, 0, len(credentials))
		for _, credential := range credentials {
			allowed = append(allowed, append([]byte(nil), credential.CredentialID...))
		}
	}

	ceremony, err := s.newCeremony(CeremonyAuthentication, userID)
	if err != nil {
		return PasskeyOptions{}, err
	}
	options, err := s.verifier.CreateAuthenticationOptions(ctx, AuthenticationOptionsInput{
		Challenge:          ceremony.Challenge,
		User:               user,
		AllowCredentialIDs: allowed,
	})
	if err != nil {
		return PasskeyOptions{}, fmt.Errorf("create passkey authentication options: %w", err)
	}
	if !validJSONObject(options.PublicKey) || !validJSONObject(options.Session) {
		return PasskeyOptions{}, fmt.Errorf("%w: verifier returned invalid authentication options", ErrPasskeyVerification)
	}
	ceremony.VerifierSession = append(json.RawMessage(nil), options.Session...)
	if err := s.repository.SaveCeremony(ctx, ceremony); err != nil {
		return PasskeyOptions{}, fmt.Errorf("save passkey ceremony: %w", err)
	}
	return PasskeyOptions{CeremonyID: ceremony.ID, PublicKey: append(json.RawMessage(nil), options.PublicKey...), ExpiresAt: ceremony.ExpiresAt}, nil
}

func (s *Service) CompletePasskeyLogin(ctx context.Context, ceremonyID string, credentialID []byte, response json.RawMessage, metadata SessionMetadata) (SessionGrant, error) {
	if s.verifier == nil {
		return SessionGrant{}, ErrVerifierUnavailable
	}
	if strings.TrimSpace(ceremonyID) == "" || len(credentialID) == 0 || !validJSONObject(response) {
		return SessionGrant{}, ErrInvalidInput
	}
	pending, grant, err := s.prepareSession(metadata)
	if err != nil {
		return SessionGrant{}, err
	}

	now := s.now().UTC()
	ceremony, err := s.repository.ConsumeCeremony(ctx, ceremonyID, CeremonyAuthentication, now)
	if err != nil {
		return SessionGrant{}, ErrInvalidCeremony
	}
	credential, err := s.repository.GetCredentialByCredentialID(ctx, credentialID)
	if err != nil {
		return SessionGrant{}, ErrPasskeyVerification
	}
	if ceremony.UserID != "" && ceremony.UserID != credential.UserID {
		return SessionGrant{}, ErrPasskeyVerification
	}
	user, err := s.activeUser(ctx, credential.UserID)
	if err != nil {
		return SessionGrant{}, err
	}
	verified, err := s.verifier.VerifyAuthentication(ctx, AuthenticationVerificationInput{
		Challenge:       ceremony.Challenge,
		VerifierSession: append(json.RawMessage(nil), ceremony.VerifierSession...),
		Credential:      cloneCredential(credential),
		Response:        append(json.RawMessage(nil), response...),
	})
	if err != nil {
		if errors.Is(err, ErrInvalidSignCount) {
			return SessionGrant{}, s.rejectSuspectedPasskeyClone(ctx, credential)
		}
		return SessionGrant{}, fmt.Errorf("%w: %v", ErrPasskeyVerification, err)
	}
	if counterDidNotAdvance(credential.SignCount, verified.SignCount) {
		return SessionGrant{}, s.rejectSuspectedPasskeyClone(ctx, credential)
	}
	if !validJSONObject(verified.VerifierCredential) {
		return SessionGrant{}, fmt.Errorf("%w: verifier returned incomplete credential", ErrPasskeyVerification)
	}
	if err := s.repository.UseCredential(ctx, credential.CredentialID, credential.SignCount, verified.SignCount, verified.VerifierCredential, now); err != nil {
		if errors.Is(err, ErrConflict) {
			return SessionGrant{}, s.rejectSuspectedPasskeyClone(ctx, credential)
		}
		return SessionGrant{}, fmt.Errorf("update passkey usage: %w", err)
	}

	pending.UserID = user.ID
	grant.UserID = user.ID
	if err := s.repository.SaveSession(ctx, pending); err != nil {
		return SessionGrant{}, fmt.Errorf("save session: %w", err)
	}
	return grant, nil
}

func (s *Service) rejectSuspectedPasskeyClone(ctx context.Context, credential PasskeyCredential) error {
	rejection := errors.Join(ErrInvalidSignCount, ErrMagicLinkReauthenticationRequired)
	if s.securityEvents == nil {
		return rejection
	}
	err := s.securityEvents.RecordSecurityEvent(ctx, SecurityEvent{
		Kind:       SecurityEventPasskeyCloneSuspected,
		UserID:     credential.UserID,
		PasskeyID:  credential.ID,
		OccurredAt: s.now().UTC(),
	})
	if err != nil {
		return errors.Join(rejection, fmt.Errorf("record suspected passkey clone: %w", err))
	}
	return rejection
}

func (s *Service) activeUser(ctx context.Context, userID string) (User, error) {
	user, err := s.repository.GetUserByID(ctx, userID)
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	if user.Status != UserStatusActive {
		return User{}, ErrUserDisabled
	}
	return user, nil
}

func (s *Service) newCeremony(kind CeremonyKind, userID string) (PasskeyCeremony, error) {
	id, err := s.randomUUID()
	if err != nil {
		return PasskeyCeremony{}, err
	}
	challenge, err := s.randomToken(32)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	now := s.now().UTC()
	return PasskeyCeremony{
		ID:        id,
		Kind:      kind,
		Challenge: challenge,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ceremonyTTL),
	}, nil
}

func (s *Service) prepareSession(metadata SessionMetadata) (Session, SessionGrant, error) {
	id, err := s.randomUUID()
	if err != nil {
		return Session{}, SessionGrant{}, err
	}
	token, err := s.randomToken(32)
	if err != nil {
		return Session{}, SessionGrant{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.sessionTTL)
	return Session{
			ID:        id,
			TokenHash: tokenDigest(token),
			CreatedAt: now,
			ExpiresAt: expiresAt,
			IPAddress: strings.TrimSpace(metadata.IPAddress),
			UserAgent: strings.TrimSpace(metadata.UserAgent),
		}, SessionGrant{
			Token:     token,
			SessionID: id,
			ExpiresAt: expiresAt,
		}, nil
}

func (s *Service) randomID(prefix string, byteCount int) (string, error) {
	value, err := s.randomToken(byteCount)
	if err != nil {
		return "", err
	}
	return prefix + value, nil
}

func (s *Service) randomUUID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(s.random, value[:]); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func (s *Service) randomToken(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := io.ReadFull(s.random, buffer); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func normalizeEmail(value string) (string, error) {
	value, ok := identity.CanonicalEmail(value)
	if !ok {
		return "", ErrInvalidInput
	}
	return value, nil
}

func tokenDigest(token string) Digest {
	return sha256.Sum256([]byte(token))
}

func validBearerToken(token string) bool {
	if token == "" || len(token) > maxBearerTokenBytes {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) >= 24
}

func digestEqual(left, right Digest) bool {
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

func validJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	trimmed := strings.TrimSpace(string(value))
	return strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")
}

func counterDidNotAdvance(previous, next uint32) bool {
	return (previous != 0 || next != 0) && next <= previous
}
