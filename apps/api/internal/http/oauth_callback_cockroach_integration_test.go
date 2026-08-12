package apihttp_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	adapteroauth "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	oauthstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth/cockroach"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

// This exercises the real API-role state, connection, environment, and audit
// adapters around a real OAuth provider adapter. The local fixture is the
// provider boundary only; exchange, identity parsing, state consumption, token
// encryption, rollback hooks, and transaction coordination remain production.
func TestCockroachOAuthCallbackOutboxFailureConsumesStateRollsBackConnectionAndRevokesToken(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	apiDB, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = apiDB.Close() })
	var currentUser string
	if err := apiDB.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	userID := "oauth-rollback-user-" + suffix
	subjectID := "oauth-rollback-subject-" + suffix
	handle := "oauth-rollback-" + suffix
	email := userID + "@example.invalid"
	remote := "203.0.113.77:4747"
	auditKey := []byte("oauth-rollback-audit-source-key")
	source := testAuditSourceIP("203.0.113.77", auditKey)
	if _, err := admin.ExecContext(ctx, `INSERT INTO users(id,primary_email,status,created_at,updated_at) VALUES($1,$2,'active',$3,$3)`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO subjects(id,owner_user_id,handle,timezone,is_public,created_at,updated_at) VALUES($1,$2,$3,'UTC',false,$4,$4)`, subjectID, userID, handle, now); err != nil {
		t.Fatal(err)
	}

	integrationDB, err := integrationstore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	activityDB, err := activitystore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	subjectDB, err := subjectstore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	subjectService, err := subjects.NewService(subjectDB, subjects.Config{})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := integrations.NewAESGCMCipher(bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	connectionService, err := integrations.NewConnectionService(integrationDB, cipher, nil, nil, activityDB)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := connectionService.BeginOAuth(ctx, integrations.ConnectInput{
		SubjectID: subjectID, ProviderID: "gitlab", EnvironmentID: "gitlab",
		ExternalAccountLogin: handle, Scopes: []string{"read_user"}, IncludePrivate: true,
	})
	if err != nil {
		t.Fatalf("create pending OAuth connection: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = admin.ExecContext(cleanup, `DELETE FROM audit_events WHERE source_ip=$1`, source)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM mutation_audit_outbox WHERE actor_id=$1 OR target_id=$2`, userID, pending.ID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM auth_challenges WHERE payload->>'ConnectionID'=$1`, pending.ID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM provider_connections WHERE id=$1`, pending.ID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM environments WHERE id=$1`, pending.EnvironmentID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM subjects WHERE id=$1`, subjectID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM users WHERE id=$1`, userID)
	})

	stateStore, err := oauthstore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	var tokenExchanges, identityLookups, revocations atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			tokenExchanges.Add(1)
			if err := request.ParseForm(); err != nil || request.PostForm.Get("code") != "authorization-code" {
				t.Errorf("token form=%v err=%v", request.PostForm, err)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(response, `{"access_token":"fresh-private-token","refresh_token":"fresh-refresh-token","expires_in":3600,"scope":"read_user"}`)
		case "/identity":
			identityLookups.Add(1)
			if authorization := request.Header.Get("Authorization"); authorization != "Bearer fresh-private-token" {
				t.Errorf("identity Authorization=%q", authorization)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(response, `{"id":7123,"username":"oauth-user","name":"OAuth User","avatar_url":"https://images.example.invalid/avatar"}`)
		case "/revoke":
			revocations.Add(1)
			body, readErr := url.ParseQuery(readRequestBody(t, request))
			if readErr != nil || body.Get("token") != "fresh-private-token" {
				t.Errorf("revoke form=%v err=%v", body, readErr)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(response, `{}`)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(provider.Close)
	callbackURL := "https://api.example.test/v1/integrations/gitlab/callback"
	flow, err := adapteroauth.NewGitLab(adapteroauth.Config{
		ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: callbackURL,
		Scopes: []string{"read_user"}, StateStore: stateStore,
		Endpoints: adapteroauth.Endpoints{
			AuthorizationURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token",
			IdentityURL: provider.URL + "/identity", RevocationURL: provider.URL + "/revoke",
		},
		HTTPClient: provider.Client(), Random: bytes.NewReader(bytes.Repeat([]byte{0x39}, 64)),
		RequireSessionBinding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionBinding := "oauth-session-binding-" + suffix
	started, err := flow.Begin(ctx, adapteroauth.AuthorizationRequest{
		ConnectionID: pending.ID, SubjectID: subjectID, Scopes: []string{"read_user"}, SessionBinding: sessionBinding,
	})
	if err != nil {
		t.Fatalf("begin OAuth state: %v", err)
	}

	operationStore, err := operationsstore.New(apiDB)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := operations.NewAuditRecorder(operationStore)
	if err != nil {
		t.Fatal(err)
	}
	user := auth.User{ID: userID, PrimaryEmail: email, Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fixedDeletionUserAuth{user: user}, SubjectAuthorizer: subjectService,
		OAuthConnections: connectionService, OAuthFlows: map[string]adapteroauth.Flow{"gitlab": flow},
		OAuthWebURL: "https://app.example.test", AllowedRedirects: []string{"https://app.example.test/settings/providers"},
		Audit: recorder, AuditSourceKey: auditKey, MutationAudits: failingEnqueueCoordinator{next: operationStore},
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/integrations/gitlab/callback?state="+url.QueryEscape(started.State)+"&code=authorization-code", nil)
	request.RemoteAddr = remote
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: sessionBinding})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Location") != "" {
		t.Fatalf("callback status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if tokenExchanges.Load() != 1 || identityLookups.Load() != 1 || revocations.Load() != 1 {
		t.Fatalf("provider calls exchange=%d identity=%d revoke=%d", tokenExchanges.Load(), identityLookups.Load(), revocations.Load())
	}
	if _, err := flow.Complete(ctx, adapteroauth.CallbackRequest{State: started.State, Code: "replay", SessionBinding: sessionBinding}); !errors.Is(err, adapteroauth.ErrInvalidState) {
		t.Fatalf("OAuth state replay error=%v", err)
	}
	var status, externalAccount string
	var accessCiphertext, refreshCiphertext []byte
	var accessKeyID, refreshKeyID sql.NullString
	var expiresAt sql.NullTime
	if err := admin.QueryRowContext(ctx, `
SELECT COALESCE(sync_cursor->>'connection_status', status), COALESCE(external_account_id, ''),
       access_token_ciphertext, refresh_token_ciphertext, access_token_key_id, refresh_token_key_id, token_expires_at
FROM provider_connections WHERE id=$1`, pending.ID).Scan(
		&status, &externalAccount, &accessCiphertext, &refreshCiphertext, &accessKeyID, &refreshKeyID, &expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || externalAccount != "" || len(accessCiphertext) != 0 || len(refreshCiphertext) != 0 || accessKeyID.Valid || refreshKeyID.Valid || expiresAt.Valid {
		t.Fatalf("rolled-back connection status=%q external=%q access=%d refresh=%d accessKey=%v refreshKey=%v expiry=%v", status, externalAccount, len(accessCiphertext), len(refreshCiphertext), accessKeyID, refreshKeyID, expiresAt)
	}
	var consumedStates, callbackOutboxes int
	digest := sha256.Sum256([]byte(started.State))
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM auth_challenges WHERE challenge_hash=$1 AND kind='oauth_state' AND consumed_at IS NOT NULL`, digest[:]).Scan(&consumedStates); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='get.v1.integrations.provider.callback' AND target_id=$1`, pending.ID).Scan(&callbackOutboxes); err != nil {
		t.Fatal(err)
	}
	if consumedStates != 1 || callbackOutboxes != 0 {
		t.Fatalf("consumed states=%d callback outboxes=%d", consumedStates, callbackOutboxes)
	}
}

func readRequestBody(t *testing.T, request *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func testAuditSourceIP(ip string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(ip))
	return "ip_" + hex.EncodeToString(mac.Sum(nil)[:16])
}
