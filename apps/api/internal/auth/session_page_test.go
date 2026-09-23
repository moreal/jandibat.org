package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestListSessionsPageTraversesEqualTimestampsWithoutGaps(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "owner", "owner-page@example.invalid")
	fixture.saveUser(t, "other", "other-page@example.invalid")
	now := fixture.clock.Now()
	ids := []string{
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000004",
	}
	for i, id := range ids {
		created := now
		if i == 3 {
			created = now.Add(-time.Hour)
		}
		if err := fixture.store.SaveSession(context.Background(), Session{
			ID: id, UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte(id))),
			CreatedAt: created, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.store.SaveSession(context.Background(), Session{
		ID: "00000000-0000-4000-8000-000000000005", UserID: "other",
		TokenHash: Digest(sha256.Sum256([]byte("other"))), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := fixture.service.ListSessionsPage(context.Background(), "owner", nil, 2)
	if err != nil || len(page) != 3 || page[0].ID != ids[1] || page[1].ID != ids[2] || page[2].ID != ids[0] {
		t.Fatalf("first page = (%v, %v), want IDs 1,2,3 including lookahead", sessionIDs(page), err)
	}
	for _, session := range page {
		if session.TokenHash != (Digest{}) || session.UserID != "owner" {
			t.Fatalf("page exposed digest or another owner: %+v", session)
		}
	}
	anchor := SessionCursor{CreatedAt: page[1].CreatedAt, ID: page[1].ID}
	// A keyset cursor is a tuple, not a lookup of an anchor row.
	fixture.store.mu.Lock()
	for key, session := range fixture.store.sessions {
		if session.ID == anchor.ID {
			delete(fixture.store.sessions, key)
		}
	}
	fixture.store.mu.Unlock()
	page, err = fixture.service.ListSessionsPage(context.Background(), "owner", &anchor, 2)
	if err != nil || len(page) != 2 || page[0].ID != ids[0] || page[1].ID != ids[3] {
		t.Fatalf("page after deleted anchor = (%v, %v), want IDs 3,4", sessionIDs(page), err)
	}
	page, err = fixture.service.ListSessionsPage(context.Background(), "owner", &SessionCursor{CreatedAt: now.Add(-2 * time.Hour), ID: ids[0]}, 2)
	if err != nil || len(page) != 0 {
		t.Fatalf("empty page = (%v, %v)", sessionIDs(page), err)
	}
}

func TestListSessionsPageRejectsInvalidLimitsAndCursor(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	for _, first := range []int{0, -1, 101} {
		if _, err := fixture.service.ListSessionsPage(context.Background(), "owner", nil, first); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("first=%d error=%v, want ErrInvalidInput", first, err)
		}
	}
	for _, after := range []SessionCursor{{ID: "bad", CreatedAt: time.Now()}, {ID: "00000000-0000-4000-8000-000000000001"}} {
		if _, err := fixture.service.ListSessionsPage(context.Background(), "owner", &after, 2); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cursor=%+v error=%v, want ErrInvalidInput", after, err)
		}
	}
	if _, err := fixture.service.ListSessionsPage(context.Background(), " ", nil, 2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank owner error=%v, want ErrInvalidInput", err)
	}
}

func TestListSessionsPageCapsAtOneHundredWithOneLookahead(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "owner", "owner-page-cap@example.invalid")
	now := fixture.clock.Now()
	for i := 0; i < 102; i++ {
		id := "00000000-0000-4000-8000-" + fmt.Sprintf("%012x", i)
		if err := fixture.store.SaveSession(context.Background(), Session{
			ID: id, UserID: "owner", TokenHash: Digest(sha256.Sum256([]byte(id))),
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := fixture.service.ListSessionsPage(context.Background(), "owner", nil, 100)
	if err != nil || len(page) != 101 || page[100].ID != "00000000-0000-4000-8000-000000000064" {
		t.Fatalf("max-size page count=%d last=%v err=%v", len(page), sessionIDs(page), err)
	}
}

func TestListSessionsPageRejectsDisabledOwner(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, false)
	fixture.saveUser(t, "owner", "disabled-page@example.invalid")
	user, err := fixture.store.GetUserByID(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	user.Status = UserStatusDisabled
	if err := fixture.store.SaveUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ListSessionsPage(context.Background(), "owner", nil, 1); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled owner page error=%v, want ErrUserDisabled", err)
	}
}

func sessionIDs(sessions []Session) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}
