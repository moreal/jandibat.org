package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
)

func TestStateStorePutHashesBrowserState(t *testing.T) {
	script := fakedb.New(fakedb.Step{Operation: fakedb.Exec, Affected: 1})
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	flow := oauth.FlowState{
		ProviderID:      "github",
		ConnectionID:    "connection-1",
		SubjectID:       "subject-1",
		RedirectURI:     "https://api.example.com/v1/integrations/github/callback",
		CodeVerifier:    "server-only-verifier",
		RequestedScopes: []string{"read:user"},
		ExpiresAt:       now.Add(time.Minute),
	}
	if err := store.Put(context.Background(), state, flow); err != nil {
		t.Fatalf("put state: %v", err)
	}
	calls := script.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "INSERT INTO auth_challenges") {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	wantDigest := sha256.Sum256([]byte(state))
	gotDigest, ok := calls[0].Args[0].Value.([]byte)
	if !ok || string(gotDigest) != string(wantDigest[:]) {
		t.Fatalf("stored digest = %x, want %x", gotDigest, wantDigest)
	}
	payload, ok := calls[0].Args[1].Value.([]byte)
	if !ok || strings.Contains(string(payload), state) {
		t.Fatalf("stored payload contains raw browser state: %s", payload)
	}
}

func TestStateStoreConsumesAtomically(t *testing.T) {
	expiresAt := time.Date(2026, 8, 12, 0, 10, 0, 0, time.UTC)
	want := oauth.FlowState{
		ProviderID: "gitlab", ConnectionID: "connection-1", SubjectID: "subject-1",
		RedirectURI:  "https://api.example.com/v1/integrations/gitlab/callback",
		CodeVerifier: "verifier", RequestedScopes: []string{"read_user"}, ExpiresAt: expiresAt,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal flow: %v", err)
	}
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"payload", "expires_at"}, Rows: [][]driver.Value{{payload, expiresAt}}},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"payload", "expires_at"}},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))

	binding := oauth.StateBinding{ProviderID: want.ProviderID, RedirectURI: want.RedirectURI}
	got, err := store.Consume(context.Background(), state, binding)
	if err != nil {
		t.Fatalf("consume state: %v", err)
	}
	if got.ProviderID != want.ProviderID || got.ConnectionID != want.ConnectionID || got.CodeVerifier != want.CodeVerifier || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("consumed flow = %#v, want %#v", got, want)
	}
	if _, err := store.Consume(context.Background(), state, binding); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("replayed consume error = %v, want ErrInvalidState", err)
	}
	for _, call := range script.Calls() {
		if !strings.Contains(call.Query, "consumed_at IS NULL") || !strings.Contains(call.Query, "UPDATE auth_challenges") ||
			!strings.Contains(call.Query, "payload->>'SessionBindingHash'") {
			t.Fatalf("consume is not an atomic claim: %s", call.Query)
		}
	}
}

func TestStateStoreBindingMismatchDoesNotConsume(t *testing.T) {
	expiresAt := time.Date(2026, 8, 12, 0, 10, 0, 0, time.UTC)
	want := oauth.FlowState{
		ProviderID: "github", ConnectionID: "connection-1", SubjectID: "subject-1",
		RedirectURI: "https://api.example.com/v1/integrations/github/callback", CodeVerifier: "verifier",
		SessionBindingHash: "correct-session-digest", ExpiresAt: expiresAt,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal flow: %v", err)
	}
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"payload", "expires_at"}},
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"payload", "expires_at"}, Rows: [][]driver.Value{{payload, expiresAt}}},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	binding := oauth.StateBinding{
		ProviderID: "github", RedirectURI: want.RedirectURI,
		SessionBindingHash: "wrong-session-digest", RequireSessionBinding: true,
	}
	if _, err := store.Consume(context.Background(), state, binding); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("mismatched binding error = %v, want ErrInvalidState", err)
	}
	binding.SessionBindingHash = want.SessionBindingHash
	if _, err := store.Consume(context.Background(), state, binding); err != nil {
		t.Fatalf("matching binding after mismatch: %v", err)
	}
	calls := script.Calls()
	if len(calls) != 2 || calls[0].Args[4].Value != "wrong-session-digest" || calls[1].Args[4].Value != "correct-session-digest" {
		t.Fatalf("binding predicates were not passed exactly: %#v", calls)
	}
}

func TestStateStoreRejectsInvalidInput(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilDB) {
		t.Fatalf("New(nil) error = %v", err)
	}
	script := fakedb.New()
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, _ := New(db)
	if err := store.Put(context.Background(), "not-state", oauth.FlowState{}); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("invalid Put error = %v", err)
	}
	if _, err := store.Consume(context.Background(), "not-state", oauth.StateBinding{}); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("invalid Consume error = %v", err)
	}
}
