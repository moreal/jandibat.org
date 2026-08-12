package auth

import (
	"context"
	"sync"
)

// MemoryMailer records outbound magic-link messages for local development and
// tests. It sends no network traffic.
type MemoryMailer struct {
	mu       sync.RWMutex
	messages []MagicLinkMail
	err      error
}

func NewMemoryMailer() *MemoryMailer {
	return &MemoryMailer{}
}

func (m *MemoryMailer) SendMagicLink(ctx context.Context, mail MagicLinkMail) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.messages = append(m.messages, mail)
	return nil
}

func (m *MemoryMailer) Messages() []MagicLinkMail {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]MagicLinkMail(nil), m.messages...)
}

func (m *MemoryMailer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}

// SetError makes subsequent sends fail without recording a message.
func (m *MemoryMailer) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}
