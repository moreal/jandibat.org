// Package cockroach persists OAuth authorization state in CockroachDB.
package cockroach

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	generated "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth/cockroach/generated"
)

var ErrNilDB = errors.New("oauth cockroach: database is required")

// StateStore implements oauth.StateStore with an atomic UPDATE ... RETURNING
// consume. Browser state is represented only by its SHA-256 digest in storage;
// PKCE verifier material remains server-side inside the JSONB payload.
type StateStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

var _ oauth.StateStore = (*StateStore)(nil)

func New(pool *pgxpool.Pool) (*StateStore, error) {
	if pool == nil {
		return nil, ErrNilDB
	}
	return &StateStore{pool: pool, now: time.Now}, nil
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
	if err := generated.InsertOAuthState(ctx, store.pool, digest[:], payload, flow.ExpiresAt); err != nil {
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
	// State is a replay-prevention preflight and is intentionally autocommitted
	// before provider network calls. The final connection mutation joins the
	// request's lazy state+audit transaction; holding this transaction across an
	// OAuth exchange would create avoidable contention and timeout aborts.
	row, err := generated.ConsumeOAuthState(ctx, store.pool, digest[:], binding.ProviderID, binding.RedirectURI, binding.RequireSessionBinding, binding.SessionBindingHash)
	if err != nil {
		return oauth.FlowState{}, fmt.Errorf("oauth cockroach: consume state: %w", err)
	}
	if row == nil {
		return oauth.FlowState{}, oauth.ErrInvalidState
	}
	var flow oauth.FlowState
	if err := json.Unmarshal(row.Payload, &flow); err != nil {
		return oauth.FlowState{}, fmt.Errorf("oauth cockroach: decode state: %w", err)
	}
	flow.ExpiresAt = row.ExpiresAt
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
