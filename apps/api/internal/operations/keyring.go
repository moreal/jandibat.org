package operations

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

const (
	keyEnvelopeVersionV1 byte = 1
	keyEnvelopeVersionV2 byte = 2
	keyEnvelopeVersionV3 byte = 3

	keyEnvelopeV1HeaderSize = 7  // magic (4), version (1), key ID length (2)
	keyEnvelopeV2HeaderSize = 11 // magic (4), version (1), key ID length (2), wrapped DEK length (4)
	dataEncryptionKeySize   = 32
)

var (
	keyEnvelopeMagic = [4]byte{'J', 'D', 'B', 'K'}

	ErrInvalidKeyring     = errors.New("operations: invalid encryption keyring")
	ErrUnknownKeyID       = errors.New("operations: unknown encryption key ID")
	ErrInvalidKeyEnvelope = errors.New("operations: invalid key envelope")
)

// CipherKey assigns a stable, non-secret identifier to a SecretCipher. Key IDs
// may be persisted and logged; key material must remain in the cipher adapter.
// In version-2 envelopes, Cipher is used as a key-encryption key (KEK) which
// wraps a randomly generated per-record data-encryption key (DEK).
type CipherKey struct {
	ID     string
	Cipher integrations.SecretCipher
}

// KeyringCipher is a SecretCipher-compatible envelope-encryption and rotation
// adapter. New values use a random per-record DEK for AES-256-GCM data
// encryption, and wrap that DEK with the active versioned KEK. Reads also
// accept version-1 envelopes and, when explicitly configured, ciphertext from
// before key envelopes were introduced.
type KeyringCipher struct {
	activeID       string
	keys           map[string]integrations.SecretCipher
	legacyReadKeys []string
}

var _ integrations.SecretCipher = (*KeyringCipher)(nil)

// RotatingSecretCipher is the credential-cipher capability required by the
// maintenance worker. Unlike SecretCipher, it can inspect and migrate older
// persisted envelopes without exposing implementation key material.
type RotatingSecretCipher interface {
	integrations.SecretCipher
	ActiveKeyID() string
	NeedsReencryption(ciphertext []byte, persistedKeyID string) (bool, error)
	Reencrypt(context.Context, []byte) ([]byte, error)
}

// KeyIdentifyingSecretCipher reports the configured key that actually read a
// ciphertext. Observability wrappers use this optional capability to prove
// that retired keys have no hidden consumers without exposing key material.
type KeyIdentifyingSecretCipher interface {
	DecryptWithKeyID(context.Context, []byte) ([]byte, string, error)
	ConfiguredKeyIDs() []string
}

var _ RotatingSecretCipher = (*KeyringCipher)(nil)

func NewKeyringCipher(activeID string, keys []CipherKey, legacyReadKeys ...string) (*KeyringCipher, error) {
	activeID = strings.TrimSpace(activeID)
	if activeID == "" {
		return nil, fmt.Errorf("%w: active key ID is required", ErrInvalidKeyring)
	}
	byID := make(map[string]integrations.SecretCipher, len(keys))
	for _, key := range keys {
		id := strings.TrimSpace(key.ID)
		if id == "" || key.Cipher == nil {
			return nil, fmt.Errorf("%w: key ID and cipher are required", ErrInvalidKeyring)
		}
		if len(id) > math.MaxUint16 {
			return nil, fmt.Errorf("%w: key ID is too long", ErrInvalidKeyring)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("%w: duplicate key ID %q", ErrInvalidKeyring, id)
		}
		byID[id] = key.Cipher
	}
	if _, ok := byID[activeID]; !ok {
		return nil, fmt.Errorf("%w: active key %q is not configured", ErrInvalidKeyring, activeID)
	}
	legacy := make([]string, 0, len(legacyReadKeys))
	seenLegacy := make(map[string]struct{}, len(legacyReadKeys))
	for _, requested := range legacyReadKeys {
		id := strings.TrimSpace(requested)
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("%w: legacy key %q is not configured", ErrInvalidKeyring, id)
		}
		if _, duplicate := seenLegacy[id]; duplicate {
			continue
		}
		seenLegacy[id] = struct{}{}
		legacy = append(legacy, id)
	}
	return &KeyringCipher{activeID: activeID, keys: byID, legacyReadKeys: legacy}, nil
}

func (keyring *KeyringCipher) ActiveKeyID() string { return keyring.activeID }

func (keyring *KeyringCipher) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dek := make([]byte, dataEncryptionKeySize)
	defer clear(dek)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("generate data encryption key: %w", err)
	}

	wrappedDEK, err := keyring.keys[keyring.activeID].Encrypt(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("wrap data encryption key with key %q: %w", keyring.activeID, err)
	}
	if err := ctx.Err(); err != nil {
		clear(wrappedDEK)
		return nil, err
	}

	dataAEAD, err := newDataAEAD(dek)
	if err != nil {
		clear(wrappedDEK)
		return nil, err
	}
	envelope, err := marshalKeyEnvelopeV2(keyring.activeID, wrappedDEK, plaintext, dataAEAD)
	clear(wrappedDEK)
	return envelope, err
}

func (keyring *KeyringCipher) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, _, err := keyring.DecryptWithKeyID(ctx, ciphertext)
	return plaintext, err
}

func (keyring *KeyringCipher) DecryptWithKeyID(ctx context.Context, ciphertext []byte) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	envelope, err := parseKeyEnvelope(ciphertext)
	if err != nil {
		return nil, "", err
	}
	if !envelope.enveloped {
		return keyring.decryptLegacy(ctx, ciphertext)
	}

	configured, ok := keyring.keys[envelope.keyID]
	if !ok {
		return nil, envelope.keyID, fmt.Errorf("%w: %q", ErrUnknownKeyID, envelope.keyID)
	}
	switch envelope.version {
	case keyEnvelopeVersionV1:
		plaintext, decryptErr := configured.Decrypt(ctx, envelope.ciphertext)
		if decryptErr != nil {
			return nil, envelope.keyID, fmt.Errorf("decrypt version-1 envelope with key %q: %w", envelope.keyID, decryptErr)
		}
		return plaintext, envelope.keyID, nil
	case keyEnvelopeVersionV2:
		plaintext, decryptErr := decryptKeyEnvelopeV2(ctx, configured, envelope)
		return plaintext, envelope.keyID, decryptErr
	default:
		// parseKeyEnvelope rejects unsupported versions before this point.
		return nil, envelope.keyID, fmt.Errorf("%w: unsupported version %d", ErrInvalidKeyEnvelope, envelope.version)
	}
}

func (keyring *KeyringCipher) decryptLegacy(ctx context.Context, ciphertext []byte) ([]byte, string, error) {
	var failures []error
	for _, legacyID := range keyring.legacyReadKeys {
		plaintext, decryptErr := keyring.keys[legacyID].Decrypt(ctx, ciphertext)
		if decryptErr == nil {
			return plaintext, legacyID, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, legacyID, err
		}
		failures = append(failures, fmt.Errorf("legacy key %q: %w", legacyID, decryptErr))
	}
	if len(failures) == 0 {
		return nil, "", fmt.Errorf("%w: legacy ciphertext is not enabled", ErrInvalidKeyEnvelope)
	}
	return nil, "", fmt.Errorf("%w: no legacy key could decrypt ciphertext: %w", ErrInvalidKeyEnvelope, errors.Join(failures...))
}

func (keyring *KeyringCipher) ConfiguredKeyIDs() []string {
	ids := make([]string, 0, len(keyring.keys))
	for id := range keyring.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CiphertextKeyID reports the envelope key ID. The boolean is false for
// pre-envelope legacy ciphertext. All supported envelope versions are
// recognized.
func CiphertextKeyID(ciphertext []byte) (string, bool, error) {
	envelope, err := parseKeyEnvelope(ciphertext)
	return envelope.keyID, envelope.enveloped, err
}

// NeedsReencryption reports whether a persisted value is not a version-2
// envelope under both the active KEK and matching persisted key ID. Parsing is
// deliberately performed here rather than inferred from the key-ID column: a
// version-1 envelope may already carry the active key ID and still need format
// migration.
func (keyring *KeyringCipher) NeedsReencryption(ciphertext []byte, persistedKeyID string) (bool, error) {
	envelope, err := parseKeyEnvelope(ciphertext)
	if err != nil {
		return false, err
	}
	return !envelope.enveloped ||
		envelope.version != keyEnvelopeVersionV2 ||
		envelope.keyID != keyring.activeID ||
		persistedKeyID != keyring.activeID, nil
}

// Reencrypt decrypts any readable version-2, version-1, or legacy value and
// emits a new version-2 envelope under the active KEK. The plaintext buffer is
// cleared before returning.
func (keyring *KeyringCipher) Reencrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := keyring.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	return keyring.Encrypt(ctx, plaintext)
}

type parsedKeyEnvelope struct {
	version    byte
	keyID      string
	ciphertext []byte
	wrappedDEK []byte
	nonce      []byte
	aad        []byte
	enveloped  bool
}

func newDataAEAD(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("create data cipher: %w", err)
	}
	dataAEAD, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create data AEAD: %w", err)
	}
	return dataAEAD, nil
}

func marshalKeyEnvelopeV2(keyID string, wrappedDEK, plaintext []byte, dataAEAD cipher.AEAD) ([]byte, error) {
	if len(keyID) == 0 || len(keyID) > math.MaxUint16 || len(wrappedDEK) == 0 || uint64(len(wrappedDEK)) > math.MaxUint32 || dataAEAD == nil {
		return nil, fmt.Errorf("%w: invalid key ID, wrapped key, or data cipher", ErrInvalidKeyEnvelope)
	}
	prefixLength := keyEnvelopeV2HeaderSize + len(keyID) + len(wrappedDEK)
	if prefixLength > int(^uint(0)>>1)-dataAEAD.NonceSize()-len(plaintext)-dataAEAD.Overhead() {
		return nil, fmt.Errorf("%w: envelope is too large", ErrInvalidKeyEnvelope)
	}
	result := make([]byte, prefixLength+dataAEAD.NonceSize(), prefixLength+dataAEAD.NonceSize()+len(plaintext)+dataAEAD.Overhead())
	copy(result[:4], keyEnvelopeMagic[:])
	result[4] = keyEnvelopeVersionV2
	// #nosec G115 -- lengths are explicitly bounded above.
	binary.BigEndian.PutUint16(result[5:7], uint16(len(keyID)))
	// #nosec G115 -- lengths are explicitly bounded above.
	binary.BigEndian.PutUint32(result[7:11], uint32(len(wrappedDEK)))
	copy(result[keyEnvelopeV2HeaderSize:], keyID)
	copy(result[keyEnvelopeV2HeaderSize+len(keyID):], wrappedDEK)

	nonceOffset := prefixLength
	nonce := result[nonceOffset : nonceOffset+dataAEAD.NonceSize()]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		clear(result)
		return nil, fmt.Errorf("generate data encryption nonce: %w", err)
	}
	// The header, KEK ID, and wrapped DEK are authenticated as associated data.
	return dataAEAD.Seal(result, nonce, plaintext, result[:prefixLength]), nil
}

func decryptKeyEnvelopeV2(ctx context.Context, kek integrations.SecretCipher, envelope parsedKeyEnvelope) ([]byte, error) {
	dek, err := kek.Decrypt(ctx, envelope.wrappedDEK)
	if err != nil {
		clear(dek)
		return nil, fmt.Errorf("%w: unwrap data encryption key with key %q: %w", ErrInvalidKeyEnvelope, envelope.keyID, err)
	}
	defer clear(dek)
	if len(dek) != dataEncryptionKeySize {
		return nil, fmt.Errorf("%w: unwrapped data encryption key has length %d", ErrInvalidKeyEnvelope, len(dek))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dataAEAD, err := newDataAEAD(dek)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKeyEnvelope, err)
	}
	if len(envelope.nonce) != dataAEAD.NonceSize() || len(envelope.ciphertext) < dataAEAD.Overhead() {
		return nil, fmt.Errorf("%w: invalid data nonce or ciphertext length", ErrInvalidKeyEnvelope)
	}
	plaintext, err := dataAEAD.Open(nil, envelope.nonce, envelope.ciphertext, envelope.aad)
	if err != nil {
		return nil, fmt.Errorf("%w: data authentication failed", ErrInvalidKeyEnvelope)
	}
	return plaintext, nil
}

// marshalKeyEnvelope emits the original version-1 format. It remains only for
// migration tests and tooling that need to construct a historical envelope;
// production writes use marshalKeyEnvelopeV2 through Encrypt.
func marshalKeyEnvelope(keyID string, ciphertext []byte) ([]byte, error) {
	if len(keyID) == 0 || len(keyID) > math.MaxUint16 || len(ciphertext) == 0 {
		return nil, fmt.Errorf("%w: invalid key ID or ciphertext length", ErrInvalidKeyEnvelope)
	}
	result := make([]byte, keyEnvelopeV1HeaderSize+len(keyID)+len(ciphertext))
	copy(result[:4], keyEnvelopeMagic[:])
	result[4] = keyEnvelopeVersionV1
	// #nosec G115 -- key ID length is explicitly bounded to uint16 above and by NewKeyringCipher.
	binary.BigEndian.PutUint16(result[5:7], uint16(len(keyID)))
	copy(result[keyEnvelopeV1HeaderSize:], keyID)
	copy(result[keyEnvelopeV1HeaderSize+len(keyID):], ciphertext)
	return result, nil
}

func parseKeyEnvelope(value []byte) (parsedKeyEnvelope, error) {
	if len(value) < len(keyEnvelopeMagic) || !bytes.Equal(value[:4], keyEnvelopeMagic[:]) {
		return parsedKeyEnvelope{ciphertext: value}, nil
	}
	if len(value) < keyEnvelopeV1HeaderSize {
		return parsedKeyEnvelope{enveloped: true}, fmt.Errorf("%w: truncated header", ErrInvalidKeyEnvelope)
	}

	version := value[4]
	keyIDLength := int(binary.BigEndian.Uint16(value[5:7]))
	switch version {
	case keyEnvelopeVersionV1:
		payloadOffset := keyEnvelopeV1HeaderSize + keyIDLength
		if keyIDLength == 0 || payloadOffset >= len(value) {
			return parsedKeyEnvelope{version: version, enveloped: true}, fmt.Errorf("%w: empty key ID or ciphertext", ErrInvalidKeyEnvelope)
		}
		return parsedKeyEnvelope{
			version:    version,
			keyID:      string(value[keyEnvelopeV1HeaderSize:payloadOffset]),
			ciphertext: value[payloadOffset:],
			enveloped:  true,
		}, nil
	case keyEnvelopeVersionV2:
		return parseKeyEnvelopeV2(value, keyIDLength)
	case keyEnvelopeVersionV3:
		return parseWrappedKeyEnvelope(value, keyIDLength, keyEnvelopeVersionV3)
	default:
		return parsedKeyEnvelope{version: version, enveloped: true}, fmt.Errorf("%w: unsupported version %d", ErrInvalidKeyEnvelope, version)
	}
}

func parseKeyEnvelopeV2(value []byte, keyIDLength int) (parsedKeyEnvelope, error) {
	return parseWrappedKeyEnvelope(value, keyIDLength, keyEnvelopeVersionV2)
}

func parseWrappedKeyEnvelope(value []byte, keyIDLength int, version byte) (parsedKeyEnvelope, error) {
	invalid := func(detail string) (parsedKeyEnvelope, error) {
		return parsedKeyEnvelope{version: version, enveloped: true}, fmt.Errorf("%w: %s", ErrInvalidKeyEnvelope, detail)
	}
	if len(value) < keyEnvelopeV2HeaderSize {
		return invalid("truncated version-2 header")
	}
	if keyIDLength == 0 || keyIDLength > len(value)-keyEnvelopeV2HeaderSize {
		return invalid("empty or truncated key ID")
	}
	identifierOffset := keyEnvelopeV2HeaderSize
	wrappedOffset := identifierOffset + keyIDLength
	wrappedLength64 := uint64(binary.BigEndian.Uint32(value[7:11]))
	remainingLength := uint64(len(value) - wrappedOffset) // #nosec G115 -- wrappedOffset is between header length and len(value), so the nonnegative difference fits uint64.
	if wrappedLength64 == 0 || wrappedLength64 > remainingLength {
		return invalid("empty or truncated wrapped data encryption key")
	}
	wrappedLength := int(wrappedLength64) // #nosec G115 -- decoded uint32 length was checked against the remaining slice length, which fits int.
	nonceOffset := wrappedOffset + wrappedLength
	// AES-GCM uses a 12-byte nonce and a 16-byte authentication tag.
	if len(value)-nonceOffset < 12+16 {
		return invalid("truncated data ciphertext")
	}
	return parsedKeyEnvelope{
		version:    version,
		keyID:      string(value[identifierOffset:wrappedOffset]),
		wrappedDEK: value[wrappedOffset:nonceOffset],
		nonce:      value[nonceOffset : nonceOffset+12],
		ciphertext: value[nonceOffset+12:],
		aad:        value[:nonceOffset],
		enveloped:  true,
	}, nil
}
