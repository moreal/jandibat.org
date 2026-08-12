package operations

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestKeyringWritesVersion2EnvelopeWithRandomPerRecordDEK(t *testing.T) {
	kek := testAESCipher(t, 'k')
	keyring, err := NewKeyringCipher("kek-2026-08", []CipherKey{{ID: "kek-2026-08", Cipher: kek}})
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("per-record envelope secret")
	first, err := keyring.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := keyring.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two writes of the same plaintext produced the same envelope")
	}

	firstEnvelope := requireVersion2Envelope(t, first, "kek-2026-08")
	secondEnvelope := requireVersion2Envelope(t, second, "kek-2026-08")
	firstDEK, err := kek.Decrypt(context.Background(), firstEnvelope.wrappedDEK)
	if err != nil {
		t.Fatalf("unwrap first DEK: %v", err)
	}
	defer clear(firstDEK)
	secondDEK, err := kek.Decrypt(context.Background(), secondEnvelope.wrappedDEK)
	if err != nil {
		t.Fatalf("unwrap second DEK: %v", err)
	}
	defer clear(secondDEK)
	if len(firstDEK) != dataEncryptionKeySize || len(secondDEK) != dataEncryptionKeySize {
		t.Fatalf("DEK lengths = %d, %d", len(firstDEK), len(secondDEK))
	}
	if bytes.Equal(firstDEK, secondDEK) {
		t.Fatal("two records reused a data encryption key")
	}
	if bytes.Contains(first, plaintext) || bytes.Contains(second, plaintext) {
		t.Fatal("envelope contains plaintext")
	}

	got, err := keyring.Decrypt(context.Background(), first)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt(version 2) = %q, %v", got, err)
	}
}

func TestVersion2EnvelopeAuthenticatesAllSecuritySensitiveRegions(t *testing.T) {
	keyring, err := NewKeyringCipher("kek-a", []CipherKey{
		{ID: "kek-a", Cipher: testAESCipher(t, 'a')},
		{ID: "kek-b", Cipher: testAESCipher(t, 'b')},
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := keyring.Encrypt(context.Background(), []byte("tamper evident"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := requireVersion2Envelope(t, original, "kek-a")

	keyIDOffset := keyEnvelopeV2HeaderSize
	wrappedOffset := keyIDOffset + len(envelope.keyID)
	nonceOffset := wrappedOffset + len(envelope.wrappedDEK)
	mutations := map[string]func([]byte) []byte{
		"version": func(value []byte) []byte {
			value[4] = 0xff
			return value
		},
		"key ID": func(value []byte) []byte {
			value[keyIDOffset+len(envelope.keyID)-1] = 'b'
			return value
		},
		"wrapped DEK": func(value []byte) []byte {
			value[nonceOffset-1] ^= 0x80
			return value
		},
		"nonce": func(value []byte) []byte {
			value[nonceOffset] ^= 0x80
			return value
		},
		"data ciphertext": func(value []byte) []byte {
			value[len(value)-1] ^= 0x80
			return value
		},
		"wrapped DEK length": func(value []byte) []byte {
			binary.BigEndian.PutUint32(value[7:11], ^uint32(0))
			return value
		},
		"truncation": func(value []byte) []byte {
			return value[:nonceOffset+12+15]
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			tampered := mutate(append([]byte(nil), original...))
			if _, err := keyring.Decrypt(context.Background(), tampered); !errors.Is(err, ErrInvalidKeyEnvelope) {
				t.Fatalf("Decrypt(tampered) error = %v, want ErrInvalidKeyEnvelope", err)
			}
		})
	}
}

func TestKeyringRotatesVersion2Version1AndLegacyCiphertext(t *testing.T) {
	oldKEK := testAESCipher(t, 'o')
	newKEK := testAESCipher(t, 'n')
	oldKeyring, err := NewKeyringCipher("old", []CipherKey{{ID: "old", Cipher: oldKEK}})
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := NewKeyringCipher("new", []CipherKey{
		{ID: "old", Cipher: oldKEK},
		{ID: "new", Cipher: newKEK},
	}, "new", "old")
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("rotatable secret")
	version2Old, err := oldKeyring.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	version1Payload, err := oldKEK.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	version1Old := mustMarshalKeyEnvelope(t, "old", version1Payload)
	legacyOld, err := oldKEK.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}

	for name, ciphertext := range map[string][]byte{
		"version 2 old KEK":  version2Old,
		"version 1 envelope": version1Old,
		"raw legacy":         legacyOld,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := keyring.Decrypt(context.Background(), ciphertext)
			if err != nil || !bytes.Equal(got, plaintext) {
				t.Fatalf("Decrypt() = %q, %v", got, err)
			}

			rotated, err := keyring.Reencrypt(context.Background(), ciphertext)
			if err != nil {
				t.Fatalf("Reencrypt() error = %v", err)
			}
			requireVersion2Envelope(t, rotated, "new")
			got, err = keyring.Decrypt(context.Background(), rotated)
			if err != nil || !bytes.Equal(got, plaintext) {
				t.Fatalf("Decrypt(rotated) = %q, %v", got, err)
			}
		})
	}
}

func TestKeyringReportsTheActualConfiguredDecryptKey(t *testing.T) {
	oldCipher := testAESCipher(t, 'o')
	newCipher := testAESCipher(t, 'n')
	oldKeyring, err := NewKeyringCipher("old", []CipherKey{{ID: "old", Cipher: oldCipher}})
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := NewKeyringCipher("new", []CipherKey{
		{ID: "old", Cipher: oldCipher},
		{ID: "new", Cipher: newCipher},
	}, "new", "old")
	if err != nil {
		t.Fatal(err)
	}
	if got := keyring.ConfiguredKeyIDs(); !slices.Equal(got, []string{"new", "old"}) {
		t.Fatalf("ConfiguredKeyIDs() = %v", got)
	}

	version2, err := oldKeyring.Encrypt(context.Background(), []byte("version-two"))
	if err != nil {
		t.Fatal(err)
	}
	version1Payload, err := oldCipher.Encrypt(context.Background(), []byte("version-one"))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := oldCipher.Encrypt(context.Background(), []byte("legacy"))
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		ciphertext []byte
		plaintext  string
	}{
		"version 2": {version2, "version-two"},
		"version 1": {mustMarshalKeyEnvelope(t, "old", version1Payload), "version-one"},
		"legacy":    {legacy, "legacy"},
	} {
		t.Run(name, func(t *testing.T) {
			plaintext, keyID, err := keyring.DecryptWithKeyID(context.Background(), test.ciphertext)
			if err != nil || string(plaintext) != test.plaintext || keyID != "old" {
				t.Fatalf("DecryptWithKeyID() = %q, %q, %v", plaintext, keyID, err)
			}
		})
	}
}

func TestKeyringClearsTransientDEKBuffers(t *testing.T) {
	kek := &observingKEK{}
	keyring, err := NewKeyringCipher("observed", []CipherKey{{ID: "observed", Cipher: kek}})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := keyring.Encrypt(context.Background(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !allZero(kek.lastEncryptPlaintext) {
		t.Fatal("generated DEK was not cleared after wrapping")
	}
	if _, err := keyring.Decrypt(context.Background(), ciphertext); err != nil {
		t.Fatal(err)
	}
	if !allZero(kek.lastDecryptPlaintext) {
		t.Fatal("unwrapped DEK was not cleared after data decryption")
	}
}

func TestKeyringRejectsUnknownMalformedAndUnconfiguredLegacyCiphertext(t *testing.T) {
	cipher := testAESCipher(t, 'k')
	keyring, err := NewKeyringCipher("known", []CipherKey{{ID: "known", Cipher: cipher}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cipher.Encrypt(context.Background(), []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Decrypt(context.Background(), mustMarshalKeyEnvelope(t, "missing", payload)); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("Decrypt(unknown) error = %v", err)
	}
	if _, err := keyring.Decrypt(context.Background(), []byte("JDBK")); !errors.Is(err, ErrInvalidKeyEnvelope) {
		t.Fatalf("Decrypt(truncated) error = %v", err)
	}
	if _, err := keyring.Decrypt(context.Background(), payload); !errors.Is(err, ErrInvalidKeyEnvelope) {
		t.Fatalf("Decrypt(legacy disabled) error = %v", err)
	}
	if _, err := NewKeyringCipher("missing", []CipherKey{{ID: "known", Cipher: cipher}}); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("NewKeyringCipher() error = %v", err)
	}
}

func TestCiphertextKeyIDRecognizesBothEnvelopeVersions(t *testing.T) {
	cipher := testAESCipher(t, 'k')
	keyring, err := NewKeyringCipher("known", []CipherKey{{ID: "known", Cipher: cipher}}, "known")
	if err != nil {
		t.Fatal(err)
	}
	version2, err := keyring.Encrypt(context.Background(), []byte("v2"))
	if err != nil {
		t.Fatal(err)
	}
	version1Payload, err := cipher.Encrypt(context.Background(), []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	version1 := mustMarshalKeyEnvelope(t, "known", version1Payload)
	legacy, err := cipher.Encrypt(context.Background(), []byte("legacy"))
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		ciphertext []byte
		keyID      string
		enveloped  bool
	}{
		"version 2": {version2, "known", true},
		"version 1": {version1, "known", true},
		"legacy":    {legacy, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			keyID, enveloped, err := CiphertextKeyID(test.ciphertext)
			if err != nil || keyID != test.keyID || enveloped != test.enveloped {
				t.Fatalf("CiphertextKeyID() = %q, %t, %v", keyID, enveloped, err)
			}
		})
	}
}

func TestKeyringNeedsReencryptionUsesEnvelopeVersionAndBothKeyIDs(t *testing.T) {
	activeCipher := testAESCipher(t, 'a')
	oldCipher := testAESCipher(t, 'o')
	activeKeyring, err := NewKeyringCipher("active", []CipherKey{
		{ID: "active", Cipher: activeCipher},
		{ID: "old", Cipher: oldCipher},
	}, "old")
	if err != nil {
		t.Fatal(err)
	}
	oldKeyring, err := NewKeyringCipher("old", []CipherKey{{ID: "old", Cipher: oldCipher}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := activeKeyring.Encrypt(context.Background(), []byte("current"))
	if err != nil {
		t.Fatal(err)
	}
	oldVersion2, err := oldKeyring.Encrypt(context.Background(), []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	version1Payload, err := activeCipher.Encrypt(context.Background(), []byte("version 1"))
	if err != nil {
		t.Fatal(err)
	}
	version1 := mustMarshalKeyEnvelope(t, "active", version1Payload)
	legacy, err := oldCipher.Encrypt(context.Background(), []byte("legacy"))
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		ciphertext    []byte
		persistedID   string
		wantMigration bool
	}{
		"current":                     {current, "active", false},
		"stale persisted key ID":      {current, "old", true},
		"old version 2 KEK":           {oldVersion2, "old", true},
		"active key version 1":        {version1, "active", true},
		"pre-envelope raw ciphertext": {legacy, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := activeKeyring.NeedsReencryption(test.ciphertext, test.persistedID)
			if err != nil || got != test.wantMigration {
				t.Fatalf("NeedsReencryption() = %t, %v, want %t", got, err, test.wantMigration)
			}
		})
	}
	if _, err := activeKeyring.NeedsReencryption([]byte("JDBK"), "active"); !errors.Is(err, ErrInvalidKeyEnvelope) {
		t.Fatalf("NeedsReencryption(malformed) error = %v", err)
	}
}

func requireVersion2Envelope(t *testing.T, ciphertext []byte, wantKeyID string) parsedKeyEnvelope {
	t.Helper()
	envelope, err := parseKeyEnvelope(ciphertext)
	if err != nil {
		t.Fatalf("parseKeyEnvelope() error = %v", err)
	}
	if !envelope.enveloped || envelope.version != keyEnvelopeVersionV2 || envelope.keyID != wantKeyID {
		t.Fatalf("envelope metadata = enveloped %t, version %d, key ID %q", envelope.enveloped, envelope.version, envelope.keyID)
	}
	if len(envelope.wrappedDEK) == 0 || len(envelope.nonce) != 12 || len(envelope.ciphertext) < 16 {
		t.Fatalf("envelope field lengths = wrapped %d, nonce %d, ciphertext %d", len(envelope.wrappedDEK), len(envelope.nonce), len(envelope.ciphertext))
	}
	keyID, enveloped, err := CiphertextKeyID(ciphertext)
	if err != nil || !enveloped || keyID != wantKeyID {
		t.Fatalf("CiphertextKeyID() = %q, %t, %v", keyID, enveloped, err)
	}
	return envelope
}

func mustMarshalKeyEnvelope(t *testing.T, keyID string, ciphertext []byte) []byte {
	t.Helper()
	envelope, err := marshalKeyEnvelope(keyID, ciphertext)
	if err != nil {
		t.Fatalf("marshal key envelope: %v", err)
	}
	return envelope
}

func testAESCipher(t *testing.T, fill byte) *integrations.AESGCMCipher {
	t.Helper()
	cipher, err := integrations.NewAESGCMCipher(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

type observingKEK struct {
	lastEncryptPlaintext []byte
	lastDecryptPlaintext []byte
}

func (cipher *observingKEK) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cipher.lastEncryptPlaintext = plaintext
	return append([]byte{0xa5}, plaintext...), nil
}

func (cipher *observingKEK) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ciphertext) != 1+dataEncryptionKeySize || ciphertext[0] != 0xa5 {
		return nil, fmt.Errorf("invalid test wrapped key")
	}
	cipher.lastDecryptPlaintext = append([]byte(nil), ciphertext[1:]...)
	return cipher.lastDecryptPlaintext, nil
}

func allZero(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	for _, octet := range value {
		if octet != 0 {
			return false
		}
	}
	return true
}
