package auth

import (
	"encoding/json"
	"time"
)

// Digest is the SHA-256 digest of a bearer secret. Repositories persist the
// digest, never the corresponding raw magic-link or session token.
type Digest [32]byte

type UserStatus string

const (
	UserStatusActive          UserStatus = "active"
	UserStatusDisabled        UserStatus = "disabled"
	UserStatusPending         UserStatus = "pending"
	UserStatusDeletionPending UserStatus = "deletion_pending"
)

type User struct {
	ID              string
	PrimaryEmail    string
	Status          UserStatus
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MagicLinkPurpose string

const (
	MagicLinkPurposeSignIn      MagicLinkPurpose = "signin"
	MagicLinkPurposeVerifyEmail MagicLinkPurpose = "verify_email"
)

func (purpose MagicLinkPurpose) Valid() bool {
	return purpose == MagicLinkPurposeSignIn || purpose == MagicLinkPurposeVerifyEmail
}

type MagicLink struct {
	ID         string
	Email      string
	TokenHash  Digest
	Purpose    MagicLinkPurpose
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

type Session struct {
	ID         string
	UserID     string
	TokenHash  Digest
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastSeenAt *time.Time
	IPAddress  string
	UserAgent  string
}

type SessionMetadata struct {
	IPAddress string
	UserAgent string
}

// SessionGrant contains the bearer token exactly once, at issuance. The
// repository-facing Session contains only its digest.
type SessionGrant struct {
	Token     string
	SessionID string
	UserID    string
	ExpiresAt time.Time
}

type MagicLinkMail struct {
	Email       string
	Token       string
	ExpiresAt   time.Time
	RedirectURI string
}

type MagicLinkDeliveryStatus string

const (
	MagicLinkDeliveryPending    MagicLinkDeliveryStatus = "pending"
	MagicLinkDeliveryProcessing MagicLinkDeliveryStatus = "processing"
	MagicLinkDeliverySent       MagicLinkDeliveryStatus = "sent"
	MagicLinkDeliveryDead       MagicLinkDeliveryStatus = "dead"
	MagicLinkDeliverySuperseded MagicLinkDeliveryStatus = "superseded"
)

// MagicLinkDelivery is a durable, secret-free request to send a link. The
// repository keeps any active one-way digest private to its persistence model.
type MagicLinkDelivery struct {
	ID             string
	RecipientEmail string
	RedirectURI    string
	Purpose        MagicLinkPurpose
	Status         MagicLinkDeliveryStatus
	Attempts       int
	AvailableAt    time.Time
	LeaseUntil     *time.Time
	ClaimToken     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	TerminalAt     *time.Time
	TerminalReason string
}

type MagicLinkDeliveryResult struct {
	Claimed int
	Sent    int
	Retry   int
	Dead    int
}

type CeremonyKind string

const (
	CeremonyRegistration   CeremonyKind = "registration"
	CeremonyAuthentication CeremonyKind = "authentication"
)

type PasskeyCeremony struct {
	ID              string
	Kind            CeremonyKind
	Challenge       string
	UserID          string
	VerifierSession json.RawMessage
	CreatedAt       time.Time
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
}

type PasskeyCredential struct {
	ID           string
	UserID       string
	CredentialID []byte
	PublicKey    []byte
	AAGUID       []byte
	SignCount    uint32
	Transports   []string
	// VerifierCredential is the opaque, versioned credential record produced
	// by the WebAuthn adapter. The normalized fields above remain available to
	// application code and indexes, while this record preserves security-
	// relevant flags and authenticator metadata needed for future assertions.
	VerifierCredential json.RawMessage
	Label              string
	CreatedAt          time.Time
	LastUsedAt         *time.Time
}

type PasskeyOptions struct {
	CeremonyID string
	PublicKey  json.RawMessage
	ExpiresAt  time.Time
}

// PasskeyVerifierOptions keeps browser-facing options separate from the
// server-only verifier session. The latter must be persisted without being
// exposed to or modified by the client.
type PasskeyVerifierOptions struct {
	PublicKey json.RawMessage
	Session   json.RawMessage
}

type RegistrationOptionsInput struct {
	Challenge            string
	User                 User
	ExcludeCredentialIDs [][]byte
}

type RegistrationVerificationInput struct {
	Challenge       string
	User            User
	VerifierSession json.RawMessage
	Response        json.RawMessage
}

type RegistrationVerification struct {
	CredentialID       []byte
	PublicKey          []byte
	AAGUID             []byte
	SignCount          uint32
	Transports         []string
	VerifierCredential json.RawMessage
}

type AuthenticationOptionsInput struct {
	Challenge          string
	User               *User
	AllowCredentialIDs [][]byte
}

type AuthenticationVerificationInput struct {
	Challenge       string
	VerifierSession json.RawMessage
	Credential      PasskeyCredential
	Response        json.RawMessage
}

type AuthenticationVerification struct {
	SignCount          uint32
	UserHandle         []byte
	VerifierCredential json.RawMessage
}
