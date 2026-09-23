package cockroach

import (
	"context"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestReencryptionRequiresPGXPool(t *testing.T) {
	store := &Store{}

	if records, err := store.ListSecretsForReencryption(context.Background(), operations.SecretLocator{}, 1); err == nil {
		t.Fatalf("ListSecretsForReencryption() = %#v, expected a missing pgx pool error", records)
	}
	if changed, err := store.ReplaceEncryptedSecret(context.Background(), operations.EncryptedSecretRecord{
		Locator: operations.SecretLocator{Kind: operations.SecretConnectionAccessToken, ID: "018f0000-0000-7000-8000-000000000001"},
		KeyID:   "old", Ciphertext: []byte{1},
	}, "new", []byte{2}); err == nil {
		t.Fatalf("ReplaceEncryptedSecret() = %t, expected a missing pgx pool error", changed)
	}
}
