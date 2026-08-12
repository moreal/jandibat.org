package processruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/config"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func asymmetricSettings(t *testing.T) (config.Config, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return config.Config{
		Environment: config.EnvironmentProduction, CredentialActiveKeyID: "rsa-current",
		CredentialEncryptionPublicKeys:  map[string][]byte{"rsa-current": publicDER},
		CredentialEncryptionPrivateKeys: map[string][]byte{"rsa-current": privateDER},
	}, privateKey
}

func TestBuildCredentialEncryptorDropsPrivateAndLegacyMaterial(t *testing.T) {
	settings, _ := asymmetricSettings(t)
	settings.CredentialCipherKey = bytes.Repeat([]byte{'l'}, 32)
	settings.CredentialEncryptionKeys = map[string][]byte{"legacy": bytes.Repeat([]byte{'k'}, 32)}
	writer, err := BuildCredentialEncryptor(settings)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := writer.Encrypt(context.Background(), []byte("api-write"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Decrypt(context.Background(), ciphertext); !errors.Is(err, operations.ErrDecryptNotAvailable) {
		t.Fatalf("API writer decrypt error = %v", err)
	}
	legacyAES, _ := integrations.NewAESGCMCipher(settings.CredentialCipherKey)
	legacyCiphertext, _ := legacyAES.Encrypt(context.Background(), []byte("legacy"))
	if _, err := writer.Decrypt(context.Background(), legacyCiphertext); !errors.Is(err, operations.ErrDecryptNotAvailable) {
		t.Fatalf("API writer legacy decrypt error = %v", err)
	}
}

func TestBuildCredentialKeyringReadsV3AndLegacyForWorkerMaintenance(t *testing.T) {
	settings, _ := asymmetricSettings(t)
	settings.CredentialCipherKey = bytes.Repeat([]byte{'l'}, 32)
	reader, err := BuildCredentialKeyring(settings)
	if err != nil {
		t.Fatal(err)
	}
	v3, err := reader.Encrypt(context.Background(), []byte("v3-secret"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := reader.Decrypt(context.Background(), v3)
	if err != nil || string(plaintext) != "v3-secret" {
		t.Fatalf("v3 decrypt = %q, %v", plaintext, err)
	}
	legacyAES, _ := integrations.NewAESGCMCipher(settings.CredentialCipherKey)
	legacy, _ := legacyAES.Encrypt(context.Background(), []byte("legacy-secret"))
	plaintext, err = reader.Decrypt(context.Background(), legacy)
	if err != nil || string(plaintext) != "legacy-secret" {
		t.Fatalf("legacy decrypt = %q, %v", plaintext, err)
	}
}

func TestBuildCredentialKeyringRequiresMatchingPrivateKey(t *testing.T) {
	settings, _ := asymmetricSettings(t)
	delete(settings.CredentialEncryptionPrivateKeys, settings.CredentialActiveKeyID)
	if _, err := BuildCredentialKeyring(settings); !errors.Is(err, config.ErrMissingSecret) {
		t.Fatalf("missing private key error = %v", err)
	}
}
