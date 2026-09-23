//go:build integration

package cockroach

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestCockroachSessionPageUsesOwnerScopedKeysetAfterDeletedAnchor(t *testing.T) {
	adminDSN, apiDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL"), os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set isolated JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { api.Close(); admin.Close() })
	store, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	owner, other := "page-owner-"+uuid.NewString(), "page-other-"+uuid.NewString()
	for _, id := range []string{owner, other} {
		if _, err := admin.Exec(ctx, `INSERT INTO users (id,primary_email,status) VALUES ($1,$2,'active')`, id, id+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, owner, other)
	})
	base := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{
		"00000000-0000-4000-8000-" + uuid.NewString()[24:],
		"00000000-0000-4000-8001-" + uuid.NewString()[24:],
		"00000000-0000-4000-8002-" + uuid.NewString()[24:],
		"00000000-0000-4000-8003-" + uuid.NewString()[24:],
	}
	for i, id := range ids {
		created := base
		if i == 3 {
			created = base.Add(-time.Hour)
		}
		if err := store.SaveSession(ctx, coreauth.Session{
			ID: id, UserID: owner, TokenHash: coreauth.Digest(sha256.Sum256([]byte(owner + id))),
			CreatedAt: created, ExpiresAt: base.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSession(ctx, coreauth.Session{
		ID: uuid.NewString(), UserID: other, TokenHash: coreauth.Digest(sha256.Sum256([]byte(other))),
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListSessionsPage(ctx, owner, nil, 2)
	if err != nil || len(page) != 3 || page[0].ID != ids[0] || page[1].ID != ids[1] || page[2].ID != ids[2] {
		t.Fatalf("first page IDs = (%v, %v), want first three ordered IDs", cockroachSessionIDs(page), err)
	}
	for _, session := range page {
		if session.TokenHash != (coreauth.Digest{}) || session.UserID != owner {
			t.Fatalf("page exposed digest or other owner: %+v", session)
		}
	}
	anchor := coreauth.SessionCursor{CreatedAt: page[1].CreatedAt, ID: page[1].ID}
	if _, err := admin.Exec(ctx, `DELETE FROM user_sessions WHERE id=$1::UUID`, anchor.ID); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListSessionsPage(ctx, owner, &anchor, 2)
	if err != nil || len(page) != 2 || page[0].ID != ids[2] || page[1].ID != ids[3] {
		t.Fatalf("after deleted anchor IDs = (%v, %v), want final two", cockroachSessionIDs(page), err)
	}
	page, err = store.ListSessionsPage(ctx, owner, &coreauth.SessionCursor{CreatedAt: base.Add(-2 * time.Hour), ID: ids[0]}, 2)
	if err != nil || len(page) != 0 {
		t.Fatalf("empty page = (%v, %v)", cockroachSessionIDs(page), err)
	}
}

func cockroachSessionIDs(sessions []coreauth.Session) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}
