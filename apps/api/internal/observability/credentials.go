package observability

import (
	"context"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

// InstrumentCredentialCipher records which configured key actually decrypts
// each credential. Unknown envelope IDs collapse to one label and are never
// copied into the Prometheus surface.
func InstrumentCredentialCipher(next operations.RotatingSecretCipher, registry *Registry) operations.RotatingSecretCipher {
	if next == nil || registry == nil {
		return next
	}
	allowed := make(map[string]struct{})
	if identifying, ok := next.(operations.KeyIdentifyingSecretCipher); ok {
		for _, id := range identifying.ConfiguredKeyIDs() {
			allowed[id] = struct{}{}
		}
	}
	return &observedCredentialCipher{next: next, registry: registry, allowed: allowed}
}

type observedCredentialCipher struct {
	next     operations.RotatingSecretCipher
	registry *Registry
	allowed  map[string]struct{}
}

func (cipher *observedCredentialCipher) ActiveKeyID() string { return cipher.next.ActiveKeyID() }

func (cipher *observedCredentialCipher) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	return cipher.next.Encrypt(ctx, plaintext)
}

func (cipher *observedCredentialCipher) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	var plaintext []byte
	var keyID string
	var err error
	if identifying, ok := cipher.next.(operations.KeyIdentifyingSecretCipher); ok {
		plaintext, keyID, err = identifying.DecryptWithKeyID(ctx, ciphertext)
	} else {
		plaintext, err = cipher.next.Decrypt(ctx, ciphertext)
		if parsed, enveloped, parseErr := operations.CiphertextKeyID(ciphertext); parseErr == nil && enveloped {
			keyID = parsed
		} else {
			keyID = "legacy"
		}
	}
	cipher.registry.observeCredentialDecrypt(cipher.boundedKeyID(keyID), decryptOutcome(err))
	return plaintext, err
}

func (cipher *observedCredentialCipher) NeedsReencryption(ciphertext []byte, persistedKeyID string) (bool, error) {
	return cipher.next.NeedsReencryption(ciphertext, persistedKeyID)
}

func (cipher *observedCredentialCipher) Reencrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := cipher.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	return cipher.next.Encrypt(ctx, plaintext)
}

func (cipher *observedCredentialCipher) boundedKeyID(keyID string) string {
	if _, ok := cipher.allowed[keyID]; ok {
		return keyID
	}
	if keyID == "legacy" {
		return keyID
	}
	return "unknown"
}

func decryptOutcome(err error) string {
	if err == nil {
		return "succeeded"
	}
	return "failed"
}

var _ operations.RotatingSecretCipher = (*observedCredentialCipher)(nil)
