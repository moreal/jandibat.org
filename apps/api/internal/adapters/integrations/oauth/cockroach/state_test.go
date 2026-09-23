package cockroach

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
)

func TestStateStoreRejectsNilPool(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) error = %v", err)
	}
}

func TestStateStoreRejectsInvalidInputBeforeDatabase(t *testing.T) {
	store, err := New(&pgxpool.Pool{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if err := store.Put(context.Background(), "not-state", oauth.FlowState{}); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("invalid Put error = %v", err)
	}
	state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	if err := store.Put(context.Background(), state, oauth.FlowState{ExpiresAt: now}); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("expired Put error = %v", err)
	}
	if _, err := store.Consume(context.Background(), "not-state", oauth.StateBinding{}); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("invalid Consume error = %v", err)
	}
}

func TestValidStateRequiresThirtyTwoRandomBytes(t *testing.T) {
	for _, length := range []int{0, 31, 33} {
		state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", length)))
		if validState(state) {
			t.Fatalf("validState accepted %d bytes", length)
		}
	}
	if !validState(base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))) {
		t.Fatal("validState rejected 32 bytes")
	}
}
