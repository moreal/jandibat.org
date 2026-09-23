package apihttp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	authstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth/cockroach"
	operationsstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/operations/cockroach"
	subjectstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/subjects/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/http/handlers"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type integrationMailer struct{}

func newAuditTestPool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (integrationMailer) SendMagicLink(context.Context, auth.MagicLinkMail) error { return nil }

type successfulAuthenticationVerifier struct{ next uint32 }

func (successfulAuthenticationVerifier) CreateRegistrationOptions(context.Context, auth.RegistrationOptionsInput) (auth.PasskeyVerifierOptions, error) {
	return auth.PasskeyVerifierOptions{}, nil
}
func (successfulAuthenticationVerifier) VerifyRegistration(context.Context, auth.RegistrationVerificationInput) (auth.RegistrationVerification, error) {
	return auth.RegistrationVerification{}, nil
}
func (successfulAuthenticationVerifier) CreateAuthenticationOptions(context.Context, auth.AuthenticationOptionsInput) (auth.PasskeyVerifierOptions, error) {
	return auth.PasskeyVerifierOptions{}, nil
}
func (verifier successfulAuthenticationVerifier) VerifyAuthentication(_ context.Context, input auth.AuthenticationVerificationInput) (auth.AuthenticationVerification, error) {
	return auth.AuthenticationVerification{
		SignCount: verifier.next, UserHandle: []byte(input.Credential.UserID),
		VerifierCredential: json.RawMessage(`{"version":1,"verified":true}`),
	}, nil
}

type consumingPasskeyFailureAuth struct {
	handlers.AuthService
	repository *authstore.Store
	now        time.Time
}

func TestCockroachAuditTimeoutRollsBackStateAndOutbox(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = api.Close() })
	var currentUser string
	if err := api.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	userID := "timeout-user-" + suffix
	requestTarget := "timeout-target-" + suffix
	if _, err := admin.ExecContext(ctx, `INSERT INTO users(id,primary_email,status,created_at,updated_at) VALUES($1,$2,'active',$3,$3)`, userID, userID+"@example.invalid", now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `INSERT INTO user_settings(user_id,locale,timezone,theme,updated_at) VALUES($1,'en-US','UTC','system',$2)`, userID, now); err != nil {
		t.Fatal(err)
	}
	var auditRequestID string
	t.Cleanup(func() {
		cleanup := context.Background()
		if auditRequestID != "" {
			_, _ = admin.ExecContext(cleanup, `DELETE FROM mutation_audit_outbox WHERE request_id=$1`, auditRequestID)
			_, _ = admin.ExecContext(cleanup, `DELETE FROM audit_events WHERE request_id=$1`, auditRequestID)
		}
		_, _ = admin.ExecContext(cleanup, `DELETE FROM user_settings WHERE user_id=$1`, userID)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM users WHERE id=$1`, userID)
	})
	pool := newAuditTestPool(t, ctx, apiDSN)
	operationStore, err := operationsstore.NewWithPGXPool(api, pool)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := operations.NewAuditRecorder(operationStore)
	if err != nil {
		t.Fatal(err)
	}
	settingsStore, err := subjectstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(timeoutProblems(25 * time.Millisecond))
	router.Use(auditRequests(recorder, []byte("timeout-audit-source-key"), operationStore))
	router.Patch("/v1/subjects/{subject}", func(w http.ResponseWriter, r *http.Request) {
		if execErr := settingsStore.SaveUserSettings(r.Context(), userID, subjects.UserSettings{Locale: "ja-JP", Timezone: "UTC", Theme: subjects.ThemeSystem, UpdatedAt: now.Add(time.Second)}); execErr != nil {
			t.Errorf("update settings: %v", execErr)
			return
		}
		w.Header().Set("Set-Cookie", "must-not-escape=secret")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"uncommitted":true}`))
		<-r.Context().Done()
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/v1/subjects/"+requestTarget, nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" || response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String(), "uncommitted") {
		t.Fatalf("timeout response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var locale string
	if err := admin.QueryRowContext(ctx, `SELECT locale FROM user_settings WHERE user_id=$1`, userID).Scan(&locale); err != nil || locale != "en-US" {
		t.Fatalf("timed out state escaped rollback: locale=%q err=%v", locale, err)
	}
	var outboxCount, outcomeCount int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE target_id=$1`, requestTarget).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(request_id),'') FROM audit_events WHERE action='patch.v1.subjects.subject' AND target_id=$1 AND outcome='failed'`, requestTarget).Scan(&outcomeCount, &auditRequestID); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 || outcomeCount != 1 || auditRequestID == "" {
		t.Fatalf("outbox=%d failed_outcomes=%d request_id=%q", outboxCount, outcomeCount, auditRequestID)
	}
	var intentCount int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE request_id=$1 AND action='http.mutation.intent'`, auditRequestID).Scan(&intentCount); err != nil || intentCount != 1 {
		t.Fatalf("intent_count=%d err=%v", intentCount, err)
	}
}

func (service consumingPasskeyFailureAuth) CompletePasskeyLogin(ctx context.Context, ceremonyID string, _ []byte, _ json.RawMessage, _ auth.SessionMetadata) (auth.SessionGrant, error) {
	if _, err := service.repository.ConsumeCeremony(ctx, ceremonyID, auth.CeremonyAuthentication, service.now); err != nil {
		return auth.SessionGrant{}, auth.ErrInvalidCeremony
	}
	return auth.SessionGrant{}, auth.ErrPasskeyVerification
}

// This exercises the production router boundary with the real API-role SQL
// adapters. A verifier rejection after ceremony consumption must commit the
// replay guard and denied audit outcome together before releasing the 401.
func TestNewRouterCockroachPasskeyFailureCommitsReplayGuardAndOutbox(t *testing.T) {
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = api.Close() })
	var currentUser string
	if err := api.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	ceremonyID := "f17d0f6a-04ae-4e72-b2f3-" + suffix
	pool := newAuditTestPool(t, ctx, apiDSN)
	authRepository, err := authstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := operationsstore.NewWithPGXPool(api, pool)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := operations.NewAuditRecorder(operationStore)
	if err != nil {
		t.Fatal(err)
	}
	ceremony := auth.PasskeyCeremony{
		ID: ceremonyID, Kind: auth.CeremonyAuthentication, Challenge: "router-challenge-" + suffix,
		VerifierSession: json.RawMessage(`{"session":"bound"}`), CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := authRepository.SaveCeremony(ctx, ceremony); err != nil {
		t.Fatalf("save ceremony: %v", err)
	}
	var auditRequestID string
	var replayAuditRequestID string
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, requestID := range []string{auditRequestID, replayAuditRequestID} {
			if requestID == "" {
				continue
			}
			_, _ = admin.ExecContext(cleanup, `DELETE FROM mutation_audit_outbox WHERE request_id=$1`, requestID)
			_, _ = admin.ExecContext(cleanup, `DELETE FROM audit_events WHERE request_id=$1`, requestID)
		}
		_, _ = admin.ExecContext(cleanup, `DELETE FROM auth_challenges WHERE id=$1`, ceremonyID)
	})
	router := NewRouter(Dependencies{
		Auth:  consumingPasskeyFailureAuth{repository: authRepository, now: now.Add(time.Second)},
		Audit: recorder, AuditSourceKey: []byte("router-cockroach-audit-source-key"), MutationAudits: operationStore,
	})
	body := `{"ceremonyId":"` + ceremonyID + `","credential":{"id":"Y3JlZA","rawId":"Y3JlZA","type":"public-key","response":{},"clientExtensionResults":{}}}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("first finish status=%d body=%s", response.Code, response.Body.String())
	}
	var consumed time.Time
	if err := admin.QueryRowContext(ctx, `SELECT consumed_at FROM auth_challenges WHERE id=$1`, ceremonyID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	var outcome, status string
	if err := admin.QueryRowContext(ctx, `
SELECT request_id,outcome,status FROM mutation_audit_outbox
WHERE action='post.v1.auth.passkey.sign-in.finish' AND created_at >= $1
ORDER BY created_at DESC LIMIT 1`, now).Scan(&auditRequestID, &outcome, &status); err != nil {
		t.Fatal(err)
	}
	if consumed.IsZero() || outcome != "denied" || status != "pending" {
		t.Fatalf("consumed=%v outcome=%q status=%q", consumed, outcome, status)
	}
	firstConsumed := consumed
	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", strings.NewReader(body))
	replayRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay finish status=%d body=%s", replay.Code, replay.Body.String())
	}
	if err := admin.QueryRowContext(ctx, `SELECT consumed_at FROM auth_challenges WHERE id=$1`, ceremonyID).Scan(&consumed); err != nil || !consumed.Equal(firstConsumed) {
		t.Fatalf("replay changed consumed_at: before=%v after=%v err=%v", firstConsumed, consumed, err)
	}
	var outboxCount int
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.auth.passkey.sign-in.finish' AND created_at >= $1`, now).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("passkey failure outbox count=%d err=%v", outboxCount, err)
	}
	if err := admin.QueryRowContext(ctx, `
SELECT request_id FROM audit_events
WHERE action='post.v1.auth.passkey.sign-in.finish' AND occurred_at >= $1 AND request_id <> $2
ORDER BY occurred_at DESC LIMIT 1`, now, auditRequestID).Scan(&replayAuditRequestID); err != nil {
		t.Fatalf("load replay direct audit request ID: %v", err)
	}
}

func TestNewRouterCockroachMagicLinkNewUserSuccessCommitsSessionAndOutbox(t *testing.T) {
	admin, api, ctx := openRouterIntegrationDatabases(t)
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	token := "magic-success-token-" + suffix + "-sufficient-entropy"
	digest := sha256.Sum256([]byte(token))
	email := "magic-success-" + suffix + "@example.invalid"
	linkID := "917d0f6a-04ae-4e72-b2f3-" + suffix
	auditKey := []byte("magic-success-audit-source-key")
	remote := "203.0.113.41:4242"
	source := auditSourceIP(remote, auditKey)
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = admin.ExecContext(cleanup, `DELETE FROM mutation_audit_outbox WHERE metadata->>'source_ip'=$1`, source)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM audit_events WHERE metadata->>'source_ip'=$1`, source)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM users WHERE primary_email=$1`, email)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM magic_link_tokens WHERE id=$1`, linkID)
	})
	pool := newAuditTestPool(t, ctx, apiDSN)
	repository, err := authstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	// The production worker mints the digest; admin is fixture-only here because
	// the API role intentionally cannot INSERT legacy token rows.
	if _, err := admin.ExecContext(ctx, `
INSERT INTO magic_link_tokens(id,email,token_hash,purpose,created_at,expires_at)
VALUES($1,$2,$3,'signin',$4,$5)`, linkID, email, digest[:], now, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(auth.Dependencies{Repository: repository, Mailer: integrationMailer{}}, auth.Config{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	operationStore, _ := operationsstore.NewWithPGXPool(api, pool)
	recorder, _ := operations.NewAuditRecorder(operationStore)
	router := NewRouter(Dependencies{Auth: service, Audit: recorder, AuditSourceKey: auditKey, MutationAudits: operationStore, Now: func() time.Time { return now.Add(time.Second) }})
	body := `{"token":"` + token + `"}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remote
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) == 0 {
		t.Fatalf("magic success status=%d cookies=%v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}
	var userID string
	var users, sessions, succeeded int
	if err := admin.QueryRowContext(ctx, `SELECT id FROM users WHERE primary_email=$1`, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=$1 AND status='active'`, userID).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM user_sessions WHERE user_id=$1 AND revoked_at IS NULL`, userID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.auth.magic-link.consume' AND actor_id=$1 AND outcome='succeeded'`, userID).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if users != 1 || sessions != 1 || succeeded != 1 {
		t.Fatalf("magic commit users=%d sessions=%d succeeded_outbox=%d", users, sessions, succeeded)
	}
	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(body))
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.RemoteAddr = remote
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("magic replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.auth.magic-link.consume' AND metadata->>'source_ip'=$1`, source).Scan(&succeeded); err != nil || succeeded != 1 {
		t.Fatalf("magic replay outbox count=%d err=%v", succeeded, err)
	}
}

func TestNewRouterCockroachPasskeySuccessCommitsCounterSessionAndOutbox(t *testing.T) {
	admin, api, ctx := openRouterIntegrationDatabases(t)
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")[:12]
	userID := "passkey-success-user-" + suffix
	email := userID + "@example.invalid"
	ceremonyID := "817d0f6a-04ae-4e72-b2f3-" + suffix
	passkeyID := "717d0f6a-04ae-4e72-b2f3-" + suffix
	credentialID := []byte("passkey-success-credential-" + suffix)
	auditKey := []byte("passkey-success-audit-source-key")
	remote := "203.0.113.42:4242"
	source := auditSourceIP(remote, auditKey)
	if _, err := admin.ExecContext(ctx, `INSERT INTO users(id,primary_email,status,created_at,updated_at) VALUES($1,$2,'active',$3,$3)`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = admin.ExecContext(cleanup, `DELETE FROM mutation_audit_outbox WHERE metadata->>'source_ip'=$1`, source)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM audit_events WHERE metadata->>'source_ip'=$1`, source)
		_, _ = admin.ExecContext(cleanup, `DELETE FROM users WHERE id=$1`, userID)
	})
	pool := newAuditTestPool(t, ctx, apiDSN)
	repository, err := authstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveCredential(ctx, auth.PasskeyCredential{
		ID: passkeyID, UserID: userID, CredentialID: credentialID, PublicKey: []byte("public-key"), SignCount: 1,
		VerifierCredential: json.RawMessage(`{"version":1,"signCount":1}`), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveCeremony(ctx, auth.PasskeyCeremony{
		ID: ceremonyID, Kind: auth.CeremonyAuthentication, Challenge: "challenge-" + suffix, UserID: userID,
		VerifierSession: json.RawMessage(`{"session":"bound"}`), CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(auth.Dependencies{
		Repository: repository, Mailer: integrationMailer{}, Verifier: successfulAuthenticationVerifier{next: 2},
	}, auth.Config{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	operationStore, _ := operationsstore.NewWithPGXPool(api, pool)
	recorder, _ := operations.NewAuditRecorder(operationStore)
	router := NewRouter(Dependencies{Auth: service, Audit: recorder, AuditSourceKey: auditKey, MutationAudits: operationStore, Now: func() time.Time { return now.Add(time.Second) }})
	encodedID := base64.RawURLEncoding.EncodeToString(credentialID)
	body := `{"ceremonyId":"` + ceremonyID + `","credential":{"id":"` + encodedID + `","rawId":"` + encodedID + `","type":"public-key","response":{},"clientExtensionResults":{}}}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remote
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) == 0 {
		t.Fatalf("passkey success status=%d cookies=%v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}
	var counter, sessions, succeeded int
	if err := admin.QueryRowContext(ctx, `SELECT sign_count FROM user_passkeys WHERE id=$1`, passkeyID).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM user_sessions WHERE user_id=$1 AND revoked_at IS NULL`, userID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.auth.passkey.sign-in.finish' AND actor_id=$1 AND outcome='succeeded'`, userID).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if counter != 2 || sessions != 1 || succeeded != 1 {
		t.Fatalf("passkey commit counter=%d sessions=%d succeeded_outbox=%d", counter, sessions, succeeded)
	}
	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", strings.NewReader(body))
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.RemoteAddr = remote
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("passkey replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM mutation_audit_outbox WHERE action='post.v1.auth.passkey.sign-in.finish' AND metadata->>'source_ip'=$1`, source).Scan(&succeeded); err != nil || succeeded != 1 {
		t.Fatalf("passkey replay outbox count=%d err=%v", succeeded, err)
	}
}

func openRouterIntegrationDatabases(t *testing.T) (*sql.DB, *sql.DB, context.Context) {
	t.Helper()
	adminDSN := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if adminDSN == "" || apiDSN == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL and JANDIBAT_TEST_API_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	api, err := sql.Open("pgx", apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(); _ = api.Close() })
	var currentUser string
	if err := api.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil || currentUser != "jandibat_api" {
		t.Fatalf("API DSN current_user=%q err=%v", currentUser, err)
	}
	return admin, api, ctx
}
