package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestGetSessionByIDOwnerScopedMetadata(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "owner", "owner@example.invalid")
	fixture.saveUser(t, "other", "other@example.invalid")
	ctx := context.Background()
	now := fixture.clock.Now()
	const sessionID = "158f21a1-3fa1-4706-a2a6-c99a1caf40f0"
	token := base64.RawURLEncoding.EncodeToString([]byte("owner session bearer token material"))
	secretDigest := Digest(sha256.Sum256([]byte(token)))
	stored := Session{
		ID: sessionID, UserID: "owner", TokenHash: secretDigest,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		IPAddress: "203.0.113.8", UserAgent: "test browser",
	}
	if err := fixture.store.SaveSession(ctx, stored); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.service.GetSessionByID(ctx, "owner", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != sessionID || got.UserID != "owner" || !got.ExpiresAt.Equal(stored.ExpiresAt) || got.IPAddress != stored.IPAddress || got.UserAgent != stored.UserAgent {
		t.Fatalf("owner session metadata = %+v", got)
	}
	if got.TokenHash != (Digest{}) {
		t.Fatal("owner-scoped lookup exposed a session token digest")
	}
	if upper, err := fixture.service.GetSessionByID(ctx, "owner", "158F21A1-3FA1-4706-A2A6-C99A1CAF40F0"); err != nil || upper.ID != sessionID {
		t.Fatalf("canonical UUID lookup = (%+v, %v)", upper, err)
	}
	if _, err := fixture.service.GetSessionByID(ctx, "other", sessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner lookup error = %v, want ErrNotFound", err)
	}
	if _, err := fixture.service.GetSessionByID(ctx, "owner", "dea4b966-7041-4500-b278-ff55113fa8c6"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing lookup error = %v, want ErrNotFound", err)
	}
	if err := fixture.store.RevokeSessionByID(ctx, "owner", sessionID, now); err != nil {
		t.Fatal(err)
	}
	got, err = fixture.service.GetSessionByID(ctx, "owner", sessionID)
	if err != nil || got.RevokedAt == nil || !got.RevokedAt.Equal(now) || got.TokenHash != (Digest{}) {
		t.Fatalf("revoked session metadata = (%+v, %v)", got, err)
	}
	// Historical metadata remains visible while bearer authentication rejects expiry.
	if _, err := fixture.service.CurrentSession(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired bearer error = %v, want ErrInvalidSession", err)
	}
}

func TestGetSessionByIDRejectsMalformedIdentifiers(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	for _, tc := range []struct{ userID, sessionID string }{
		{"", "158f21a1-3fa1-4706-a2a6-c99a1caf40f0"},
		{"owner", ""},
		{"owner", "not-a-uuid"},
	} {
		if _, err := fixture.service.GetSessionByID(context.Background(), tc.userID, tc.sessionID); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("GetSessionByID(%q, %q) error = %v, want ErrInvalidInput", tc.userID, tc.sessionID, err)
		}
	}
}

func TestGetSessionByIDFailsClosedOnRepositoryOwnershipMismatch(t *testing.T) {
	t.Parallel()
	const sessionID = "158f21a1-3fa1-4706-a2a6-c99a1caf40f0"
	for _, tc := range []struct {
		name    string
		session Session
	}{
		{"wrong owner", Session{ID: sessionID, UserID: "other"}},
		{"wrong session ID", Session{ID: "dea4b966-7041-4500-b278-ff55113fa8c6", UserID: "owner"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t, false)
			fixture.service.repository = mismatchedSessionRepository{Repository: fixture.store, session: tc.session}
			got, err := fixture.service.GetSessionByID(context.Background(), "owner", sessionID)
			if !errors.Is(err, ErrNotFound) || got != (Session{}) {
				t.Fatalf("mismatched repository row = (%+v, %v), want empty ErrNotFound", got, err)
			}
		})
	}
}

type mismatchedSessionRepository struct {
	Repository
	session Session
}

func (r mismatchedSessionRepository) GetSessionByID(context.Context, string, string) (Session, error) {
	return r.session, nil
}
