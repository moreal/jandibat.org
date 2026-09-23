//go:build integration

package scytheprobe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	queries "github.com/moreal/jandibat.org/apps/api/internal/adapters/scytheprobe/generated"
)

func TestGeneratedCockroachQueriesInTransaction(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	userID := "scythe-probe-" + uuid.NewString()
	user, err := queries.UpsertProbeUser(ctx, tx, userID, userID+"@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if user.Id != userID || user.EmailVerifiedAt != nil {
		t.Fatalf("unexpected generated STRING/nullable TIMESTAMPTZ row: %+v", user)
	}

	credentialID := []byte("scythe-probe-" + uuid.NewString())
	transports := []string{"internal", "usb"}
	verifier := json.RawMessage(`{"type":"public-key"}`)
	passkey, err := queries.CreateProbePasskey(ctx, tx, userID, credentialID,
		[]byte("public-key"), &transports, verifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	if passkey.Id == uuid.Nil || passkey.UserId != userID || !bytes.Equal(passkey.CredentialId, credentialID) ||
		passkey.Transports == nil || !reflect.DeepEqual(*passkey.Transports, transports) ||
		passkey.LastUsedAt != nil {
		t.Fatalf("unexpected generated UUID/BYTES/JSONB/STRING[]/nullable row: %+v", passkey)
	}
	var verifierResult map[string]string
	if err := json.Unmarshal(passkey.VerifierCredential, &verifierResult); err != nil || verifierResult["type"] != "public-key" {
		t.Fatalf("unexpected JSONB result: %s (%v)", passkey.VerifierCredential, err)
	}

	listed, err := queries.ListProbePasskeys(ctx, tx, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Id != passkey.Id {
		t.Fatalf("ANY(array) returned %+v", listed)
	}
	count, err := queries.CountProbePasskeys(ctx, tx, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	if count.Total != 1 {
		t.Fatalf("aggregate count = %d, want 1", count.Total)
	}
	usedAt := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	marked, err := queries.MarkProbePasskeyUsed(ctx, tx, passkey.Id, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Id != passkey.Id || marked.LastUsedAt == nil || !marked.LastUsedAt.Equal(usedAt) {
		t.Fatalf("RETURNING UUID/TIMESTAMPTZ = %+v", marked)
	}
}
