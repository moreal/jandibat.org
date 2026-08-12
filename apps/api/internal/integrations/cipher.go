package integrations

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const ciphertextVersion byte = 1

// AESGCMCipher provides authenticated at-rest encryption using only the Go
// standard library. Production callers should obtain the key from a secret
// manager and rotate it outside this package.
type AESGCMCipher struct {
	aead cipher.AEAD
}

func NewAESGCMCipher(key []byte) (*AESGCMCipher, error) {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return nil, ErrInvalidEncryptionKey
	}
	block, err := aes.NewCipher(copyBytes(key))
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm cipher: %w", err)
	}
	return &AESGCMCipher{aead: aead}, nil
}

func (cipher *AESGCMCipher) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nonce := make([]byte, cipher.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	result := make([]byte, 1, 1+len(nonce)+len(plaintext)+cipher.aead.Overhead())
	result[0] = ciphertextVersion
	result = append(result, nonce...)
	result = cipher.aead.Seal(result, nonce, plaintext, nil)
	return result, nil
}

func (cipher *AESGCMCipher) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nonceSize := cipher.aead.NonceSize()
	if len(ciphertext) < 1+nonceSize+cipher.aead.Overhead() || ciphertext[0] != ciphertextVersion {
		return nil, ErrInvalidCiphertext
	}
	nonce := ciphertext[1 : 1+nonceSize]
	plaintext, err := cipher.aead.Open(nil, nonce, ciphertext[1+nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrInvalidCiphertext)
	}
	return plaintext, nil
}
