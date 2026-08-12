package processruntime

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"sort"
	"strings"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const (
	developmentCredentialKeyID = "development-only"
	legacySingleKeyID          = "legacy-single-key"
)

// BuildCredentialEncryptor constructs the API process's write-only credential
// capability. In production it accepts only RSA public keys and contains no
// legacy symmetric or private key material.
func BuildCredentialEncryptor(settings config.Config) (operations.RotatingSecretCipher, error) {
	if len(settings.CredentialEncryptionPublicKeys) != 0 {
		keys, err := parseAsymmetricKeys(settings, false)
		if err != nil {
			return nil, err
		}
		cipher, err := operations.NewAsymmetricEnvelopeCipher(settings.CredentialActiveKeyID, keys, nil)
		if err != nil {
			return nil, fmt.Errorf("runtime: construct encrypt-only credential cipher: %w", err)
		}
		return cipher, nil
	}
	if settings.Environment == config.EnvironmentProduction {
		return nil, fmt.Errorf("runtime: %w: asymmetric credential public keyring is required", config.ErrMissingSecret)
	}
	return buildLegacyCredentialKeyring(settings, true)
}

// BuildCredentialKeyring constructs the worker/maintenance read-write cipher.
// Version-3 private keys decrypt current rows; explicitly configured symmetric
// keys are retained only as a backward-read source for v1/v2 migration.
func BuildCredentialKeyring(settings config.Config) (operations.RotatingSecretCipher, error) {
	if len(settings.CredentialEncryptionPublicKeys) != 0 {
		legacy, err := buildLegacyCredentialKeyring(settings, false)
		if err != nil {
			return nil, err
		}
		keys, err := parseAsymmetricKeys(settings, true)
		if err != nil {
			return nil, err
		}
		var legacyReader integrations.SecretCipher
		if legacy != nil {
			legacyReader = legacy
		}
		cipher, err := operations.NewAsymmetricEnvelopeCipher(settings.CredentialActiveKeyID, keys, legacyReader)
		if err != nil {
			return nil, fmt.Errorf("runtime: construct credential keyring: %w", err)
		}
		return cipher, nil
	}
	if settings.Environment == config.EnvironmentProduction {
		return nil, fmt.Errorf("runtime: %w: asymmetric credential keyring is required", config.ErrMissingSecret)
	}
	return buildLegacyCredentialKeyring(settings, true)
}

func buildLegacyCredentialKeyring(settings config.Config, developmentFallback bool) (*operations.KeyringCipher, error) {
	if len(settings.CredentialCipherKey) != 0 && len(settings.CredentialCipherKey) != 32 {
		return nil, fmt.Errorf("runtime: %w: legacy credential key must be exactly 32 bytes", operations.ErrInvalidKeyring)
	}
	configured := make(map[string][]byte, len(settings.CredentialEncryptionKeys)+1)
	for id, material := range settings.CredentialEncryptionKeys {
		if len(material) != 32 {
			return nil, fmt.Errorf("runtime: %w: credential key %q must be exactly 32 bytes", operations.ErrInvalidKeyring, id)
		}
		configured[id] = append([]byte(nil), material...)
	}
	legacyIDs := make([]string, 0, len(configured)+1)
	if len(settings.CredentialCipherKey) != 0 {
		matchingID := ""
		for id, material := range configured {
			if bytes.Equal(material, settings.CredentialCipherKey) {
				matchingID = id
				break
			}
		}
		if matchingID == "" {
			matchingID = legacySingleKeyID
			for suffix := 2; configured[matchingID] != nil; suffix++ {
				matchingID = fmt.Sprintf("%s-%d", legacySingleKeyID, suffix)
			}
			configured[matchingID] = append([]byte(nil), settings.CredentialCipherKey...)
		}
		legacyIDs = append(legacyIDs, matchingID)
	}
	activeID := strings.TrimSpace(settings.CredentialActiveKeyID)
	if len(configured) == 0 {
		if !developmentFallback {
			return nil, nil
		}
		material := append([]byte(nil), settings.CredentialCipherKey...)
		if len(material) == 0 {
			digest := sha256.Sum256([]byte("jandibat.org development-only credential key"))
			material = digest[:]
		}
		if activeID == "" {
			activeID = developmentCredentialKeyID
		}
		configured[activeID] = material
	} else if _, activeExists := configured[activeID]; !activeExists {
		if len(configured) != 1 {
			return nil, fmt.Errorf("runtime: %w: active credential key ID is required", operations.ErrInvalidKeyring)
		}
		for id := range configured {
			activeID = id
		}
	}
	if _, ok := configured[activeID]; !ok {
		return nil, fmt.Errorf("runtime: %w: active credential key %q is absent", operations.ErrInvalidKeyring, activeID)
	}

	orderedIDs := make([]string, 0, len(configured))
	for id := range configured {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	legacyIDs = appendUnique(legacyIDs, activeID)
	keys := make([]operations.CipherKey, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		cipher, err := integrations.NewAESGCMCipher(configured[id])
		if err != nil {
			return nil, fmt.Errorf("runtime: construct credential cipher %q: %w", id, err)
		}
		keys = append(keys, operations.CipherKey{ID: id, Cipher: cipher})
		legacyIDs = appendUnique(legacyIDs, id)
	}
	keyring, err := operations.NewKeyringCipher(activeID, keys, legacyIDs...)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct credential keyring: %w", err)
	}
	return keyring, nil
}

func parseAsymmetricKeys(settings config.Config, requirePrivate bool) ([]operations.AsymmetricKey, error) {
	return parseAsymmetricMaterial(settings.CredentialEncryptionPublicKeys, settings.CredentialEncryptionPrivateKeys, settings.CredentialActiveKeyID, requirePrivate, "credential")
}

func parseAsymmetricMaterial(publicMaterials, privateMaterials map[string][]byte, activeID string, requirePrivate bool, label string) ([]operations.AsymmetricKey, error) {
	ids := make([]string, 0, len(publicMaterials))
	for id := range publicMaterials {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	keys := make([]operations.AsymmetricKey, 0, len(ids))
	for _, id := range ids {
		parsed, err := x509.ParsePKIXPublicKey(publicMaterials[id])
		if err != nil {
			return nil, fmt.Errorf("runtime: parse %s public key %q: %w", label, id, err)
		}
		publicKey, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("runtime: %s public key %q must be RSA", label, id)
		}
		key := operations.AsymmetricKey{ID: id, PublicKey: publicKey}
		if requirePrivate {
			material := privateMaterials[id]
			if len(material) == 0 {
				return nil, fmt.Errorf("runtime: %w: private %s key %q is required", config.ErrMissingSecret, label, id)
			}
			privateKey, parseErr := parseRSAPrivateKey(material)
			if parseErr != nil {
				return nil, fmt.Errorf("runtime: parse %s private key %q: %w", label, id, parseErr)
			}
			key.PrivateKey = privateKey
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func parseRSAPrivateKey(material []byte) (*rsa.PrivateKey, error) {
	if parsed, err := x509.ParsePKCS8PrivateKey(material); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is not RSA")
		}
		return key, nil
	}
	return x509.ParsePKCS1PrivateKey(material)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
