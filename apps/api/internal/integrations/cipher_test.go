package integrations

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestAESGCMCipherRoundTripAndAuthentication(t *testing.T) {
	t.Parallel()
	cipher := mustTestCipher()
	plaintext := []byte("not-stored-in-plaintext")

	first, err := cipher.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	second, err := cipher.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("Encrypt() second error = %v", err)
	}
	if bytes.Equal(first, plaintext) || bytes.Contains(first, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}
	if bytes.Equal(first, second) {
		t.Fatal("encryption reused a nonce")
	}
	decrypted, err := cipher.Decrypt(context.Background(), first)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt() = %q, want %q", decrypted, plaintext)
	}

	first[len(first)-1] ^= 0xff
	if _, err := cipher.Decrypt(context.Background(), first); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("Decrypt(tampered) error = %v, want ErrInvalidCiphertext", err)
	}
}

func TestAESGCMCipherRejectsInvalidKeys(t *testing.T) {
	t.Parallel()
	if _, err := NewAESGCMCipher([]byte("too-short")); !errors.Is(err, ErrInvalidEncryptionKey) {
		t.Fatalf("NewAESGCMCipher() error = %v, want ErrInvalidEncryptionKey", err)
	}
}
