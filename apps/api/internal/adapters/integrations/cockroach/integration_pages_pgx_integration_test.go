package cockroach_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestCockroachIntegrationPagesUseScopedKeysetAndPublicProjection(t *testing.T) {
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated isolated CockroachDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	suffix := integrationSuffix(t)
	userID := "it_page_user_" + suffix
	subjectID := "it_page_subject_" + suffix
	otherSubjectID := "it_page_other_" + suffix
	envIDs := []string{"it_page_env_a_" + suffix, "it_page_env_b_" + suffix, "it_page_env_c_" + suffix, "it_page_env_other_" + suffix}
	ids := []string{"10000000-0000-4000-8000-" + suffix, "20000000-0000-4000-8000-" + suffix, "30000000-0000-4000-8000-" + suffix}
	providerIDs := []string{"40000000-0000-4000-8000-" + suffix, "50000000-0000-4000-8000-" + suffix, "60000000-0000-4000-8000-" + suffix}
	otherID := "70000000-0000-4000-8000-" + suffix
	otherProviderID := "80000000-0000-4000-8000-" + suffix
	base := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	if _, err := admin.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		for _, id := range append(providerIDs, otherProviderID) {
			_, _ = admin.ExecContext(cleanup, `DELETE FROM custom_providers WHERE id=$1`, id)
		}
		for _, id := range append(ids, otherID) {
			_, _ = admin.ExecContext(cleanup, `DELETE FROM provider_connections WHERE id=$1`, id)
		}
		for _, id := range envIDs {
			_, _ = admin.ExecContext(cleanup, `DELETE FROM environments WHERE id=$1`, id)
		}
		_, _ = admin.ExecContext(cleanup, `DELETE FROM subjects WHERE id IN ($1,$2)`, subjectID, otherSubjectID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM users WHERE id=$1`, userID)
	})
	for _, id := range []string{subjectID, otherSubjectID} {
		if _, err := admin.ExecContext(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone, is_public) VALUES ($1,$2,$3,'UTC',true)`, id, userID, id); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range envIDs {
		owner := subjectID
		if i == 3 {
			owner = otherSubjectID
		}
		if _, err := admin.ExecContext(ctx, `INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1,$1,'Page fixture','subject',$2)`, id, owner); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		status := "active"
		if i == 2 {
			status = "revoked"
		}
		if _, err := admin.ExecContext(ctx, `INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, external_account_id, status, access_token_ciphertext, created_at, updated_at) VALUES ($1,$2,$3,'token',$4,$5,$6,$7,$7)`, id, subjectID, envIDs[0], id, status, []byte("fixture-ciphertext"), base); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO provider_connections (id, subject_id, environment_id, auth_method, external_account_id, status, created_at, updated_at) VALUES ($1,$2,$3,'token','other','active',$4,$4)`, otherID, otherSubjectID, envIDs[0], base); err != nil {
		t.Fatal(err)
	}
	for i, id := range providerIDs {
		if _, err := admin.ExecContext(ctx, `INSERT INTO custom_providers (id, owner_user_id, subject_id, environment_id, slug, name, status, configuration, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,'Page provider','active','{}'::JSONB,$6,$6)`, id, userID, subjectID, envIDs[i], []string{"zeta", "alpha", "middle"}[i]+suffix, base); err != nil {
			t.Fatal(err)
		}
		fakeDigest := sha256.Sum256([]byte(id))
		if _, err := admin.ExecContext(ctx, `INSERT INTO custom_provider_secrets (provider_id, ingest_token_hash) VALUES ($1,$2)`, id, fakeDigest[:]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO custom_providers (id, owner_user_id, subject_id, environment_id, slug, name, status, configuration, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,'Other page provider','active','{}'::JSONB,$6,$6)`, otherProviderID, userID, otherSubjectID, envIDs[3], "other"+suffix, base); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.User("jandibat_api")
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := integrationstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	connections, err := store.ListConnectionsPage(ctx, subjectID, nil, 1)
	if err != nil || len(connections) != 1 || connections[0].ID != ids[0] {
		t.Fatalf("first connection page = (%+v, %v)", connections, err)
	}
	if _, err := admin.ExecContext(ctx, `DELETE FROM provider_connections WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	connections, err = store.ListConnectionsPage(ctx, subjectID, &integrations.ConnectionCursor{CreatedAt: base, ID: ids[0]}, 2)
	if err != nil || len(connections) != 1 || connections[0].ID != ids[1] {
		t.Fatalf("connection page after deleted anchor = (%+v, %v)", connections, err)
	}
	providers, err := store.ListCustomProvidersPage(ctx, subjectID, nil, 2)
	if err != nil || len(providers) != 2 || providers[0].ID != providerIDs[0] || providers[1].ID != providerIDs[1] {
		t.Fatalf("first provider page = (%+v, %v)", providers, err)
	}
	otherProviders, err := store.ListCustomProvidersPage(ctx, otherSubjectID, nil, 2)
	if err != nil || len(otherProviders) != 1 || otherProviders[0].ID != otherProviderID {
		t.Fatalf("other subject provider page = (%+v, %v)", otherProviders, err)
	}
	if _, err := admin.ExecContext(ctx, `DELETE FROM custom_provider_secrets WHERE provider_id=$1`, providerIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `DELETE FROM custom_providers WHERE id=$1`, providerIDs[1]); err != nil {
		t.Fatal(err)
	}
	providers, err = store.ListCustomProvidersPage(ctx, subjectID, &integrations.CustomProviderCursor{CreatedAt: base, ID: providerIDs[1]}, 2)
	if err != nil || len(providers) != 1 || providers[0].ID != providerIDs[2] {
		t.Fatalf("provider page after deleted anchor = (%+v, %v)", providers, err)
	}
}
