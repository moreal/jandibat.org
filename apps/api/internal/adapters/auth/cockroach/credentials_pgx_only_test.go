package cockroach

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

// Credential persistence must not silently fall back to a second SQL path.
// The running API supplies a pgx pool and all credential statements are
// generated from the baseline schema.
func TestCredentialOperationsRequireGeneratedPGXStore(t *testing.T) {
	db := fakedb.New().Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	credential := coreauth.PasskeyCredential{
		ID: uuid.NewString(), UserID: "user-1", CredentialID: []byte("passkey-id"),
		PublicKey: []byte("public-key"), VerifierCredential: json.RawMessage(`{"v":1}`),
		CreatedAt: time.Now().UTC(),
	}
	ctx := context.Background()
	if err := store.SaveCredential(ctx, credential); !errors.Is(err, ErrNilDB) {
		t.Fatalf("SaveCredential(sql.DB) = %v, want ErrNilDB", err)
	}
	if _, err := store.GetCredentialByCredentialID(ctx, credential.CredentialID); !errors.Is(err, ErrNilDB) {
		t.Fatalf("GetCredentialByCredentialID(sql.DB) = %v, want ErrNilDB", err)
	}
	if _, err := store.ListCredentialsByUser(ctx, credential.UserID); !errors.Is(err, ErrNilDB) {
		t.Fatalf("ListCredentialsByUser(sql.DB) = %v, want ErrNilDB", err)
	}
	if err := store.UseCredential(ctx, credential.CredentialID, 0, 1, json.RawMessage(`{"v":2}`), time.Now().UTC()); !errors.Is(err, ErrNilDB) {
		t.Fatalf("UseCredential(sql.DB) = %v, want ErrNilDB", err)
	}
}
