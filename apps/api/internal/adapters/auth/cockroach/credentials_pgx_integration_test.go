//go:build integration

package cockroach

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
)

func TestGeneratedCredentialsPreserveFieldsAndCounterFencing(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { api.Close(); admin.Close() })
	store, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	userID := "credential-generated-" + uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })

	now := time.Now().UTC().Truncate(time.Microsecond)
	first := coreauth.PasskeyCredential{
		ID: uuid.NewString(), UserID: userID, CredentialID: []byte("credential-" + uuid.NewString()),
		PublicKey: []byte("public-key"), AAGUID: []byte{1, 2, 3, 4}, SignCount: 7,
		Transports: []string{"internal", "hybrid"}, VerifierCredential: json.RawMessage(`{"v":1}`),
		Label: "Laptop", CreatedAt: now,
	}
	if err := store.SaveCredential(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCredential(ctx, first); !errors.Is(err, coreauth.ErrConflict) {
		t.Fatalf("duplicate credential = %v, want ErrConflict", err)
	}
	unknownUser := first
	unknownUser.ID = uuid.NewString()
	unknownUser.CredentialID = []byte("unknown-user-" + uuid.NewString())
	unknownUser.UserID = "missing-" + uuid.NewString()
	if err := store.SaveCredential(ctx, unknownUser); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("missing user = %v, want ErrNotFound", err)
	}
	second := first
	second.ID = uuid.NewString()
	second.CredentialID = []byte("empty-transport-" + uuid.NewString())
	second.Transports = []string{}
	second.AAGUID = nil
	second.Label = ""
	if err := store.SaveCredential(ctx, second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCredentialByCredentialID(ctx, first.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	var verifier struct {
		Version int `json:"v"`
	}
	if err := json.Unmarshal(loaded.VerifierCredential, &verifier); err != nil {
		t.Fatal(err)
	}
	if loaded.ID != first.ID || loaded.UserID != userID || loaded.SignCount != 7 || loaded.Label != "Laptop" ||
		verifier.Version != 1 || len(loaded.AAGUID) != 4 ||
		len(loaded.Transports) != 2 || loaded.Transports[0] != "internal" || loaded.Transports[1] != "hybrid" {
		t.Fatalf("generated credential mapping lost fields: %+v", loaded)
	}
	listed, err := store.ListCredentialsByUser(ctx, userID)
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListCredentialsByUser = (%d, %v), want two", len(listed), err)
	}
	secondLoaded, err := store.GetCredentialByCredentialID(ctx, second.CredentialID)
	if err != nil || secondLoaded.AAGUID != nil || len(secondLoaded.Transports) != 0 || secondLoaded.Label != "" {
		t.Fatalf("empty optional credential fields = (%+v, %v)", secondLoaded, err)
	}
	if _, err := store.GetCredentialByCredentialID(ctx, []byte("absent")); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("missing credential = %v, want ErrNotFound", err)
	}
	if err := store.UseCredential(ctx, []byte("absent"), 0, 1, json.RawMessage(`{"v":2}`), now); !errors.Is(err, coreauth.ErrNotFound) {
		t.Fatalf("update missing credential = %v, want ErrNotFound", err)
	}
	if err := store.UseCredential(ctx, first.CredentialID, 6, 8, json.RawMessage(`{"v":2}`), now); !errors.Is(err, coreauth.ErrConflict) {
		t.Fatalf("stale sign count = %v, want ErrConflict", err)
	}
	if err := store.UseCredential(ctx, first.CredentialID, 7, 7, json.RawMessage(`{"v":2}`), now); !errors.Is(err, coreauth.ErrConflict) {
		t.Fatalf("non-advancing sign count = %v, want ErrConflict", err)
	}
	if err := store.UseCredential(ctx, first.CredentialID, 7, 8, json.RawMessage(`{"v":2}`), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetCredentialByCredentialID(ctx, first.CredentialID)
	if err == nil {
		err = json.Unmarshal(loaded.VerifierCredential, &verifier)
	}
	if err != nil || loaded.SignCount != 8 || loaded.LastUsedAt == nil || !loaded.LastUsedAt.Equal(now.Add(time.Second)) ||
		verifier.Version != 2 {
		t.Fatalf("counter update = (%+v, %v)", loaded, err)
	}

	rollback := errors.New("rollback credential transaction")
	err = appdb.InTx(ctx, api, appdb.RetryOptions{}, func(txctx context.Context, _ pgx.Tx) error {
		if err := store.UseCredential(txctx, first.CredentialID, 8, 9, json.RawMessage(`{"v":3}`), now.Add(2*time.Second)); err != nil {
			return err
		}
		inside, err := store.GetCredentialByCredentialID(txctx, first.CredentialID)
		if err != nil || inside.SignCount != 9 {
			t.Fatalf("credential update did not join transaction: (%+v, %v)", inside, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction rollback = %v", err)
	}
	loaded, err = store.GetCredentialByCredentialID(ctx, first.CredentialID)
	if err != nil || loaded.SignCount != 8 {
		t.Fatalf("rolled-back credential update persisted: (%+v, %v)", loaded, err)
	}
}
