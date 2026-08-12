package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInstrumentCredentialCipherReportsOnlyConfiguredKeyIDs(t *testing.T) {
	registry := NewRegistry(Resource{BuildSHA: "abc123", Environment: "staging", Region: "icn"})
	next := &credentialCipherTestDouble{keyID: "old-key"}
	cipher := InstrumentCredentialCipher(next, registry)
	if plaintext, err := cipher.Decrypt(context.Background(), []byte("ciphertext")); err != nil || string(plaintext) != "plaintext" {
		t.Fatalf("decrypt=%q err=%v", plaintext, err)
	}
	next.keyID = "attacker-controlled-envelope-id"
	next.err = errors.New("unknown key")
	if _, err := cipher.Decrypt(context.Background(), []byte("corrupt")); err == nil {
		t.Fatal("corrupt decrypt unexpectedly succeeded")
	}

	body := scrape(t, registry)
	for _, fragment := range []string{
		`credential_decrypt_operations_total{key_id="old-key",outcome="succeeded",build_sha="abc123",environment="staging",region="icn"} 1`,
		`credential_decrypt_operations_total{key_id="unknown",outcome="failed",build_sha="abc123",environment="staging",region="icn"} 1`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("metrics missing %q\n%s", fragment, body)
		}
	}
	if strings.Contains(body, "attacker-controlled-envelope-id") {
		t.Fatalf("unconfigured key ID reached metric labels:\n%s", body)
	}
}

type credentialCipherTestDouble struct {
	keyID string
	err   error
}

func (*credentialCipherTestDouble) ActiveKeyID() string { return "new-key" }
func (*credentialCipherTestDouble) ConfiguredKeyIDs() []string {
	return []string{"new-key", "old-key"}
}
func (*credentialCipherTestDouble) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	return append([]byte("encrypted:"), plaintext...), nil
}
func (cipher *credentialCipherTestDouble) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, _, err := cipher.DecryptWithKeyID(ctx, ciphertext)
	return plaintext, err
}
func (cipher *credentialCipherTestDouble) DecryptWithKeyID(context.Context, []byte) ([]byte, string, error) {
	if cipher.err != nil {
		return nil, cipher.keyID, cipher.err
	}
	return []byte("plaintext"), cipher.keyID, nil
}
func (*credentialCipherTestDouble) NeedsReencryption([]byte, string) (bool, error) { return true, nil }
func (cipher *credentialCipherTestDouble) Reencrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := cipher.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, err
	}
	return cipher.Encrypt(ctx, plaintext)
}
