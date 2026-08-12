package operations

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
)

func TestReencryptionWorkerRotatesInPagesAndSkipsConcurrentWrite(t *testing.T) {
	oldCipher := testAESCipher(t, 'o')
	newCipher := testAESCipher(t, 'n')
	keyring, err := NewKeyringCipher("new", []CipherKey{{ID: "old", Cipher: oldCipher}, {ID: "new", Cipher: newCipher}}, "old")
	if err != nil {
		t.Fatal(err)
	}
	records := make([]EncryptedSecretRecord, 0, 3)
	for index, kind := range []SecretKind{SecretConnectionAccessToken, SecretConnectionRefreshToken} {
		ciphertext, encryptErr := oldCipher.Encrypt(context.Background(), []byte{byte('a' + index)})
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		records = append(records, EncryptedSecretRecord{Locator: SecretLocator{Kind: kind, ID: "id-1"}, KeyID: "old", Ciphertext: ciphertext})
	}
	store := &memoryReencryptionStore{records: records, skip: map[SecretLocator]bool{records[1].Locator: true}}
	worker, err := NewReencryptionWorker(store, keyring, 2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 2 || result.Rotated != 1 || result.Skipped != 1 || result.Failed != 0 || store.listCalls != 2 {
		t.Fatalf("result = %#v, list calls = %d", result, store.listCalls)
	}
	for _, record := range store.records {
		if store.skip[record.Locator] {
			if record.KeyID != "old" {
				t.Fatalf("concurrent record was replaced: %#v", record)
			}
			continue
		}
		if record.KeyID != "new" {
			t.Fatalf("record not rotated: %#v", record)
		}
		plaintext, decryptErr := keyring.Decrypt(context.Background(), record.Ciphertext)
		if decryptErr != nil || len(plaintext) != 1 {
			t.Fatalf("rotated ciphertext cannot be read: %q, %v", plaintext, decryptErr)
		}
	}
}

func TestReencryptionOperatorDryRunReportsKeysWithoutWrites(t *testing.T) {
	oldCipher := testAESCipher(t, 'o')
	newCipher := testAESCipher(t, 'n')
	keyring, _ := NewKeyringCipher("new", []CipherKey{{ID: "old", Cipher: oldCipher}, {ID: "new", Cipher: newCipher}}, "old")
	oldValue, _ := oldCipher.Encrypt(context.Background(), []byte("old"))
	store := &memoryReencryptionStore{records: []EncryptedSecretRecord{{
		Locator: SecretLocator{Kind: SecretConnectionAccessToken, ID: "id-1"}, KeyID: "old", Ciphertext: mustMarshalKeyEnvelope(t, "old", oldValue),
	}}}
	worker, _ := NewReencryptionWorker(store, keyring, 1)
	result, err := worker.RunOperator(context.Background(), nil, ReencryptionOperatorOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Pending != 1 || result.Rotated != 0 || result.ByKey["old"].Pending != 1 || store.replaceCalls != 0 {
		t.Fatalf("result=%#v replaceCalls=%d", result, store.replaceCalls)
	}
}

func TestReencryptionOperatorResumesAfterPersistedPage(t *testing.T) {
	oldCipher := testAESCipher(t, 'o')
	newCipher := testAESCipher(t, 'n')
	keyring, _ := NewKeyringCipher("new", []CipherKey{{ID: "old", Cipher: oldCipher}, {ID: "new", Cipher: newCipher}}, "old")
	var records []EncryptedSecretRecord
	for _, id := range []string{"a", "b", "c"} {
		payload, _ := oldCipher.Encrypt(context.Background(), []byte(id))
		records = append(records, EncryptedSecretRecord{Locator: SecretLocator{Kind: SecretConnectionAccessToken, ID: id}, KeyID: "old", Ciphertext: mustMarshalKeyEnvelope(t, "old", payload)})
	}
	store := &memoryReencryptionStore{records: records, listErrorAt: 2}
	worker, _ := NewReencryptionWorker(store, keyring, 1)
	checkpoints := newMemoryCheckpointStore()
	result, err := worker.RunOperator(context.Background(), checkpoints, ReencryptionOperatorOptions{Scope: "rotation-1"})
	if err == nil || result.Rotated != 1 {
		t.Fatalf("first result=%#v error=%v", result, err)
	}
	store.listErrorAt = 0
	result, err = worker.RunOperator(context.Background(), checkpoints, ReencryptionOperatorOptions{Scope: "rotation-1", Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || result.Rotated != 3 || result.Scanned != 3 || len(checkpoints.values) != 0 {
		t.Fatalf("resumed result=%#v checkpoints=%#v", result, checkpoints.values)
	}
}

func TestReencryptionWorkerMigratesActiveKeyVersion1AndSkipsCurrentVersion2(t *testing.T) {
	activeCipher := testAESCipher(t, 'n')
	keyring, err := NewKeyringCipher("active", []CipherKey{{ID: "active", Cipher: activeCipher}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := keyring.Encrypt(context.Background(), []byte("already current"))
	if err != nil {
		t.Fatal(err)
	}
	version1Payload, err := activeCipher.Encrypt(context.Background(), []byte("migrate me"))
	if err != nil {
		t.Fatal(err)
	}
	version1 := mustMarshalKeyEnvelope(t, "active", version1Payload)
	store := &memoryReencryptionStore{records: []EncryptedSecretRecord{
		{Locator: SecretLocator{Kind: SecretConnectionAccessToken, ID: "current"}, KeyID: "active", Ciphertext: current},
		{Locator: SecretLocator{Kind: SecretConnectionAccessToken, ID: "version-1"}, KeyID: "active", Ciphertext: version1},
	}}
	worker, err := NewReencryptionWorker(store, keyring, 1)
	if err != nil {
		t.Fatal(err)
	}

	result, err := worker.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 2 || result.Rotated != 1 || result.Skipped != 1 || result.Failed != 0 || store.listCalls != 3 {
		t.Fatalf("result = %#v, list calls = %d", result, store.listCalls)
	}
	if !bytes.Equal(store.records[0].Ciphertext, current) {
		t.Fatal("already-current version-2 record was rewritten")
	}
	migrated, parseErr := parseKeyEnvelope(store.records[1].Ciphertext)
	if parseErr != nil || migrated.version != keyEnvelopeVersionV2 || migrated.keyID != "active" {
		t.Fatalf("migrated envelope = %#v, %v", migrated, parseErr)
	}
	plaintext, err := keyring.Decrypt(context.Background(), store.records[1].Ciphertext)
	if err != nil || string(plaintext) != "migrate me" {
		t.Fatalf("Decrypt(migrated) = %q, %v", plaintext, err)
	}
}

func TestReencryptionWorkerIsolatesCorruptRecord(t *testing.T) {
	oldCipher := testAESCipher(t, 'o')
	newCipher := testAESCipher(t, 'n')
	keyring, _ := NewKeyringCipher("new", []CipherKey{{ID: "old", Cipher: oldCipher}, {ID: "new", Cipher: newCipher}}, "old")
	valid, _ := oldCipher.Encrypt(context.Background(), []byte("valid"))
	store := &memoryReencryptionStore{records: []EncryptedSecretRecord{
		{Locator: SecretLocator{Kind: SecretConnectionAccessToken, ID: "bad"}, KeyID: "old", Ciphertext: []byte("corrupt")},
		{Locator: SecretLocator{Kind: SecretConnectionRefreshToken, ID: "good"}, KeyID: "old", Ciphertext: valid},
	}}
	worker, _ := NewReencryptionWorker(store, keyring, 10)
	result, err := worker.Run(context.Background())
	if err == nil || result.Failed != 1 || result.Rotated != 1 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

type memoryReencryptionStore struct {
	records      []EncryptedSecretRecord
	skip         map[SecretLocator]bool
	listCalls    int
	replaceCalls int
	listErrorAt  int
}

func (store *memoryReencryptionStore) ListSecretsForReencryption(_ context.Context, after SecretLocator, limit int) ([]EncryptedSecretRecord, error) {
	store.listCalls++
	if store.listErrorAt > 0 && store.listCalls == store.listErrorAt {
		return nil, errors.New("interrupted")
	}
	ordered := append([]EncryptedSecretRecord(nil), store.records...)
	sort.Slice(ordered, func(i, j int) bool { return compareSecretLocators(ordered[i].Locator, ordered[j].Locator) < 0 })
	result := make([]EncryptedSecretRecord, 0, limit)
	for _, record := range ordered {
		if compareSecretLocators(record.Locator, after) <= 0 {
			continue
		}
		record.Ciphertext = append([]byte(nil), record.Ciphertext...)
		result = append(result, record)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (store *memoryReencryptionStore) ReplaceEncryptedSecret(_ context.Context, expected EncryptedSecretRecord, newKeyID string, ciphertext []byte) (bool, error) {
	store.replaceCalls++
	if store.skip[expected.Locator] {
		return false, nil
	}
	for index := range store.records {
		record := &store.records[index]
		if record.Locator == expected.Locator && record.KeyID == expected.KeyID && bytes.Equal(record.Ciphertext, expected.Ciphertext) {
			record.KeyID = newKeyID
			record.Ciphertext = append([]byte(nil), ciphertext...)
			return true, nil
		}
	}
	return false, nil
}
