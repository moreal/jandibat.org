package cockroach

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestReencryptionRequiresPGXPoolWithoutUsingSQLFallback(t *testing.T) {
	script := fakedb.New(
		fakedb.Step{Operation: fakedb.Query, Columns: []string{"kind", "id", "key_id", "ciphertext"}, Rows: [][]driver.Value{{
			string(operations.SecretConnectionAccessToken), "018f0000-0000-7000-8000-000000000001", "old", []byte{1},
		}}},
		fakedb.Step{Operation: fakedb.Exec, Affected: 1},
	)
	db := script.Open()
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	if records, err := store.ListSecretsForReencryption(context.Background(), operations.SecretLocator{}, 1); err == nil {
		t.Fatalf("ListSecretsForReencryption() = %#v, expected a missing pgx pool error", records)
	}
	if changed, err := store.ReplaceEncryptedSecret(context.Background(), operations.EncryptedSecretRecord{
		Locator: operations.SecretLocator{Kind: operations.SecretConnectionAccessToken, ID: "018f0000-0000-7000-8000-000000000001"},
		KeyID:   "old", Ciphertext: []byte{1},
	}, "new", []byte{2}); err == nil {
		t.Fatalf("ReplaceEncryptedSecret() = %t, expected a missing pgx pool error", changed)
	}
	if calls := script.Calls(); len(calls) != 0 {
		t.Fatalf("re-encryption used SQL-only fallback: %#v", calls)
	}
}
