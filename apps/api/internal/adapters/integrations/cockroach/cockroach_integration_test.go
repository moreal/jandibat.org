package cockroach_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestCockroachConnectionPrivateConsentRoundTrip(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if apiDSN == "" {
		parsed, parseErr := url.Parse(dsn)
		if parseErr != nil {
			t.Fatalf("parse Cockroach integration DSN: %v", parseErr)
		}
		parsed.User = url.User("jandibat_api")
		apiDSN = parsed.String()
	}
	apiDB, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = apiDB.Close() })
	if err := apiDB.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB as jandibat_api: %v", err)
	}

	suffix := integrationSuffix(t)
	userID := "it_consent_user_" + suffix
	subjectID := "it_consent_subject_" + suffix
	environmentID := "it_consent_environment_" + suffix
	connectionID := "4d9ebd00-c2c0-43b7-a341-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $3, $3)`, userID, userID+"@example.invalid", now); err != nil {
		t.Fatalf("insert consent user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public, created_at, updated_at)
VALUES ($1, $2, $3, 'UTC', true, $4, $4)`, subjectID, userID, "it-consent-"+suffix, now); err != nil {
		t.Fatalf("insert consent subject: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO environments (id, key, name, scope, owner_subject_id, created_at, updated_at)
VALUES ($1, $1, 'Consent integration', 'subject', $2, $3, $3)`, environmentID, subjectID, now); err != nil {
		t.Fatalf("insert consent environment: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM provider_connections WHERE id = $1`, connectionID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM environments WHERE id = $1`, environmentID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, subjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	store, err := integrationstore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	record := integrations.ConnectionRecord{Connection: integrations.ProviderConnection{
		ID: connectionID, SubjectID: subjectID, ProviderID: "gitlab", EnvironmentID: environmentID,
		AuthMethod: integrations.AuthToken, Status: integrations.ConnectionActive,
		PrivateDataEnabled: true, CreatedAt: now, UpdatedAt: now,
	}}
	if err := store.SaveConnection(ctx, record); err != nil {
		t.Fatalf("save private consent: %v", err)
	}
	loaded, err := store.GetConnection(ctx, connectionID)
	if err != nil || !loaded.Connection.PrivateDataEnabled {
		t.Fatalf("loaded private consent = %#v, error=%v", loaded.Connection, err)
	}
	record.Connection.PrivateDataEnabled = false
	record.Connection.TokenExpiresAt = func() *time.Time { value := now.Add(time.Hour); return &value }()
	record.Credentials = integrations.EncryptedCredentials{AccessToken: []byte("must-not-persist"), RefreshToken: []byte("must-not-persist")}
	record.Connection.UpdatedAt = now.Add(time.Second)
	if err := store.SaveConnection(ctx, record); err != nil {
		t.Fatalf("disable private consent: %v", err)
	}
	loaded, err = store.GetConnection(ctx, connectionID)
	if err != nil || loaded.Connection.PrivateDataEnabled {
		t.Fatalf("loaded opt-out = %#v, error=%v", loaded.Connection, err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM provider_connection_private_consents WHERE connection_id = $1`, connectionID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("consent rows after opt-out = %d, error=%v", rows, err)
	}
	var credentialColumnsNull bool
	if err := db.QueryRowContext(ctx, `
SELECT access_token_ciphertext IS NULL AND access_token_key_id IS NULL
   AND refresh_token_ciphertext IS NULL AND refresh_token_key_id IS NULL
   AND token_expires_at IS NULL
FROM provider_connections WHERE id = $1`, connectionID).Scan(&credentialColumnsNull); err != nil || !credentialColumnsNull {
		t.Fatalf("public-only credential columns null = %t, error=%v", credentialColumnsNull, err)
	}
}

// TestCockroachCustomIngestProjectsFactsAndReplaysDurably is opt-in because it
// requires a migrated CockroachDB. It exercises the real SQL dialect and
// proves a replay survives construction of a second service instance.
func TestCockroachCustomIngestProjectsFactsAndReplaysDurably(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping CockroachDB: %v", err)
	}

	suffix := integrationSuffix(t)
	userID := "it_user_" + suffix
	subjectID := "it_subject_" + suffix
	handle := "it-" + suffix
	requestedEnvironmentID := "custom:it-" + suffix
	if _, err := db.ExecContext(ctx, `
INSERT INTO users (id, primary_email, email_verified_at, status, created_at, updated_at)
VALUES ($1, $2, now(), 'active', now(), now())`, userID, handle+"@example.invalid"); err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public, created_at, updated_at)
VALUES ($1, $2, $3, 'UTC', true, now(), now())`, subjectID, userID, handle); err != nil {
		t.Fatalf("insert integration subject: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM activity_facts WHERE subject_id = $1`, subjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM custom_providers WHERE subject_id = $1`, subjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM environments WHERE owner_subject_id = $1`, subjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, subjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	integrationDB, err := integrationstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	activityDB, err := activitystore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := integrations.NewAESGCMCipher(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := integrations.NewCustomProviderService(integrationDB, cipher, activityDB, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", 32) + suffix
	provider, err := service.Create(ctx, integrations.CreateCustomProviderInput{
		SubjectID: subjectID, EnvironmentID: requestedEnvironmentID, Slug: "it_" + suffix,
		Name: "Integration provider", AllowedActions: []string{"read"}, AllowedMetrics: []string{"count"}, IngestSecret: secret,
	})
	if err != nil {
		t.Fatalf("create custom provider: %v", err)
	}
	environmentID := "custom-provider:" + provider.ID
	if provider.EnvironmentID != environmentID {
		t.Fatalf("provider environment = %q, want isolated namespace %q", provider.EnvironmentID, environmentID)
	}
	observedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	input := integrations.IngestCustomActivitiesInput{
		ProviderID: provider.ID, IngestSecret: secret, IdempotencyKey: "idem-" + suffix,
		Activities: []integrations.CustomActivity{{
			ExternalID: "event-" + suffix, Date: time.Now().UTC().Format(time.DateOnly), Action: "read",
			Metric: "count", Value: 3, Metadata: map[string]string{"source": "integration"}, ObservedAt: &observedAt,
		}},
	}
	first, err := service.Ingest(ctx, input)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if first.Accepted != 1 || first.Duplicate != 0 || first.Rejected != 0 {
		t.Fatalf("first result = %#v", first)
	}

	secondService, err := integrations.NewCustomProviderService(integrationDB, cipher, activityDB, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := secondService.Ingest(ctx, input)
	if err != nil {
		t.Fatalf("durable replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replay = %#v, want %#v", replayed, first)
	}

	reservationKey := sha256.Sum256([]byte("reservation-" + suffix))
	requestHashes := [2][sha256.Size]byte{
		sha256.Sum256([]byte("payload-a-" + suffix)),
		sha256.Sum256([]byte("payload-b-" + suffix)),
	}
	type reservationResult struct {
		index   int
		created bool
		err     error
	}
	startReservations := make(chan struct{})
	reservationResults := make(chan reservationResult, 2)
	for index := range requestHashes {
		go func(index int) {
			<-startReservations
			created, err := integrationDB.CreateIngestIdempotencyKey(ctx, integrations.IngestIdempotencyRecord{
				ProviderID: provider.ID, KeyHash: reservationKey[:], RequestHash: requestHashes[index][:],
				ResponseStatus: 0, ResponseBody: []byte(`{}`), CreatedAt: time.Now().UTC(),
				ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
			})
			reservationResults <- reservationResult{index: index, created: created, err: err}
		}(index)
	}
	close(startReservations)
	winner := -1
	for range requestHashes {
		result := <-reservationResults
		if result.err != nil {
			t.Fatalf("concurrent reservation %d: %v", result.index, result.err)
		}
		if result.created {
			if winner != -1 {
				t.Fatalf("multiple reservation winners: %d and %d", winner, result.index)
			}
			winner = result.index
		}
	}
	if winner == -1 {
		t.Fatal("concurrent reservations had no winner")
	}
	reserved, found, err := integrationDB.GetIngestIdempotencyKey(ctx, provider.ID, reservationKey[:])
	if err != nil || !found || !bytes.Equal(reserved.RequestHash, requestHashes[winner][:]) || reserved.ResponseStatus != 0 {
		t.Fatalf("stored reservation = %#v, found=%v, error=%v", reserved, found, err)
	}
	if err := integrationDB.ReleaseIngestIdempotencyKey(ctx, provider.ID, reservationKey[:], requestHashes[winner][:]); err != nil {
		t.Fatalf("release concurrent reservation: %v", err)
	}

	facts, err := activityDB.LoadFacts(ctx, activity.LoadFactsInput{Subject: activity.SubjectID(subjectID)})
	if err != nil {
		t.Fatalf("load projected facts: %v", err)
	}
	if len(facts) != 1 || facts[0].EnvironmentID != activity.EnvironmentID(environmentID) || facts[0].Metric.Value != 3 || facts[0].Metadata["provider_event_id"] != "event-"+suffix {
		t.Fatalf("projected facts = %#v", facts)
	}
	for table, want := range map[string]int{"custom_activity_events": 1, "activity_facts": 1, "ingest_idempotency_keys": 1} {
		var count int
		query := "SELECT count(*) FROM " + table + " WHERE "
		if table == "custom_activity_events" || table == "ingest_idempotency_keys" {
			query += "custom_provider_id = $1::UUID"
			if err := db.QueryRowContext(ctx, query, provider.ID).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
		} else if err := db.QueryRowContext(ctx, query+"subject_id = $1", subjectID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
	if err := service.Delete(ctx, provider.ID); err != nil {
		t.Fatalf("delete custom provider: %v", err)
	}
	var remainingFacts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM activity_facts WHERE subject_id = $1`, subjectID).Scan(&remainingFacts); err != nil {
		t.Fatalf("count facts after delete: %v", err)
	}
	if remainingFacts != 0 {
		t.Fatalf("facts after provider delete = %d", remainingFacts)
	}
}

func integrationSuffix(t *testing.T) string {
	t.Helper()
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("generate integration suffix: %v", err)
	}
	return hex.EncodeToString(random[:])
}
