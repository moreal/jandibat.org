package auth

import (
	"context"
	"encoding/json"
	"time"
)

type UserRepository interface {
	GetUserByID(ctx context.Context, id string) (User, error)
	// GetOrCreateUserByEmail must atomically return the existing user or create
	// one with suggestedID. Implementations must enforce normalized-email
	// uniqueness.
	GetOrCreateUserByEmail(ctx context.Context, email, suggestedID string, now time.Time) (User, error)
}

type MagicLinkRepository interface {
	// SaveMagicLink atomically stores link and invalidates older, unconsumed
	// links for the same normalized email and purpose.
	SaveMagicLink(ctx context.Context, link MagicLink) error
	// ConsumeMagicLink atomically checks purpose, existence, expiry and
	// one-time use. A token for a different purpose must not be consumed.
	ConsumeMagicLink(ctx context.Context, tokenHash Digest, purpose MagicLinkPurpose, now time.Time) (MagicLink, error)
	InvalidateMagicLink(ctx context.Context, tokenHash Digest, now time.Time) error
}

type SessionRepository interface {
	SaveSession(ctx context.Context, session Session) error
	// UseSession atomically verifies that the session is active and records
	// last-seen time.
	UseSession(ctx context.Context, tokenHash Digest, now time.Time) (Session, error)
	RevokeSession(ctx context.Context, tokenHash Digest, now time.Time) error
	GetSession(ctx context.Context, tokenHash Digest) (Session, error)
	// GetSessionByID returns owner-visible metadata, including historical
	// revoked/expired sessions, without bearer-token digest material.
	GetSessionByID(ctx context.Context, userID, sessionID string) (Session, error)
	ListSessionsByUser(ctx context.Context, userID string) ([]Session, error)
	// ListSessionsPage returns up to first+1 owner-visible metadata rows,
	// ordered by created_at DESC, id ASC. The extra row is a lookahead.
	ListSessionsPage(ctx context.Context, userID string, after *SessionCursor, first int) ([]Session, error)
	RevokeOtherSessions(ctx context.Context, userID string, exceptTokenHash Digest, now time.Time) error
	// RevokeOtherSessionsExceptID atomically validates a live, owned current
	// session before revoking its peers. No bearer material crosses this port.
	RevokeOtherSessionsExceptID(ctx context.Context, userID, sessionID string, now time.Time) error
	RevokeSessionByID(ctx context.Context, userID, sessionID string, now time.Time) error
}

type CeremonyRepository interface {
	SaveCeremony(ctx context.Context, ceremony PasskeyCeremony) error
	// ConsumeCeremony claims a ceremony before cryptographic verification so
	// parallel/replayed finish attempts cannot both succeed.
	ConsumeCeremony(ctx context.Context, id string, kind CeremonyKind, now time.Time) (PasskeyCeremony, error)
}

type CredentialRepository interface {
	SaveCredential(ctx context.Context, credential PasskeyCredential) error
	GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (PasskeyCredential, error)
	ListCredentialsByUser(ctx context.Context, userID string) ([]PasskeyCredential, error)
	// UseCredential performs a compare-and-swap on the signature counter and
	// updates last-used time. This prevents two assertions from racing with the
	// same authenticator counter.
	UseCredential(ctx context.Context, credentialID []byte, previous, next uint32, verifierCredential json.RawMessage, now time.Time) error
}

type Repository interface {
	UserRepository
	MagicLinkRepository
	SessionRepository
	CeremonyRepository
	CredentialRepository
}

type Mailer interface {
	SendMagicLink(ctx context.Context, mail MagicLinkMail) error
}

// MagicLinkDeliveryIssuer persists no bearer material. It supersedes older
// queued jobs and invalidates any token already minted for those jobs in the
// same transaction.
type MagicLinkDeliveryIssuer interface {
	SaveMagicLinkDeliveryIntent(context.Context, MagicLinkDelivery) error
}

type MagicLinkDeliveryRepository interface {
	ClaimMagicLinkDeliveries(context.Context, time.Time, time.Duration, int, int) ([]MagicLinkDelivery, error)
	// ActivateMagicLinkDelivery atomically invalidates the claim's prior token,
	// invalidates older live links for the same email/purpose, stores only the
	// new token digest, and associates it with the fenced claim.
	ActivateMagicLinkDelivery(context.Context, string, string, MagicLink) error
	CompleteMagicLinkDelivery(context.Context, string, string, time.Time) error
	RetryMagicLinkDelivery(context.Context, string, string, time.Time, time.Time, int) (MagicLinkDeliveryStatus, error)
}

type MagicLinkDeliveryObserver interface {
	ObserveMagicLinkDelivery(string)
}

type SecurityEventKind string

const SecurityEventPasskeyCloneSuspected SecurityEventKind = "passkey_clone_suspected"

// SecurityEvent carries only stable, non-secret identifiers across the auth
// audit boundary. In particular, PasskeyID is the database record ID, not the
// WebAuthn credential ID or any ceremony material.
type SecurityEvent struct {
	Kind       SecurityEventKind
	UserID     string
	PasskeyID  string
	OccurredAt time.Time
}

type SecurityEventRecorder interface {
	RecordSecurityEvent(context.Context, SecurityEvent) error
}

// PasskeyVerifier is the boundary at which a real WebAuthn implementation is
// connected. A successful Verify method must mean the adapter has performed
// the complete WebAuthn verification for its RP configuration, including
// origin, RP ID, challenge, type, user-presence/user-verification policy, and
// attestation/assertion signature checks. The auth package deliberately does
// not provide a permissive or pretend cryptographic implementation.
type PasskeyVerifier interface {
	CreateRegistrationOptions(ctx context.Context, input RegistrationOptionsInput) (PasskeyVerifierOptions, error)
	VerifyRegistration(ctx context.Context, input RegistrationVerificationInput) (RegistrationVerification, error)
	CreateAuthenticationOptions(ctx context.Context, input AuthenticationOptionsInput) (PasskeyVerifierOptions, error)
	VerifyAuthentication(ctx context.Context, input AuthenticationVerificationInput) (AuthenticationVerification, error)
}
