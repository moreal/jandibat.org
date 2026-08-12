package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// FlowState is the server-side data bound to an opaque browser state value.
// CodeVerifier must never be returned through an API.
type FlowState struct {
	ProviderID         string
	ConnectionID       string
	SubjectID          string
	RedirectURI        string
	CodeVerifier       string
	RequestedScopes    []string
	SessionBindingHash string
	ClientRedirectURI  string
	ExpiresAt          time.Time
}

// StateBinding contains the server-controlled values that must match before a
// callback can claim an OAuth state. SessionBindingHash is the SHA-256 digest
// of the browser session token, never the token itself.
type StateBinding struct {
	ProviderID            string
	RedirectURI           string
	SessionBindingHash    string
	RequireSessionBinding bool
}

func (binding StateBinding) matches(flow FlowState) bool {
	if flow.ProviderID != binding.ProviderID || flow.RedirectURI != binding.RedirectURI {
		return false
	}
	if !binding.RequireSessionBinding && flow.SessionBindingHash == "" {
		return true
	}
	return binding.SessionBindingHash != "" && flow.SessionBindingHash != "" &&
		subtle.ConstantTimeCompare([]byte(binding.SessionBindingHash), []byte(flow.SessionBindingHash)) == 1
}

// StateStore persists short-lived OAuth flow state. Consume must atomically
// validate the callback binding and claim the state. A binding mismatch must
// leave the state available for the browser session that initiated the flow;
// after a successful claim the same value must never work again.
type StateStore interface {
	Put(context.Context, string, FlowState) error
	Consume(context.Context, string, StateBinding) (FlowState, error)
}

// MemoryStateStore retains only SHA-256 hashes of browser state values. It is
// suitable for a single API process; a distributed deployment can implement
// StateStore with a shared transactional store while keeping the Adapter API.
type MemoryStateStore struct {
	mu      sync.Mutex
	clock   Clock
	entries map[[sha256.Size]byte]FlowState
}

func NewMemoryStateStore(clock Clock) *MemoryStateStore {
	return &MemoryStateStore{
		clock:   clockOrDefault(clock),
		entries: make(map[[sha256.Size]byte]FlowState),
	}
}

func (store *MemoryStateStore) Put(ctx context.Context, state string, flow FlowState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validStateValue(state) || flow.ExpiresAt.IsZero() || !flow.ExpiresAt.After(store.clock.Now()) {
		return ErrInvalidState
	}
	hash := sha256.Sum256([]byte(state))
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeExpiredLocked(store.clock.Now())
	if _, exists := store.entries[hash]; exists {
		return ErrInvalidState
	}
	store.entries[hash] = cloneFlowState(flow)
	return nil
}

func (store *MemoryStateStore) Consume(ctx context.Context, state string, binding StateBinding) (FlowState, error) {
	if err := ctx.Err(); err != nil {
		return FlowState{}, err
	}
	if !validStateValue(state) {
		return FlowState{}, ErrInvalidState
	}
	hash := sha256.Sum256([]byte(state))
	now := store.clock.Now()
	store.mu.Lock()
	defer store.mu.Unlock()
	flow, exists := store.entries[hash]
	store.removeExpiredLocked(now)
	if !exists || !flow.ExpiresAt.After(now) || !binding.matches(flow) {
		return FlowState{}, ErrInvalidState
	}
	delete(store.entries, hash)
	return cloneFlowState(flow), nil
}

func (store *MemoryStateStore) removeExpiredLocked(now time.Time) {
	for hash, flow := range store.entries {
		if !flow.ExpiresAt.After(now) {
			delete(store.entries, hash)
		}
	}
}

func validStateValue(state string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(state)
	return err == nil && len(decoded) == 32
}

func cloneFlowState(flow FlowState) FlowState {
	flow.RequestedScopes = append([]string(nil), flow.RequestedScopes...)
	return flow
}
