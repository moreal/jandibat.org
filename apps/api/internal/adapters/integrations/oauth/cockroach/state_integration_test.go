//go:build integration

package cockroach

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
)

func TestGeneratedOAuthStateIsBoundAndConsumedOnce(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	store.now = func() time.Time { return now }
	state := base64.RawURLEncoding.EncodeToString([]byte("oauth-state-" + uuid.NewString()[:20]))
	digest := sha256.Sum256([]byte(state))
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_challenges WHERE challenge_hash=$1`, digest[:])
	})
	flow := oauth.FlowState{ProviderID: "github", ConnectionID: "connection", SubjectID: "subject", RedirectURI: "https://example.invalid/callback", CodeVerifier: "server-only", SessionBindingHash: "bound", ExpiresAt: now.Add(time.Minute)}
	if err := store.Put(ctx, state, flow); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT challenge_hash FROM auth_challenges WHERE challenge_hash=$1`, digest[:]).Scan(&stored); err != nil || string(stored) != string(digest[:]) {
		t.Fatalf("stored digest = %x, %v", stored, err)
	}
	binding := oauth.StateBinding{ProviderID: "github", RedirectURI: flow.RedirectURI, SessionBindingHash: "wrong", RequireSessionBinding: true}
	if _, err := store.Consume(ctx, state, binding); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("wrong binding = %v", err)
	}
	binding.SessionBindingHash = "bound"
	got, err := store.Consume(ctx, state, binding)
	if err != nil || got.CodeVerifier != flow.CodeVerifier {
		t.Fatalf("consume = (%+v, %v)", got, err)
	}
	if _, err := store.Consume(ctx, state, binding); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("replay = %v", err)
	}
}
