// Package cockroach persists OAuth authorization state in CockroachDB.
package cockroach

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
)

var ErrNilDB = errors.New("oauth cockroach: database is required")

const (
	putStateQuery = `
INSERT INTO auth_challenges (kind, challenge_hash, payload, expires_at)
VALUES ('oauth_state', $1, $2::JSONB, $3)`

	consumeStateQuery = `
UPDATE auth_challenges
SET consumed_at = now()
WHERE challenge_hash = $1
  AND kind = 'oauth_state'
  AND consumed_at IS NULL
  AND expires_at > now()
  AND payload->>'ProviderID' = $2
  AND payload->>'RedirectURI' = $3
  AND (
    (NOT $4::BOOL AND COALESCE(payload->>'SessionBindingHash', '') = '')
    OR (
      COALESCE(payload->>'SessionBindingHash', '') <> ''
      AND $5 <> ''
      AND payload->>'SessionBindingHash' = $5
    )
  )
RETURNING payload, expires_at`
)

// StateStore implements oauth.StateStore with an atomic UPDATE ... RETURNING
// consume. Browser state is represented only by its SHA-256 digest in storage;
// PKCE verifier material remains server-side inside the JSONB payload.
type StateStore struct {
	db  *sql.DB
	now func() time.Time
}

var _ oauth.StateStore = (*StateStore)(nil)

func New(db *sql.DB) (*StateStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	return &StateStore{db: db, now: time.Now}, nil
}

func (store *StateStore) Put(ctx context.Context, state string, flow oauth.FlowState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validState(state) || flow.ExpiresAt.IsZero() || !flow.ExpiresAt.After(store.now().UTC()) {
		return oauth.ErrInvalidState
	}
	payload, err := json.Marshal(flow)
	if err != nil {
		return fmt.Errorf("oauth cockroach: encode state: %w", err)
	}
	digest := sha256.Sum256([]byte(state))
	if _, err := store.db.ExecContext(ctx, putStateQuery, digest[:], payload, flow.ExpiresAt); err != nil {
		return fmt.Errorf("oauth cockroach: save state: %w", err)
	}
	return nil
}

func (store *StateStore) Consume(ctx context.Context, state string, binding oauth.StateBinding) (oauth.FlowState, error) {
	if err := ctx.Err(); err != nil {
		return oauth.FlowState{}, err
	}
	if !validState(state) {
		return oauth.FlowState{}, oauth.ErrInvalidState
	}
	digest := sha256.Sum256([]byte(state))
	var payload []byte
	var expiresAt time.Time
	// State is a replay-prevention preflight and is intentionally autocommitted
	// before provider network calls. The final connection mutation joins the
	// request's lazy state+audit transaction; holding this transaction across an
	// OAuth exchange would create avoidable contention and timeout aborts.
	err := store.db.QueryRowContext(ctx, consumeStateQuery, digest[:], binding.ProviderID, binding.RedirectURI, binding.RequireSessionBinding, binding.SessionBindingHash).Scan(&payload, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return oauth.FlowState{}, oauth.ErrInvalidState
	}
	if err != nil {
		return oauth.FlowState{}, fmt.Errorf("oauth cockroach: consume state: %w", err)
	}
	var flow oauth.FlowState
	if err := json.Unmarshal(payload, &flow); err != nil {
		return oauth.FlowState{}, fmt.Errorf("oauth cockroach: decode state: %w", err)
	}
	flow.ExpiresAt = expiresAt
	if strings.TrimSpace(flow.ProviderID) == "" || strings.TrimSpace(flow.ConnectionID) == "" ||
		strings.TrimSpace(flow.SubjectID) == "" || strings.TrimSpace(flow.RedirectURI) == "" ||
		strings.TrimSpace(flow.CodeVerifier) == "" {
		return oauth.FlowState{}, oauth.ErrInvalidState
	}
	flow.RequestedScopes = append([]string(nil), flow.RequestedScopes...)
	return flow, nil
}

func validState(state string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(state)
	return err == nil && len(decoded) == 32
}
