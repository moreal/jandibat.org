package config

import (
	"errors"
	"testing"
	"time"
)

func lookup(values map[string]string) Lookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	got, err := Load(lookup(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.APIAddress != ":8080" || got.SchedulerInterval != 15*time.Minute || got.ShutdownTimeout != 40*time.Second {
		t.Fatalf("unexpected defaults: %#v", got)
	}
	if got.PublicURL.String() != "http://localhost:8080" {
		t.Fatalf("unexpected public URL: %s", got.PublicURL)
	}
}

func TestLoadHonorsExplicitShutdownTimeout(t *testing.T) {
	got, err := Load(lookup(map[string]string{"SHUTDOWN_TIMEOUT": "45s"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.ShutdownTimeout != 45*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 45s", got.ShutdownTimeout)
	}
}

func TestLoadProductionRequiresSecrets(t *testing.T) {
	_, err := Load(lookup(map[string]string{"APP_ENV": EnvironmentProduction}))
	if !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("expected ErrMissingSecret, got %v", err)
	}
}

func TestLoadProductionAcceptsValidSecrets(t *testing.T) {
	values := map[string]string{
		"APP_ENV":                             EnvironmentProduction,
		"SESSION_SIGNING_KEY":                 "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
		"DELETION_PSEUDONYM_KEY":              "LS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0",
		"CREDENTIAL_ACTIVE_KEY_ID":            "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":   `{"current":"cHVibGljLWtleQ"}`,
		"DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID": "identity-current",
		"DELETED_IDENTITY_HMAC_KEYS":          `{"identity-current":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU"}`,
		"SCHEDULER_INTERVAL":                  "30m",
		"TRUST_PROXY_HEADERS":                 "true",
	}
	got, err := Load(lookup(values))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.SessionSigningKey) != 32 || len(got.DeletionPseudonymKey) != 32 || len(got.CredentialEncryptionPublicKeys["current"]) == 0 || len(got.CredentialEncryptionPrivateKeys) != 0 || len(got.DeletedIdentityHMACKeys["identity-current"]) != 32 {
		t.Fatalf("unexpected process key material: session=%d public=%d private=%d", len(got.SessionSigningKey), len(got.CredentialEncryptionPublicKeys["current"]), len(got.CredentialEncryptionPrivateKeys))
	}
	if !got.TrustProxyHeaders || got.SchedulerInterval != 30*time.Minute {
		t.Fatalf("unexpected parsed settings: %#v", got)
	}
}

func TestLoadDeletionPseudonymKeyUsesUnpaddedBase64URLAndMinimumLength(t *testing.T) {
	for name, value := range map[string]string{
		"standard alphabet": "//////////////////////////////////////////8",
		"padded":            "LS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0=",
		"too short":         "c2hvcnQ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(lookup(map[string]string{"DELETION_PSEUDONYM_KEY": value})); err == nil {
				t.Fatalf("Load accepted %s key", name)
			}
		})
	}
	got, err := Load(lookup(map[string]string{"DELETION_PSEUDONYM_KEY": "X19fX19fX19fX19fX19fX19fX19fX19fX19fX19fX18"}))
	if err != nil || len(got.DeletionPseudonymKey) != 32 {
		t.Fatalf("valid URL-safe key = %d bytes, %v", len(got.DeletionPseudonymKey), err)
	}
}

func TestLoadRejectsInvalidURLAndDuration(t *testing.T) {
	_, err := Load(lookup(map[string]string{"PUBLIC_BASE_URL": "localhost"}))
	if err == nil {
		t.Fatal("expected invalid URL error")
	}

	_, err = Load(lookup(map[string]string{"SCHEDULER_INTERVAL": "0s"}))
	if err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestLoadRejectsInvalidCredentialKeyring(t *testing.T) {
	tests := []string{
		`{"current":"not-base64"}`,
		`{" current":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"}`,
		`{"current":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY","current":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"}`,
		`{"current":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"} trailing`,
	}
	for _, value := range tests {
		if _, err := Load(lookup(map[string]string{"CREDENTIAL_ENCRYPTION_KEYS": value})); err == nil {
			t.Fatalf("Load accepted invalid keyring %q", value)
		}
	}
}

func TestLoadRejectsNon256BitLegacyCredentialKey(t *testing.T) {
	if _, err := Load(lookup(map[string]string{"CREDENTIAL_ENCRYPTION_KEY": "YWFhYWFhYWFhYWFhYWFhYQ"})); err == nil {
		t.Fatal("16-byte legacy AES key was accepted")
	}
}

func TestLoadMaintenanceDefaultsAndValidation(t *testing.T) {
	got, err := Load(lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionInterval != 24*time.Hour || got.ReencryptionInterval != time.Hour || got.MaintenanceTimeout != 15*time.Minute || got.MaintenanceBatchSize != 500 || got.RetentionMaxBatches != 30 {
		t.Fatalf("maintenance defaults = %#v", got)
	}
	if _, err := Load(lookup(map[string]string{"MAINTENANCE_BATCH_SIZE": "0"})); err == nil {
		t.Fatal("zero maintenance batch size was accepted")
	}
}

func TestLoadProcessDatabaseAndHealthSettings(t *testing.T) {
	got, err := LoadForProcess(lookup(map[string]string{
		"WORKER_DATABASE_URL":      "postgresql://worker@db/jandibat",
		"MAINTENANCE_DATABASE_URL": "postgresql://maintenance@db/jandibat",
		"WORKER_HEALTH_ADDR":       "127.0.0.1:9081",
		"MAINTENANCE_HEALTH_ADDR":  "127.0.0.1:9082",
		"DEVELOPMENT_ALL_IN_ONE":   "true",
	}), ProcessWorker)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkerDatabaseURL != "postgresql://worker@db/jandibat" || got.MaintenanceDatabaseURL != "postgresql://maintenance@db/jandibat" {
		t.Fatalf("role database URLs = %q, %q", got.WorkerDatabaseURL, got.MaintenanceDatabaseURL)
	}
	if got.WorkerHealthAddress != "127.0.0.1:9081" || got.MaintenanceHealthAddress != "127.0.0.1:9082" || !got.DevelopmentAllInOne {
		t.Fatalf("process settings = %#v", got)
	}
}

func TestLoadWorkerAndMaintenanceDoNotRequireAPISessionSecret(t *testing.T) {
	values := map[string]string{
		"APP_ENV":                            EnvironmentProduction,
		"DELETION_PSEUDONYM_KEY":             "LS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0",
		"CREDENTIAL_ACTIVE_KEY_ID":           "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":  `{"current":"cHVibGljLWtleQ"}`,
		"CREDENTIAL_ENCRYPTION_PRIVATE_KEYS": `{"current":"cHJpdmF0ZS1rZXk"}`,
		"SMTP_ADDR":                          "smtp.example.com:587", "SMTP_USERNAME": "mailer", "SMTP_PASSWORD": "secret", "SMTP_FROM": "no-reply@example.com",
	}
	got, err := LoadForProcess(lookup(values), ProcessWorker)
	if err != nil {
		t.Fatalf("LoadForProcess(worker): %v", err)
	}
	if len(got.SessionSigningKey) != 0 {
		t.Fatal("worker unexpectedly loaded an API session key")
	}
	values["DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID"] = "identity-current"
	values["DELETED_IDENTITY_HMAC_KEYS"] = `{"identity-current":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU"}`
	delete(values, "SMTP_ADDR")
	delete(values, "SMTP_USERNAME")
	delete(values, "SMTP_PASSWORD")
	delete(values, "SMTP_FROM")
	got, err = LoadForProcess(lookup(values), ProcessMaintenance)
	if err != nil {
		t.Fatalf("LoadForProcess(maintenance): %v", err)
	}
	if len(got.SessionSigningKey) != 0 {
		t.Fatal("maintenance unexpectedly loaded an API session key")
	}
	delete(values, "DELETION_PSEUDONYM_KEY")
	delete(values, "DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID")
	delete(values, "DELETED_IDENTITY_HMAC_KEYS")
	values["SMTP_ADDR"] = "smtp.example.com:587"
	values["SMTP_USERNAME"] = "mailer"
	values["SMTP_PASSWORD"] = "secret"
	values["SMTP_FROM"] = "no-reply@example.com"
	if _, err := LoadForProcess(lookup(values), ProcessWorker); err != nil {
		t.Fatalf("worker unexpectedly requires deletion pseudonym key: %v", err)
	}
	values["SESSION_SIGNING_KEY"] = "LS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0"
	delete(values, "CREDENTIAL_ENCRYPTION_PRIVATE_KEYS")
	delete(values, "SMTP_ADDR")
	delete(values, "SMTP_USERNAME")
	delete(values, "SMTP_PASSWORD")
	delete(values, "SMTP_FROM")
	values["DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID"] = "identity-current"
	values["DELETED_IDENTITY_HMAC_KEYS"] = `{"identity-current":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU"}`
	if _, err := LoadForProcess(lookup(values), ProcessAPI); err != nil {
		t.Fatalf("enqueue-only API unexpectedly requires deletion pseudonym key: %v", err)
	}
	delete(values, "SESSION_SIGNING_KEY")
	values["CREDENTIAL_ENCRYPTION_PRIVATE_KEYS"] = `{"current":"cHJpdmF0ZS1rZXk"}`
	if _, err := LoadForProcess(lookup(values), ProcessMaintenance); !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("maintenance load without deletion pseudonym key error = %v", err)
	}
	values["DELETION_PSEUDONYM_KEY"] = "LS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0"
	delete(values, "CREDENTIAL_ENCRYPTION_PRIVATE_KEYS")
	if _, err := LoadForProcess(lookup(values), ProcessAPI); !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("API load without session secret error = %v", err)
	}
}

func TestLoadProductionAPIRejectsDecryptKeyMaterial(t *testing.T) {
	base := map[string]string{
		"APP_ENV":                             EnvironmentProduction,
		"SESSION_SIGNING_KEY":                 "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
		"DELETION_PSEUDONYM_KEY":              "LS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0tLS0",
		"CREDENTIAL_ACTIVE_KEY_ID":            "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":   `{"current":"cHVibGljLWtleQ"}`,
		"DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID": "identity-current",
		"DELETED_IDENTITY_HMAC_KEYS":          `{"identity-current":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU"}`,
	}
	for key, value := range map[string]string{
		"CREDENTIAL_ENCRYPTION_PRIVATE_KEYS": `{"current":"cHJpdmF0ZS1rZXk"}`,
		"CREDENTIAL_ENCRYPTION_KEYS":         `{"legacy":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"}`,
		"CREDENTIAL_ENCRYPTION_KEY":          "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
	} {
		values := make(map[string]string, len(base)+1)
		for name, setting := range base {
			values[name] = setting
		}
		values[key] = value
		if _, err := LoadForProcess(lookup(values), ProcessAPI); !errors.Is(err, ErrForbiddenSecret) {
			t.Fatalf("API with %s error = %v", key, err)
		}
	}
}

func TestLoadDeletedIdentityHMACKeyringIsProcessScoped(t *testing.T) {
	valid := map[string]string{
		"APP_ENV":                             EnvironmentProduction,
		"SESSION_SIGNING_KEY":                 "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
		"CREDENTIAL_ACTIVE_KEY_ID":            "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":   `{"current":"cHVibGljLWtleQ"}`,
		"DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID": "identity-current",
		"DELETED_IDENTITY_HMAC_KEYS":          `{"identity-current":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU"}`,
	}
	if got, err := LoadForProcess(lookup(valid), ProcessAPI); err != nil || len(got.DeletedIdentityHMACKeys["identity-current"]) != 32 {
		t.Fatalf("valid API identity keyring = %#v, %v", got.DeletedIdentityHMACKeys, err)
	}
	invalidLength := make(map[string]string, len(valid))
	for key, value := range valid {
		invalidLength[key] = value
	}
	invalidLength["DELETED_IDENTITY_HMAC_KEYS"] = `{"identity-current":"c2hvcnQ"}`
	if _, err := LoadForProcess(lookup(invalidLength), ProcessAPI); err == nil {
		t.Fatal("short identity HMAC key was accepted")
	}
	worker := map[string]string{
		"APP_ENV":                             EnvironmentProduction,
		"CREDENTIAL_ACTIVE_KEY_ID":            "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":   `{"current":"cHVibGljLWtleQ"}`,
		"CREDENTIAL_ENCRYPTION_PRIVATE_KEYS":  `{"current":"cHJpdmF0ZS1rZXk"}`,
		"DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID": "identity-current",
		"DELETED_IDENTITY_HMAC_KEYS":          valid["DELETED_IDENTITY_HMAC_KEYS"],
		"SMTP_ADDR":                           "smtp.example.com:587", "SMTP_USERNAME": "mailer", "SMTP_PASSWORD": "secret", "SMTP_FROM": "no-reply@example.com",
	}
	if _, err := LoadForProcess(lookup(worker), ProcessWorker); !errors.Is(err, ErrForbiddenSecret) {
		t.Fatalf("worker identity HMAC key error = %v", err)
	}
}

func TestLoadProductionScopesSMTPToWorker(t *testing.T) {
	api := map[string]string{
		"APP_ENV":                             EnvironmentProduction,
		"SESSION_SIGNING_KEY":                 "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
		"CREDENTIAL_ACTIVE_KEY_ID":            "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":   `{"current":"cHVibGljLWtleQ"}`,
		"DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID": "identity-current",
		"DELETED_IDENTITY_HMAC_KEYS":          `{"identity-current":"QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU"}`,
	}
	if _, err := LoadForProcess(lookup(api), ProcessAPI); err != nil {
		t.Fatalf("API without SMTP: %v", err)
	}
	api["SMTP_ADDR"] = "smtp.example.com:587"
	if _, err := LoadForProcess(lookup(api), ProcessAPI); !errors.Is(err, ErrForbiddenSecret) {
		t.Fatalf("API with SMTP error = %v", err)
	}
	worker := map[string]string{
		"APP_ENV":                            EnvironmentProduction,
		"CREDENTIAL_ACTIVE_KEY_ID":           "current",
		"CREDENTIAL_ENCRYPTION_PUBLIC_KEYS":  `{"current":"cHVibGljLWtleQ"}`,
		"CREDENTIAL_ENCRYPTION_PRIVATE_KEYS": `{"current":"cHJpdmF0ZS1rZXk"}`,
	}
	if _, err := LoadForProcess(lookup(worker), ProcessWorker); !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("worker without SMTP error = %v", err)
	}
	worker["SMTP_ADDR"] = "smtp.example.com:587"
	worker["SMTP_USERNAME"] = "mailer"
	worker["SMTP_PASSWORD"] = "secret"
	worker["SMTP_FROM"] = "no-reply@example.com"
	if _, err := LoadForProcess(lookup(worker), ProcessWorker); err != nil {
		t.Fatalf("worker with complete SMTP: %v", err)
	}
}

func TestLoadRejectsProductionAllInOneAndUnknownProcess(t *testing.T) {
	if _, err := LoadForProcess(lookup(map[string]string{
		"APP_ENV":                EnvironmentProduction,
		"DEVELOPMENT_ALL_IN_ONE": "true",
	}), ProcessWorker); err == nil {
		t.Fatal("production all-in-one mode was accepted")
	}
	if _, err := LoadForProcess(lookup(nil), "surprise"); err == nil {
		t.Fatal("unknown process was accepted")
	}
}
