package operations

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"slices"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAsymmetricEnvelopePublicWriterHasNoDecryptCapability(t *testing.T) {
	privateKey := testRSAKey(t)
	writer, err := NewAsymmetricEnvelopeCipher("rsa-2026", []AsymmetricKey{{ID: "rsa-2026", PublicKey: &privateKey.PublicKey}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("provider-token-not-retained")
	ciphertext, err := writer.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("version-3 envelope contains plaintext")
	}
	if keyID, enveloped, parseErr := CiphertextKeyID(ciphertext); parseErr != nil || !enveloped || keyID != "rsa-2026" || ciphertext[4] != keyEnvelopeVersionV3 {
		t.Fatalf("version-3 envelope key = %q, enveloped=%t, version=%d, err=%v", keyID, enveloped, ciphertext[4], parseErr)
	}
	if _, err := writer.Decrypt(context.Background(), ciphertext); !errors.Is(err, ErrDecryptNotAvailable) {
		t.Fatalf("public-only Decrypt error = %v", err)
	}
}

func TestAsymmetricEnvelopeReaderRejectsTamperingAndWrongPrivateKey(t *testing.T) {
	privateKey := testRSAKey(t)
	reader, err := NewAsymmetricEnvelopeCipher("active", []AsymmetricKey{{ID: "active", PublicKey: &privateKey.PublicKey, PrivateKey: privateKey}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := reader.Encrypt(context.Background(), []byte("credential"))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := reader.Decrypt(context.Background(), ciphertext)
	if err != nil || string(decrypted) != "credential" {
		t.Fatalf("Decrypt = %q, %v", decrypted, err)
	}
	decrypted, keyID, err := reader.DecryptWithKeyID(context.Background(), ciphertext)
	if err != nil || string(decrypted) != "credential" || keyID != "active" {
		t.Fatalf("DecryptWithKeyID = %q, %q, %v", decrypted, keyID, err)
	}
	if got := reader.ConfiguredKeyIDs(); !slices.Equal(got, []string{"active"}) {
		t.Fatalf("ConfiguredKeyIDs() = %v", got)
	}
	for _, offset := range []int{8, len(ciphertext) / 2, len(ciphertext) - 1} {
		tampered := append([]byte(nil), ciphertext...)
		tampered[offset] ^= 1
		if _, err := reader.Decrypt(context.Background(), tampered); !errors.Is(err, ErrInvalidKeyEnvelope) && !errors.Is(err, ErrUnknownKeyID) {
			t.Fatalf("tamper offset %d error = %v", offset, err)
		}
	}
	wrongPrivate := testRSAKey(t)
	if _, err := NewAsymmetricEnvelopeCipher("active", []AsymmetricKey{{ID: "active", PublicKey: &privateKey.PublicKey, PrivateKey: wrongPrivate}}, nil); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("mismatched private key error = %v", err)
	}
}

func TestAsymmetricEnvelopeMigratesVersionTwoAndRotatesRSAKeys(t *testing.T) {
	legacyMaterial := bytes.Repeat([]byte{'l'}, 32)
	legacyAES, err := integrations.NewAESGCMCipher(legacyMaterial)
	if err != nil {
		t.Fatal(err)
	}
	legacyKeyring, err := NewKeyringCipher("legacy", []CipherKey{{ID: "legacy", Cipher: legacyAES}}, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	legacyCiphertext, err := legacyKeyring.Encrypt(context.Background(), []byte("rotate-me"))
	if err != nil {
		t.Fatal(err)
	}

	oldRSA, newRSA := testRSAKey(t), testRSAKey(t)
	full, err := NewAsymmetricEnvelopeCipher("new-rsa", []AsymmetricKey{
		{ID: "old-rsa", PublicKey: &oldRSA.PublicKey, PrivateKey: oldRSA},
		{ID: "new-rsa", PublicKey: &newRSA.PublicKey, PrivateKey: newRSA},
	}, legacyKeyring)
	if err != nil {
		t.Fatal(err)
	}
	needs, err := full.NeedsReencryption(legacyCiphertext, "legacy")
	if err != nil || !needs {
		t.Fatalf("legacy NeedsReencryption = %t, %v", needs, err)
	}
	rotated, err := full.Reencrypt(context.Background(), legacyCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	if keyID, _, _ := CiphertextKeyID(rotated); keyID != "new-rsa" || rotated[4] != keyEnvelopeVersionV3 {
		t.Fatalf("rotated envelope key/version = %q/%d", keyID, rotated[4])
	}
	decrypted, err := full.Decrypt(context.Background(), rotated)
	if err != nil || string(decrypted) != "rotate-me" {
		t.Fatalf("rotated decrypt = %q, %v", decrypted, err)
	}
	needs, err = full.NeedsReencryption(rotated, "new-rsa")
	if err != nil || needs {
		t.Fatalf("active v3 NeedsReencryption = %t, %v", needs, err)
	}

	oldCipher, err := NewAsymmetricEnvelopeCipher("old-rsa", []AsymmetricKey{{ID: "old-rsa", PublicKey: &oldRSA.PublicKey, PrivateKey: oldRSA}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldCiphertext, err := oldCipher.Encrypt(context.Background(), []byte("rsa-rotation"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err = full.Reencrypt(context.Background(), oldCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	if keyID, _, _ := CiphertextKeyID(rotated); keyID != "new-rsa" {
		t.Fatalf("RSA rotation key ID = %q", keyID)
	}
}
