package operations

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

var ErrDecryptNotAvailable = errors.New("operations: credential decryption is not available in this process")

// AsymmetricKey contains the non-secret RSA public key used by API writers and
// an optional private key used only by worker/maintenance readers. RSA-OAEP is
// used solely to wrap a random per-record DEK; credential bytes remain under
// AES-256-GCM authenticated encryption.
type AsymmetricKey struct {
	ID         string
	PublicKey  *rsa.PublicKey
	PrivateKey *rsa.PrivateKey
}

// AsymmetricEnvelopeCipher emits version-3 envelopes. An instance built with
// public keys only is genuinely encrypt-only: it contains neither a symmetric
// KEK nor an RSA private key and rejects every decrypt request.
type AsymmetricEnvelopeCipher struct {
	activeID string
	keys     map[string]AsymmetricKey
	legacy   integrations.SecretCipher
}

var _ RotatingSecretCipher = (*AsymmetricEnvelopeCipher)(nil)

func NewAsymmetricEnvelopeCipher(activeID string, keys []AsymmetricKey, legacy integrations.SecretCipher) (*AsymmetricEnvelopeCipher, error) {
	activeID = strings.TrimSpace(activeID)
	if activeID == "" {
		return nil, fmt.Errorf("%w: active key ID is required", ErrInvalidKeyring)
	}
	configured := make(map[string]AsymmetricKey, len(keys))
	for _, key := range keys {
		id := strings.TrimSpace(key.ID)
		if id == "" || id != key.ID || len(id) > math.MaxUint16 || key.PublicKey == nil || key.PublicKey.N == nil || key.PublicKey.E < 3 || key.PublicKey.Size() < 256 {
			return nil, fmt.Errorf("%w: valid key ID and RSA-2048-or-stronger public key are required", ErrInvalidKeyring)
		}
		if key.PrivateKey != nil {
			if err := key.PrivateKey.Validate(); err != nil || key.PrivateKey.PublicKey.E != key.PublicKey.E || subtle.ConstantTimeCompare(key.PrivateKey.PublicKey.N.Bytes(), key.PublicKey.N.Bytes()) != 1 {
				return nil, fmt.Errorf("%w: private key does not match public key %q", ErrInvalidKeyring, id)
			}
		}
		if _, duplicate := configured[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate key ID %q", ErrInvalidKeyring, id)
		}
		configured[id] = key
	}
	if _, ok := configured[activeID]; !ok {
		return nil, fmt.Errorf("%w: active key %q is not configured", ErrInvalidKeyring, activeID)
	}
	return &AsymmetricEnvelopeCipher{activeID: activeID, keys: configured, legacy: legacy}, nil
}

func (cipher *AsymmetricEnvelopeCipher) ActiveKeyID() string { return cipher.activeID }

func (cipher *AsymmetricEnvelopeCipher) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dek := make([]byte, dataEncryptionKeySize)
	defer clear(dek)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("generate data encryption key: %w", err)
	}
	key := cipher.keys[cipher.activeID]
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key.PublicKey, dek, asymmetricWrapLabel(cipher.activeID))
	if err != nil {
		return nil, fmt.Errorf("wrap data encryption key with public key %q: %w", cipher.activeID, err)
	}
	defer clear(wrapped)
	dataAEAD, err := newDataAEAD(dek)
	if err != nil {
		return nil, err
	}
	return marshalWrappedKeyEnvelope(keyEnvelopeVersionV3, cipher.activeID, wrapped, plaintext, dataAEAD)
}

func (cipher *AsymmetricEnvelopeCipher) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, _, err := cipher.DecryptWithKeyID(ctx, ciphertext)
	return plaintext, err
}

func (cipher *AsymmetricEnvelopeCipher) DecryptWithKeyID(ctx context.Context, ciphertext []byte) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	envelope, err := parseKeyEnvelope(ciphertext)
	if err != nil {
		return nil, "", err
	}
	if envelope.enveloped && envelope.version == keyEnvelopeVersionV3 {
		key, ok := cipher.keys[envelope.keyID]
		if !ok {
			return nil, envelope.keyID, fmt.Errorf("%w: %q", ErrUnknownKeyID, envelope.keyID)
		}
		if key.PrivateKey == nil {
			return nil, envelope.keyID, ErrDecryptNotAvailable
		}
		dek, decryptErr := rsa.DecryptOAEP(sha256.New(), rand.Reader, key.PrivateKey, envelope.wrappedDEK, asymmetricWrapLabel(envelope.keyID))
		if decryptErr != nil {
			return nil, envelope.keyID, fmt.Errorf("%w: unwrap data encryption key", ErrInvalidKeyEnvelope)
		}
		defer clear(dek)
		if len(dek) != dataEncryptionKeySize {
			return nil, envelope.keyID, fmt.Errorf("%w: unwrapped data encryption key has length %d", ErrInvalidKeyEnvelope, len(dek))
		}
		dataAEAD, err := newDataAEAD(dek)
		if err != nil {
			return nil, envelope.keyID, fmt.Errorf("%w: create data cipher", ErrInvalidKeyEnvelope)
		}
		plaintext, err := dataAEAD.Open(nil, envelope.nonce, envelope.ciphertext, envelope.aad)
		if err != nil {
			return nil, envelope.keyID, fmt.Errorf("%w: data authentication failed", ErrInvalidKeyEnvelope)
		}
		return plaintext, envelope.keyID, nil
	}
	if cipher.legacy == nil {
		return nil, "", ErrDecryptNotAvailable
	}
	if identifying, ok := cipher.legacy.(KeyIdentifyingSecretCipher); ok {
		return identifying.DecryptWithKeyID(ctx, ciphertext)
	}
	plaintext, decryptErr := cipher.legacy.Decrypt(ctx, ciphertext)
	return plaintext, "legacy", decryptErr
}

func (cipher *AsymmetricEnvelopeCipher) ConfiguredKeyIDs() []string {
	set := make(map[string]struct{}, len(cipher.keys))
	for id := range cipher.keys {
		set[id] = struct{}{}
	}
	if identifying, ok := cipher.legacy.(KeyIdentifyingSecretCipher); ok {
		for _, id := range identifying.ConfiguredKeyIDs() {
			set[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (cipher *AsymmetricEnvelopeCipher) NeedsReencryption(ciphertext []byte, persistedKeyID string) (bool, error) {
	envelope, err := parseKeyEnvelope(ciphertext)
	if err != nil {
		return false, err
	}
	return !envelope.enveloped || envelope.version != keyEnvelopeVersionV3 || envelope.keyID != cipher.activeID || persistedKeyID != cipher.activeID, nil
}

func (cipher *AsymmetricEnvelopeCipher) Reencrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := cipher.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	return cipher.Encrypt(ctx, plaintext)
}

func asymmetricWrapLabel(keyID string) []byte {
	return []byte("jandibat.org/credential-dek/v3\x00" + keyID)
}

func marshalWrappedKeyEnvelope(version byte, keyID string, wrappedDEK, plaintext []byte, dataAEAD interface {
	NonceSize() int
	Overhead() int
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
}) ([]byte, error) {
	if version != keyEnvelopeVersionV2 && version != keyEnvelopeVersionV3 {
		return nil, fmt.Errorf("%w: invalid wrapped envelope version", ErrInvalidKeyEnvelope)
	}
	if len(keyID) == 0 || len(keyID) > math.MaxUint16 || len(wrappedDEK) == 0 || uint64(len(wrappedDEK)) > math.MaxUint32 || dataAEAD == nil {
		return nil, fmt.Errorf("%w: invalid key ID, wrapped key, or data cipher", ErrInvalidKeyEnvelope)
	}
	prefixLength := keyEnvelopeV2HeaderSize + len(keyID) + len(wrappedDEK)
	if prefixLength > int(^uint(0)>>1)-dataAEAD.NonceSize()-len(plaintext)-dataAEAD.Overhead() {
		return nil, fmt.Errorf("%w: envelope is too large", ErrInvalidKeyEnvelope)
	}
	result := make([]byte, prefixLength+dataAEAD.NonceSize(), prefixLength+dataAEAD.NonceSize()+len(plaintext)+dataAEAD.Overhead())
	copy(result[:4], keyEnvelopeMagic[:])
	result[4] = version
	// #nosec G115 -- lengths are explicitly bounded above.
	result[5], result[6] = byte(len(keyID)>>8), byte(len(keyID))
	wrappedLength := uint32(len(wrappedDEK)) // #nosec G115 -- bounded above.
	result[7], result[8], result[9], result[10] = byte(wrappedLength>>24), byte(wrappedLength>>16), byte(wrappedLength>>8), byte(wrappedLength)
	copy(result[keyEnvelopeV2HeaderSize:], keyID)
	copy(result[keyEnvelopeV2HeaderSize+len(keyID):], wrappedDEK)
	nonceOffset := prefixLength
	nonce := result[nonceOffset : nonceOffset+dataAEAD.NonceSize()]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		clear(result)
		return nil, fmt.Errorf("generate data encryption nonce: %w", err)
	}
	return dataAEAD.Seal(result, nonce, plaintext, result[:prefixLength]), nil
}
