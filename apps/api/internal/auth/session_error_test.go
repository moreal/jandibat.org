package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

type failingSessionRepository struct {
	Repository
	useErr  error
	getErr  error
	userErr error
}

func (r failingSessionRepository) UseSession(ctx context.Context, digest Digest, now time.Time) (Session, error) {
	if r.useErr != nil {
		return Session{}, r.useErr
	}
	return r.Repository.UseSession(ctx, digest, now)
}

func (r failingSessionRepository) GetSession(ctx context.Context, digest Digest) (Session, error) {
	if r.getErr != nil {
		return Session{}, r.getErr
	}
	return r.Repository.GetSession(ctx, digest)
}

func (r failingSessionRepository) GetUserByID(ctx context.Context, id string) (User, error) {
	if r.userErr != nil {
		return User{}, r.userErr
	}
	return r.Repository.GetUserByID(ctx, id)
}

func TestSessionAuthenticationPreservesOperationalRepositoryErrors(t *testing.T) {
	t.Parallel()
	transient := errors.New("repository temporarily unavailable")
	for _, tc := range []struct {
		name string
		port string
	}{
		{"active session update", "use"},
		{"user lookup", "user"},
		{"session metadata lookup", "get"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t, false)
			fixture.saveUser(t, "user-1", "person@example.test")
			token := base64.RawURLEncoding.EncodeToString([]byte("session token with sufficient entropy"))
			now := fixture.clock.Now()
			if err := fixture.store.SaveSession(context.Background(), Session{
				ID: "00000000-0000-4000-8000-000000000001", UserID: "user-1", TokenHash: tokenDigest(token),
				CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			defer func() { fixture.service.repository = fixture.store }()
			repository := failingSessionRepository{Repository: fixture.store}
			switch tc.port {
			case "use":
				repository.useErr = transient
			case "user":
				repository.userErr = transient
			case "get":
				repository.getErr = transient
			}
			fixture.service.repository = repository
			var err error
			if tc.port == "get" {
				_, err = fixture.service.CurrentSession(context.Background(), token)
			} else {
				_, err = fixture.service.AuthenticateSession(context.Background(), token)
			}
			if !errors.Is(err, transient) || errors.Is(err, ErrInvalidSession) {
				t.Fatalf("repository outage error = %v, want operational error distinct from invalid session", err)
			}
		})
	}
}
