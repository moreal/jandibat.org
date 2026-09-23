package cockroach

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestMagicLinkOperationsRequireGeneratedPGXStore(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC)
	digest := sha256.Sum256([]byte("pgx-only-magic-link"))
	script := fakedb.New()
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	link := coreauth.MagicLink{ID: "9a69e9fe-c3d0-497b-b2c2-06751de328d9", Email: "person@example.com", TokenHash: digest, Purpose: coreauth.MagicLinkPurposeSignIn, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	delivery := coreauth.MagicLinkDelivery{ID: "00f2ecf8-4f85-4bfd-8fbf-a006c4aedb11", RecipientEmail: link.Email, RedirectURI: "https://app.example/#auth", Purpose: link.Purpose, Status: coreauth.MagicLinkDeliveryPending, AvailableAt: now, CreatedAt: now}
	checks := []struct {
		name string
		run  func() error
	}{
		{"schema", func() error { return store.CheckMagicLinkDeliverySchema(context.Background()) }},
		{"save link", func() error { return store.SaveMagicLink(context.Background(), link) }},
		{"consume link", func() error {
			_, err := store.ConsumeMagicLink(context.Background(), digest, link.Purpose, now)
			return err
		}},
		{"invalidate link", func() error { return store.InvalidateMagicLink(context.Background(), digest, now) }},
		{"save intent", func() error { return store.SaveMagicLinkDeliveryIntent(context.Background(), delivery) }},
		{"claim", func() error {
			_, err := store.ClaimMagicLinkDeliveries(context.Background(), now, time.Minute, 1, 5)
			return err
		}},
		{"activate", func() error {
			return store.ActivateMagicLinkDelivery(context.Background(), delivery.ID, "37b12264-d530-4718-9561-fd4105ad40cc", link)
		}},
		{"complete", func() error {
			return store.CompleteMagicLinkDelivery(context.Background(), delivery.ID, "37b12264-d530-4718-9561-fd4105ad40cc", now)
		}},
		{"retry", func() error {
			_, err := store.RetryMagicLinkDelivery(context.Background(), delivery.ID, "37b12264-d530-4718-9561-fd4105ad40cc", now, now.Add(time.Minute), 5)
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrNilDB) {
				t.Fatalf("SQL-only magic-link operation error = %v, want ErrNilDB", err)
			}
		})
	}
	if calls := script.Calls(); len(calls) != 0 {
		t.Fatalf("SQL-only magic-link operations made legacy queries: %#v", calls)
	}
}
