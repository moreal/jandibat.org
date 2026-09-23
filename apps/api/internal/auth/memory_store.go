package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a thread-safe Repository intended for local development and
// tests. All returned byte slices and pointer fields are defensive copies.
type MemoryStore struct {
	mu sync.RWMutex

	usersByID         map[string]User
	userIDByEmail     map[string]string
	magicLinks        map[string]MagicLink
	sessions          map[string]Session
	ceremonies        map[string]PasskeyCeremony
	credentials       map[string]PasskeyCredential
	credentialKeyByID map[string]string
	deletedEmails     map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		usersByID:         make(map[string]User),
		userIDByEmail:     make(map[string]string),
		magicLinks:        make(map[string]MagicLink),
		sessions:          make(map[string]Session),
		ceremonies:        make(map[string]PasskeyCeremony),
		credentials:       make(map[string]PasskeyCredential),
		credentialKeyByID: make(map[string]string),
		deletedEmails:     make(map[string]time.Time),
	}
}

// SaveUser is a memory-adapter helper for provisioning users before a passkey
// registration flow. Production adapters can provision them transactionally
// through their own application boundary.
func (s *MemoryStore) SaveUser(ctx context.Context, user User) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if user.ID == "" {
		return ErrInvalidInput
	}
	email, err := normalizeEmail(user.PrimaryEmail)
	if err != nil {
		return err
	}
	user.PrimaryEmail = email
	if user.Status == "" {
		user.Status = UserStatusActive
	}
	if !validUserStatus(user.Status) {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.usersByID[user.ID]; ok && existing.PrimaryEmail != email {
		return ErrConflict
	}
	if existingID, ok := s.userIDByEmail[email]; ok && existingID != user.ID {
		return ErrConflict
	}
	s.usersByID[user.ID] = cloneUser(user)
	s.userIDByEmail[email] = user.ID
	return nil
}

func (s *MemoryStore) GetUserByID(ctx context.Context, id string) (User, error) {
	if err := contextError(ctx); err != nil {
		return User{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.usersByID[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return cloneUser(user), nil
}

func (s *MemoryStore) GetOrCreateUserByEmail(ctx context.Context, email, suggestedID string, now time.Time) (User, error) {
	if err := contextError(ctx); err != nil {
		return User{}, err
	}
	normalized, err := normalizeEmail(email)
	if err != nil || suggestedID == "" {
		return User{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expiresAt, deleted := s.deletedEmails[normalized]; deleted {
		if expiresAt.After(now) {
			return User{}, ErrUserDisabled
		}
		delete(s.deletedEmails, normalized)
	}
	if id, ok := s.userIDByEmail[normalized]; ok {
		return cloneUser(s.usersByID[id]), nil
	}
	verifiedAt := now
	user := User{
		ID:              suggestedID,
		PrimaryEmail:    normalized,
		Status:          UserStatusActive,
		EmailVerifiedAt: &verifiedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.usersByID[user.ID] = user
	s.userIDByEmail[normalized] = user.ID
	return cloneUser(user), nil
}

// TombstoneUserByEmail atomically removes a local-development identity and
// prevents magic-link completion from recreating it during the retention
// window. Production account deletion persists the equivalent digest-only
// marker in deleted_identity_tombstones.
func (s *MemoryStore) TombstoneUserByEmail(ctx context.Context, email string, expiresAt time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	normalized, err := normalizeEmail(email)
	if err != nil || expiresAt.IsZero() {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.userIDByEmail[normalized]; ok {
		delete(s.usersByID, id)
		delete(s.userIDByEmail, normalized)
		for key, session := range s.sessions {
			if session.UserID == id {
				delete(s.sessions, key)
			}
		}
		for key, credential := range s.credentials {
			if credential.UserID == id {
				delete(s.credentialKeyByID, credential.ID)
				delete(s.credentials, key)
			}
		}
	}
	s.deletedEmails[normalized] = expiresAt
	return nil
}

func (s *MemoryStore) SaveMagicLink(ctx context.Context, link MagicLink) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if link.ID == "" || link.Email == "" || !link.Purpose.Valid() || !link.ExpiresAt.After(link.CreatedAt) {
		return ErrInvalidInput
	}
	key := digestKey(link.TokenHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.magicLinks[key]; ok {
		return ErrConflict
	}
	for existingKey, existing := range s.magicLinks {
		if existing.Email == link.Email && existing.Purpose == link.Purpose && existing.ConsumedAt == nil {
			consumedAt := link.CreatedAt
			existing.ConsumedAt = &consumedAt
			s.magicLinks[existingKey] = existing
		}
	}
	s.magicLinks[key] = cloneMagicLink(link)
	return nil
}

func (s *MemoryStore) ConsumeMagicLink(ctx context.Context, tokenHash Digest, purpose MagicLinkPurpose, now time.Time) (MagicLink, error) {
	if err := contextError(ctx); err != nil {
		return MagicLink{}, err
	}
	if !purpose.Valid() {
		return MagicLink{}, ErrInvalidInput
	}
	key := digestKey(tokenHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.magicLinks[key]
	if !ok || !digestEqual(link.TokenHash, tokenHash) || link.Purpose != purpose {
		return MagicLink{}, ErrNotFound
	}
	if link.ConsumedAt != nil {
		return MagicLink{}, ErrConsumed
	}
	if !now.Before(link.ExpiresAt) {
		return MagicLink{}, ErrExpired
	}
	consumedAt := now
	link.ConsumedAt = &consumedAt
	s.magicLinks[key] = link
	return cloneMagicLink(link), nil
}

func (s *MemoryStore) InvalidateMagicLink(ctx context.Context, tokenHash Digest, now time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	key := digestKey(tokenHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.magicLinks[key]
	if !ok || !digestEqual(link.TokenHash, tokenHash) {
		return ErrNotFound
	}
	if link.ConsumedAt == nil {
		consumedAt := now
		link.ConsumedAt = &consumedAt
		s.magicLinks[key] = link
	}
	return nil
}

func (s *MemoryStore) SaveSession(ctx context.Context, session Session) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if session.ID == "" || session.UserID == "" || !session.ExpiresAt.After(session.CreatedAt) {
		return ErrInvalidInput
	}
	key := digestKey(session.TokenHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[key]; ok {
		return ErrConflict
	}
	if _, ok := s.usersByID[session.UserID]; !ok {
		return ErrNotFound
	}
	if s.usersByID[session.UserID].Status != UserStatusActive {
		return ErrUserDisabled
	}
	s.sessions[key] = cloneSession(session)
	return nil
}

func (s *MemoryStore) UseSession(ctx context.Context, tokenHash Digest, now time.Time) (Session, error) {
	if err := contextError(ctx); err != nil {
		return Session{}, err
	}
	key := digestKey(tokenHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[key]
	if !ok || !digestEqual(session.TokenHash, tokenHash) {
		return Session{}, ErrNotFound
	}
	if session.RevokedAt != nil {
		return Session{}, ErrConsumed
	}
	if !now.Before(session.ExpiresAt) {
		return Session{}, ErrExpired
	}
	user, ok := s.usersByID[session.UserID]
	if !ok || user.Status != UserStatusActive {
		return Session{}, ErrUserDisabled
	}
	lastSeenAt := now
	session.LastSeenAt = &lastSeenAt
	s.sessions[key] = session
	return cloneSession(session), nil
}

func (s *MemoryStore) RevokeSession(ctx context.Context, tokenHash Digest, now time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	key := digestKey(tokenHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[key]
	if !ok || !digestEqual(session.TokenHash, tokenHash) {
		return ErrNotFound
	}
	if session.RevokedAt == nil {
		revokedAt := now
		session.RevokedAt = &revokedAt
		s.sessions[key] = session
	}
	return nil
}

func (s *MemoryStore) GetSession(ctx context.Context, tokenHash Digest) (Session, error) {
	if err := contextError(ctx); err != nil {
		return Session{}, err
	}
	key := digestKey(tokenHash)
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[key]
	if !ok || !digestEqual(session.TokenHash, tokenHash) {
		return Session{}, ErrNotFound
	}
	return cloneSession(session), nil
}

func (s *MemoryStore) GetSessionByID(ctx context.Context, userID, sessionID string) (Session, error) {
	if err := contextError(ctx); err != nil {
		return Session{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, session := range s.sessions {
		if session.UserID == userID && session.ID == sessionID {
			result := cloneSession(session)
			result.TokenHash = Digest{}
			return result, nil
		}
	}
	return Session{}, ErrNotFound
}

func (s *MemoryStore) ListSessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]Session, 0)
	for _, session := range s.sessions {
		if session.UserID == userID {
			items = append(items, cloneSession(session))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) ListSessionsPage(ctx context.Context, userID string, after *SessionCursor, first int) ([]Session, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if first < 1 || first > 100 {
		return nil, ErrInvalidInput
	}
	s.mu.RLock()
	items := make([]Session, 0)
	for _, session := range s.sessions {
		if session.UserID != userID {
			continue
		}
		if after != nil && !session.CreatedAt.Before(after.CreatedAt) &&
			(!session.CreatedAt.Equal(after.CreatedAt) || session.ID <= after.ID) {
			continue
		}
		item := cloneSession(session)
		item.TokenHash = Digest{}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > first+1 {
		items = items[:first+1]
	}
	return items, nil
}

func (s *MemoryStore) RevokeOtherSessions(ctx context.Context, userID string, exceptTokenHash Digest, now time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	foundCurrent := false
	for key, session := range s.sessions {
		if session.UserID != userID {
			continue
		}
		if digestEqual(session.TokenHash, exceptTokenHash) {
			foundCurrent = true
			continue
		}
		if session.RevokedAt == nil {
			revokedAt := now
			session.RevokedAt = &revokedAt
			s.sessions[key] = session
		}
	}
	if !foundCurrent {
		return ErrNotFound
	}
	return nil
}

func (s *MemoryStore) RevokeOtherSessionsExceptID(ctx context.Context, userID, sessionID string, now time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	currentFound := false
	for _, session := range s.sessions {
		if session.UserID == userID && session.ID == sessionID && session.RevokedAt == nil && now.Before(session.ExpiresAt) {
			currentFound = true
			break
		}
	}
	if !currentFound {
		return ErrNotFound
	}
	for key, session := range s.sessions {
		if session.UserID != userID || session.ID == sessionID || session.RevokedAt != nil {
			continue
		}
		when := now
		session.RevokedAt = &when
		s.sessions[key] = session
	}
	return nil
}

func (s *MemoryStore) RevokeSessionByID(ctx context.Context, userID, sessionID string, now time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, session := range s.sessions {
		if session.ID != sessionID || session.UserID != userID {
			continue
		}
		if session.RevokedAt == nil {
			revokedAt := now
			session.RevokedAt = &revokedAt
			s.sessions[key] = session
		}
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) SaveCeremony(ctx context.Context, ceremony PasskeyCeremony) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if ceremony.ID == "" || ceremony.Challenge == "" || !validJSONObject(ceremony.VerifierSession) || !validCeremonyKind(ceremony.Kind) || !ceremony.ExpiresAt.After(ceremony.CreatedAt) {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ceremonies[ceremony.ID]; ok {
		return ErrConflict
	}
	s.ceremonies[ceremony.ID] = cloneCeremony(ceremony)
	return nil
}

func (s *MemoryStore) ConsumeCeremony(ctx context.Context, id string, kind CeremonyKind, now time.Time) (PasskeyCeremony, error) {
	if err := contextError(ctx); err != nil {
		return PasskeyCeremony{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ceremony, ok := s.ceremonies[id]
	if !ok || ceremony.Kind != kind {
		return PasskeyCeremony{}, ErrNotFound
	}
	if ceremony.ConsumedAt != nil {
		return PasskeyCeremony{}, ErrConsumed
	}
	if !now.Before(ceremony.ExpiresAt) {
		return PasskeyCeremony{}, ErrExpired
	}
	consumedAt := now
	ceremony.ConsumedAt = &consumedAt
	s.ceremonies[id] = ceremony
	return cloneCeremony(ceremony), nil
}

func (s *MemoryStore) SaveCredential(ctx context.Context, credential PasskeyCredential) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if credential.ID == "" || credential.UserID == "" || len(credential.CredentialID) == 0 || len(credential.PublicKey) == 0 || !validJSONObject(credential.VerifierCredential) {
		return ErrInvalidInput
	}
	key := credentialKey(credential.CredentialID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.usersByID[credential.UserID]; !ok {
		return ErrNotFound
	}
	if _, ok := s.credentials[key]; ok {
		return ErrConflict
	}
	if _, ok := s.credentialKeyByID[credential.ID]; ok {
		return ErrConflict
	}
	s.credentials[key] = cloneCredential(credential)
	s.credentialKeyByID[credential.ID] = key
	return nil
}

func (s *MemoryStore) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (PasskeyCredential, error) {
	if err := contextError(ctx); err != nil {
		return PasskeyCredential{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	credential, ok := s.credentials[credentialKey(credentialID)]
	if !ok {
		return PasskeyCredential{}, ErrNotFound
	}
	return cloneCredential(credential), nil
}

func (s *MemoryStore) ListCredentialsByUser(ctx context.Context, userID string) ([]PasskeyCredential, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	credentials := make([]PasskeyCredential, 0)
	for _, credential := range s.credentials {
		if credential.UserID == userID {
			credentials = append(credentials, cloneCredential(credential))
		}
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].ID < credentials[j].ID })
	return credentials, nil
}

func (s *MemoryStore) UseCredential(ctx context.Context, credentialID []byte, previous, next uint32, verifierCredential json.RawMessage, now time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if !validJSONObject(verifierCredential) {
		return ErrInvalidInput
	}
	key := credentialKey(credentialID)
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, ok := s.credentials[key]
	if !ok {
		return ErrNotFound
	}
	if credential.SignCount != previous || counterDidNotAdvance(previous, next) {
		return ErrConflict
	}
	credential.SignCount = next
	credential.VerifierCredential = append(json.RawMessage(nil), verifierCredential...)
	lastUsedAt := now
	credential.LastUsedAt = &lastUsedAt
	s.credentials[key] = credential
	return nil
}

func digestKey(digest Digest) string {
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func credentialKey(credentialID []byte) string {
	return base64.RawURLEncoding.EncodeToString(credentialID)
}

func validCeremonyKind(kind CeremonyKind) bool {
	return kind == CeremonyRegistration || kind == CeremonyAuthentication
}

func validUserStatus(status UserStatus) bool {
	switch status {
	case UserStatusActive, UserStatusDisabled, UserStatusPending, UserStatusDeletionPending:
		return true
	default:
		return false
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("auth: nil context")
	}
	return ctx.Err()
}

func cloneUser(user User) User {
	if user.EmailVerifiedAt != nil {
		value := *user.EmailVerifiedAt
		user.EmailVerifiedAt = &value
	}
	return user
}

func cloneMagicLink(link MagicLink) MagicLink {
	if link.ConsumedAt != nil {
		value := *link.ConsumedAt
		link.ConsumedAt = &value
	}
	return link
}

func cloneSession(session Session) Session {
	if session.RevokedAt != nil {
		value := *session.RevokedAt
		session.RevokedAt = &value
	}
	if session.LastSeenAt != nil {
		value := *session.LastSeenAt
		session.LastSeenAt = &value
	}
	return session
}

func cloneCeremony(ceremony PasskeyCeremony) PasskeyCeremony {
	ceremony.VerifierSession = append(json.RawMessage(nil), ceremony.VerifierSession...)
	if ceremony.ConsumedAt != nil {
		value := *ceremony.ConsumedAt
		ceremony.ConsumedAt = &value
	}
	return ceremony
}

func cloneCredential(credential PasskeyCredential) PasskeyCredential {
	credential.CredentialID = append([]byte(nil), credential.CredentialID...)
	credential.PublicKey = append([]byte(nil), credential.PublicKey...)
	credential.AAGUID = append([]byte(nil), credential.AAGUID...)
	credential.Transports = append([]string(nil), credential.Transports...)
	credential.VerifierCredential = append(json.RawMessage(nil), credential.VerifierCredential...)
	if credential.LastUsedAt != nil {
		value := *credential.LastUsedAt
		credential.LastUsedAt = &value
	}
	return credential
}
