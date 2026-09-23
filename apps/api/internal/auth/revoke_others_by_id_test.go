package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestRevokeOtherSessionsExceptIDPreservesCurrentAndOtherUsers(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "owner", "revoke-owner@example.invalid")
	fixture.saveUser(t, "other", "revoke-other@example.invalid")
	now := fixture.clock.Now()
	current := Session{ID: "00000000-0000-4000-8000-000000000001", UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte("current"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	other := Session{ID: "00000000-0000-4000-8000-000000000002", UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte("other-owner"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	foreign := Session{ID: "00000000-0000-4000-8000-000000000003", UserID: "other", TokenHash: Digest(sha256.Sum256([]byte("foreign"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	for _, session := range []Session{current, other, foreign} {
		if err := fixture.store.SaveSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.service.RevokeOtherSessionsExceptID(context.Background(), "owner", current.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		session Session
		revoked bool
	}{{current, false}, {other, true}, {foreign, false}} {
		got, err := fixture.store.GetSessionByID(context.Background(), tc.session.UserID, tc.session.ID)
		if err != nil || (got.RevokedAt != nil) != tc.revoked {
			t.Fatalf("session %s revoked=%t, got=%+v err=%v", tc.session.ID, tc.revoked, got, err)
		}
	}
}

func TestRevokeOtherSessionsExceptIDRejectsUntrustedOrInactiveCurrentWithoutSideEffects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, userID, sessionID string
		revokeCurrent           bool
		expireCurrent           bool
		atExpiry                bool
		want                    error
	}{
		{"wrong owner", "other", "00000000-0000-4000-8000-000000000001", false, false, false, ErrNotFound},
		{"missing", "owner", "00000000-0000-4000-8000-000000000003", false, false, false, ErrNotFound},
		{"revoked", "owner", "00000000-0000-4000-8000-000000000001", true, false, false, ErrNotFound},
		{"expired", "owner", "00000000-0000-4000-8000-000000000001", false, true, false, ErrNotFound},
		{"at expiry", "owner", "00000000-0000-4000-8000-000000000001", false, false, true, ErrNotFound},
		{"malformed", "owner", "not-a-uuid", false, false, false, ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t, false)
			fixture.saveUser(t, "owner", "revoke-owner@example.invalid")
			fixture.saveUser(t, "other", "revoke-other@example.invalid")
			now := fixture.clock.Now()
			current := Session{ID: "00000000-0000-4000-8000-000000000001", UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte("current"))), CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(time.Hour)}
			if tc.expireCurrent {
				current.ExpiresAt = now.Add(-time.Hour)
			}
			if tc.atExpiry {
				current.ExpiresAt = now
			}
			other := Session{ID: "00000000-0000-4000-8000-000000000002", UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte("other"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			for _, session := range []Session{current, other} {
				if err := fixture.store.SaveSession(context.Background(), session); err != nil {
					t.Fatal(err)
				}
			}
			if tc.revokeCurrent {
				if err := fixture.store.RevokeSessionByID(context.Background(), "owner", current.ID, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.service.RevokeOtherSessionsExceptID(context.Background(), tc.userID, tc.sessionID); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			got, err := fixture.store.GetSessionByID(context.Background(), "owner", other.ID)
			if err != nil || got.RevokedAt != nil {
				t.Fatalf("failed attempt revoked another session: %+v, %v", got, err)
			}
		})
	}
}

func TestRevokeOtherSessionsExceptIDRequiresActiveAccount(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "owner", "revoke-disabled@example.invalid")
	now := fixture.clock.Now()
	current := Session{ID: "00000000-0000-4000-8000-000000000001", UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte("disabled-current"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	other := Session{ID: "00000000-0000-4000-8000-000000000002", UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte("disabled-other"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	for _, session := range []Session{current, other} {
		if err := fixture.store.SaveSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}
	user, err := fixture.store.GetUserByID(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	user.Status = UserStatusDisabled
	if err := fixture.store.SaveUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeOtherSessionsExceptID(context.Background(), "owner", current.ID); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled owner error=%v, want ErrUserDisabled", err)
	}
	got, err := fixture.store.GetSessionByID(context.Background(), "owner", other.ID)
	if err != nil || got.RevokedAt != nil {
		t.Fatalf("disabled owner revoked peer: %+v, %v", got, err)
	}
}
