//go:build integration

package cockroach

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestMaintenanceReencryptionUsesPGXForEverySealedSecretKind(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	maintenanceDSN := os.Getenv("JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	if adminDSN == "" || maintenanceDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_MAINTENANCE_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	pool, err := pgxpool.New(ctx, maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	legacy, err := sql.Open("pgx", maintenanceDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	userID, subjectID, environmentID := "rekey-user-"+suffix, "rekey-subject-"+suffix, "rekey:environment:"+suffix
	connectionID, revocationID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, fixture := range []struct {
			name  string
			query string
			id    string
		}{
			{"revocation job", `DELETE FROM provider_token_revocation_jobs WHERE id=$1::UUID`, revocationID},
			{"connection", `DELETE FROM provider_connections WHERE id=$1::UUID`, connectionID},
			{"environment", `DELETE FROM environments WHERE id=$1`, environmentID},
			{"subject", `DELETE FROM subjects WHERE id=$1`, subjectID},
			{"user", `DELETE FROM users WHERE id=$1`, userID},
		} {
			if _, err := admin.ExecContext(cleanupCtx, fixture.query, fixture.id); err != nil {
				t.Errorf("cleanup %s: %v", fixture.name, err)
			}
		}
		var remaining int
		err := admin.QueryRowContext(cleanupCtx, `
SELECT
  (SELECT count(*) FROM provider_token_revocation_jobs WHERE id=$1::UUID)
  + (SELECT count(*) FROM provider_connections WHERE id=$2::UUID)
  + (SELECT count(*) FROM environments WHERE id=$3)
  + (SELECT count(*) FROM subjects WHERE id=$4)
  + (SELECT count(*) FROM users WHERE id=$5)`,
			revocationID, connectionID, environmentID, subjectID, userID).Scan(&remaining)
		if err != nil || remaining != 0 {
			t.Errorf("re-encryption fixture cleanup left %d rows: %v", remaining, err)
		}
	})
	for _, fixture := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, primary_email) VALUES ($1, $2)`, []any{userID, suffix + "@example.invalid"}},
		{`INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $3, 'UTC')`, []any{subjectID, userID, "rekey-" + suffix}},
		{`INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $1, $1, 'subject', $2)`, []any{environmentID, subjectID}},
		{`INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, status, access_token_ciphertext, access_token_key_id, refresh_token_ciphertext, refresh_token_key_id) VALUES ($1::UUID, $2, $3, 'oauth2', 'active', $4, 'old', $5, 'old')`, []any{connectionID, subjectID, environmentID, []byte("test-access"), []byte("test-refresh")}},
		{`INSERT INTO provider_token_revocation_jobs (id, provider_id, token_ciphertext, token_key_id) VALUES ($1::UUID, 'test-provider', $2, 'old')`, []any{revocationID, []byte("test-revocation")}},
	} {
		if _, err := admin.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			t.Fatal(err)
		}
	}
	want := map[operations.SecretLocator][]byte{
		{Kind: operations.SecretConnectionAccessToken, ID: connectionID}:  []byte("test-access"),
		{Kind: operations.SecretConnectionRefreshToken, ID: connectionID}: []byte("test-refresh"),
		{Kind: operations.SecretOAuthRevocationToken, ID: revocationID}:   []byte("test-revocation"),
	}
	remaining := len(want)
	after := operations.SecretLocator{}
	for remaining > 0 {
		rows, err := store.ListSecretsForReencryption(ctx, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatalf("re-encryption page exhausted with %d fixture secret kinds missing", remaining)
		}
		for _, row := range rows {
			old, exists := want[row.Locator]
			if !exists {
				continue
			}
			if row.KeyID != "old" || !bytes.Equal(row.Ciphertext, old) {
				t.Fatalf("secret kind %s was mapped incorrectly", row.Locator.Kind)
			}
			changed, err := store.ReplaceEncryptedSecret(ctx, row, "new", []byte("rotated-"+string(row.Locator.Kind)))
			if err != nil || !changed {
				t.Fatalf("secret kind %s CAS changed=%t err=%v", row.Locator.Kind, changed, err)
			}
			changed, err = store.ReplaceEncryptedSecret(ctx, row, "again", []byte("stale"))
			if err != nil || changed {
				t.Fatalf("secret kind %s stale CAS changed=%t err=%v", row.Locator.Kind, changed, err)
			}
			delete(want, row.Locator)
			remaining--
		}
		after = rows[len(rows)-1].Locator
	}

	if _, err := store.ReplaceEncryptedSecret(ctx, operations.EncryptedSecretRecord{
		Locator: operations.SecretLocator{Kind: "unsealed", ID: connectionID}, KeyID: "old", Ciphertext: []byte("test-access"),
	}, "new", []byte("rotated")); !errors.Is(err, operations.ErrInvalidSecretRecord) {
		t.Fatalf("unknown secret kind error = %v", err)
	}
}
