package integrations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type countingCipher struct{ encryptCalls int }

func (cipher *countingCipher) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	cipher.encryptCalls++
	return append([]byte("encrypted:"), plaintext...), nil
}

func (*countingCipher) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	return append([]byte(nil), ciphertext...), nil
}

func TestConnectionServiceEncryptsTokensBeforeStorage(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	clock := &fixedClock{now: time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)}
	cipher := mustTestCipher()
	service, err := NewConnectionService(store, cipher, clock, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := clock.Now().Add(time.Hour)

	connection, err := service.ConnectToken(context.Background(), ConnectTokenInput{
		ConnectInput: ConnectInput{
			SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab-env", IncludePrivate: true,
			ExternalAccountID: "octocat", Scopes: []string{"repo", "read:user"},
		},
		Credentials: TokenCredentials{AccessToken: "access-secret", RefreshToken: "refresh-secret", ExpiresAt: &expiresAt},
	})
	if err != nil {
		t.Fatalf("ConnectToken() error = %v", err)
	}
	if connection.Status != ConnectionActive || connection.AuthMethod != AuthToken {
		t.Fatalf("connection status/method = %s/%s", connection.Status, connection.AuthMethod)
	}
	if connection.EnvironmentID != "connection:id-1" {
		t.Fatalf("private environment = %q, want connection:id-1", connection.EnvironmentID)
	}
	if got, want := connection.Scopes, []string{"read:user", "repo"}; !equalStrings(got, want) {
		t.Fatalf("Scopes = %v, want %v", got, want)
	}
	record, err := store.GetConnection(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record.Credentials.AccessToken, []byte("access-secret")) || bytes.Contains(record.Credentials.RefreshToken, []byte("refresh-secret")) {
		t.Fatal("store received a plaintext credential")
	}
	access, err := cipher.Decrypt(context.Background(), record.Credentials.AccessToken)
	if err != nil || string(access) != "access-secret" {
		t.Fatalf("stored access token decrypted to %q, error %v", access, err)
	}
	refresh, err := cipher.Decrypt(context.Background(), record.Credentials.RefreshToken)
	if err != nil || string(refresh) != "refresh-secret" {
		t.Fatalf("stored refresh token decrypted to %q, error %v", refresh, err)
	}

	disabled := false
	connection, err = service.Update(context.Background(), UpdateConnectionInput{ID: connection.ID, Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != ConnectionDisabled {
		t.Fatalf("disabled status = %q", connection.Status)
	}
	disabledRecord, err := store.GetConnection(context.Background(), connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disabledRecord.Credentials.AccessToken, record.Credentials.AccessToken) ||
		!bytes.Equal(disabledRecord.Credentials.RefreshToken, record.Credentials.RefreshToken) {
		t.Fatal("disabling a connection erased its credentials")
	}
	enabled := true
	connection, err = service.Update(context.Background(), UpdateConnectionInput{ID: connection.ID, Enabled: &enabled})
	if err != nil || connection.Status != ConnectionActive {
		t.Fatalf("resume connection = %#v, error %v", connection, err)
	}
}

func TestConnectionServiceOAuthLifecycleAndRevocation(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	service := mustConnectionService(store, &fixedClock{now: time.Now().UTC()}, &sequentialIDs{})
	ctx := context.Background()

	connection, err := service.BeginOAuth(ctx, ConnectInput{
		SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab-env",
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != ConnectionPending || connection.AuthMethod != AuthOAuth2 {
		t.Fatalf("pending connection = %#v", connection)
	}
	connection, err = service.CompleteOAuth(ctx, connection.ID, TokenCredentials{AccessToken: "oauth-access"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != ConnectionActive {
		t.Fatalf("CompleteOAuth status = %s", connection.Status)
	}
	disabled := false
	connection, err = service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Enabled: &disabled})
	if err != nil || connection.Status != ConnectionDisabled {
		t.Fatalf("disable completed OAuth connection = %#v, error %v", connection, err)
	}
	enabled := true
	connection, err = service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Enabled: &enabled})
	if err != nil || connection.Status != ConnectionActive {
		t.Fatalf("re-enable completed OAuth connection = %#v, error %v", connection, err)
	}
	connection, err = service.Revoke(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != ConnectionRevoked {
		t.Fatalf("Revoke status = %s", connection.Status)
	}
	record, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Connection.Status != ConnectionRevoked || len(record.Credentials.AccessToken) != 0 || len(record.Credentials.RefreshToken) != 0 {
		t.Fatalf("revoked tombstone retained credentials: %#v", record)
	}
	if _, err := service.Get(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("public lookup of revoked tombstone error = %v, want ErrNotFound", err)
	}
	if _, err := service.CompleteOAuth(ctx, connection.ID, TokenCredentials{AccessToken: "again"}); !errors.Is(err, ErrInvalidConnectionStatus) {
		t.Fatalf("CompleteOAuth(revoked) error = %v", err)
	}
}

func TestConnectionServiceCannotEnablePendingOAuthWithoutCallback(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	service := mustConnectionService(store, &fixedClock{now: time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)}, &sequentialIDs{})
	ctx := context.Background()
	connection, err := service.BeginOAuth(ctx, ConnectInput{
		SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab-env", IncludePrivate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Enabled: &enabled}); !errors.Is(err, ErrInvalidConnectionStatus) {
		t.Fatalf("Update(pending OAuth, enabled) error = %v, want ErrInvalidConnectionStatus", err)
	}
	disabled := false
	if _, err := service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Enabled: &disabled}); !errors.Is(err, ErrInvalidConnectionStatus) {
		t.Fatalf("Update(pending OAuth, disabled) error = %v, want ErrInvalidConnectionStatus", err)
	}
	record, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Connection.Status != ConnectionPending || len(record.Credentials.AccessToken) != 0 {
		t.Fatalf("Update(pending OAuth) changed status or credentials: %#v", record)
	}
}

func TestConnectionServiceRequiresExplicitSupportedPrivateConsent(t *testing.T) {
	t.Parallel()
	service := mustConnectionService(NewMemoryStore(), &fixedClock{now: time.Now().UTC()}, &sequentialIDs{})
	ctx := context.Background()

	publicOnly, err := service.ConnectToken(ctx, ConnectTokenInput{
		ConnectInput: ConnectInput{SubjectID: "subject-public", ProviderID: "gitlab", EnvironmentID: "gitlab", IncludePrivate: false},
		Credentials:  TokenCredentials{AccessToken: "token"},
	})
	if err != nil || publicOnly.PrivateDataEnabled || publicOnly.TokenExpiresAt != nil {
		t.Fatalf("GitLab public-only connection = %#v, error=%v", publicOnly, err)
	}
	publicOnlyRecord, err := service.store.GetConnection(ctx, publicOnly.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicOnlyRecord.Credentials.AccessToken) != 0 || len(publicOnlyRecord.Credentials.RefreshToken) != 0 {
		t.Fatalf("public-only connection retained credentials: %#v", publicOnlyRecord.Credentials)
	}
	private, err := service.BeginOAuth(ctx, ConnectInput{
		SubjectID: "subject-private", ProviderID: "gitlab", EnvironmentID: "gitlab", IncludePrivate: true,
	})
	if err != nil || !private.PrivateDataEnabled {
		t.Fatalf("GitLab private connection = %#v, error=%v", private, err)
	}
	if _, err := service.ConnectToken(ctx, ConnectTokenInput{
		ConnectInput: ConnectInput{SubjectID: "subject-github", ProviderID: "github", EnvironmentID: "github", IncludePrivate: true},
		Credentials:  TokenCredentials{AccessToken: "token"},
	}); !errors.Is(err, ErrPrivateDataUnsupported) {
		t.Fatalf("unsupported credential private consent error = %v", err)
	}
	if _, err := service.ConnectPublic(ctx, ConnectInput{
		SubjectID: "subject-none", ProviderID: "gitlab", EnvironmentID: "gitlab", IncludePrivate: true,
	}); !errors.Is(err, ErrPrivateDataUnsupported) {
		t.Fatalf("anonymous private consent error = %v", err)
	}
}

func TestConnectionServiceRejectsTokenUpdateForPublicOnlyConnectionBeforeEncryption(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	cipher := &countingCipher{}
	service, err := NewConnectionService(store, cipher, &fixedClock{now: time.Now().UTC()}, &sequentialIDs{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connection, err := service.ConnectToken(ctx, ConnectTokenInput{
		ConnectInput: ConnectInput{SubjectID: "subject-public", ProviderID: "gitlab", EnvironmentID: "gitlab", IncludePrivate: false},
		Credentials:  TokenCredentials{AccessToken: "initial-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cipher.encryptCalls != 0 {
		t.Fatalf("public-only create encryption calls = %d, want 0", cipher.encryptCalls)
	}
	update := "replacement-secret"
	if _, err := service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Token: &update}); !errors.Is(err, ErrPrivateDataUnsupported) {
		t.Fatalf("Update(public-only token) error = %v, want ErrPrivateDataUnsupported", err)
	}
	if cipher.encryptCalls != 0 {
		t.Fatalf("public-only update encryption calls = %d, want 0", cipher.encryptCalls)
	}
	record, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Credentials.AccessToken) != 0 || len(record.Credentials.RefreshToken) != 0 {
		t.Fatalf("public-only update retained ciphertext: %#v", record.Credentials)
	}
}

func TestConnectionServiceSeparatesPublicAndCredentialBackedEnvironments(t *testing.T) {
	t.Parallel()
	sink := &recordingActivitySink{}
	service, err := NewConnectionService(NewMemoryStore(), mustTestCipher(), &fixedClock{now: time.Now().UTC()}, &sequentialIDs{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	public, err := service.ConnectPublic(ctx, ConnectInput{
		SubjectID: "subject-1", ProviderID: "codeberg", EnvironmentID: "codeberg",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.ConnectToken(ctx, ConnectTokenInput{
		ConnectInput: ConnectInput{SubjectID: "subject-1", ProviderID: "github", EnvironmentID: "github"},
		Credentials:  TokenCredentials{AccessToken: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := service.BeginOAuth(ctx, ConnectInput{
		SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab",
	})
	if err != nil {
		t.Fatal(err)
	}

	if public.EnvironmentID != "codeberg" {
		t.Fatalf("public environment = %q, want codeberg", public.EnvironmentID)
	}
	if token.EnvironmentID != "connection:id-2" || oauth.EnvironmentID != "connection:id-3" {
		t.Fatalf("private environments are not connection-unique: token=%q oauth=%q", token.EnvironmentID, oauth.EnvironmentID)
	}
	if len(sink.environments) != 3 {
		t.Fatalf("saved environments = %#v", sink.environments)
	}
	if sink.environments[0].Scope != activity.EnvironmentScopeGlobal || sink.environments[0].OwnerSubject != nil {
		t.Fatalf("public connection environment = %#v", sink.environments[0])
	}
	for _, environment := range sink.environments[1:] {
		if environment.Scope != activity.EnvironmentScopeSubject || environment.OwnerSubject == nil || *environment.OwnerSubject != "subject-1" {
			t.Fatalf("private connection environment = %#v", environment)
		}
		if environment.Key != string(environment.ID) {
			t.Fatalf("private environment key %q is not unique id %q", environment.Key, environment.ID)
		}
		if environment.Metadata["visibility"] != "private" || environment.Metadata["connection_id"] == "" {
			t.Fatalf("private environment metadata = %#v", environment.Metadata)
		}
	}
}

func TestConnectionServiceEnforcesCredentialAndScopeLimits(t *testing.T) {
	t.Parallel()
	service := mustConnectionService(NewMemoryStore(), &fixedClock{now: time.Now().UTC()}, &sequentialIDs{})
	ctx := context.Background()
	base := ConnectInput{SubjectID: "subject-1", ProviderID: "gitlab", EnvironmentID: "gitlab", IncludePrivate: true}

	tooManyScopes := make([]string, 51)
	for index := range tooManyScopes {
		tooManyScopes[index] = fmt.Sprintf("scope-%d", index)
	}
	for name, test := range map[string]struct {
		token  string
		scopes []string
		want   error
	}{
		"token length": {token: strings.Repeat("x", 4097), want: ErrAccessTokenTooLong},
		"scope count":  {token: "secret", scopes: tooManyScopes, want: ErrTooManyScopes},
		"duplicate":    {token: "secret", scopes: []string{"repo", " repo "}, want: ErrDuplicateScope},
		"empty scope":  {token: "secret", scopes: []string{"   "}, want: ErrInvalidScope},
		"long scope":   {token: "secret", scopes: []string{strings.Repeat("s", 129)}, want: ErrInvalidScope},
		"unsafe scope": {token: "secret", scopes: []string{"read user"}, want: ErrInvalidScope},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.Scopes = test.scopes
			_, err := service.ConnectToken(ctx, ConnectTokenInput{ConnectInput: input, Credentials: TokenCredentials{AccessToken: test.token}})
			if !errors.Is(err, test.want) {
				t.Fatalf("ConnectToken() error = %v, want %v", err, test.want)
			}
		})
	}

	connection, err := service.ConnectToken(ctx, ConnectTokenInput{ConnectInput: base, Credentials: TokenCredentials{AccessToken: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	tooLong := strings.Repeat("x", 4097)
	if _, err := service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Token: &tooLong}); !errors.Is(err, ErrAccessTokenTooLong) {
		t.Fatalf("Update(long token) error = %v", err)
	}
	duplicates := []string{"repo", " repo "}
	if _, err := service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Scopes: &duplicates}); !errors.Is(err, ErrDuplicateScope) {
		t.Fatalf("Update(duplicate scopes) error = %v", err)
	}

	if _, err := service.Revoke(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	revoked, err := service.store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Connection.Status != ConnectionRevoked || len(revoked.Credentials.AccessToken) != 0 {
		t.Fatalf("revoke did not sanitize tombstone: %#v", revoked)
	}
	enabled := true
	if _, err := service.Update(ctx, UpdateConnectionInput{ID: connection.ID, Enabled: &enabled}); !errors.Is(err, ErrInvalidConnectionStatus) {
		t.Fatalf("Update(revoked) error = %v", err)
	}
}

type recordingOAuthTokenRevoker struct {
	store            *MemoryStore
	providerID       string
	token            []byte
	calls            int
	sawSanitizedData bool
	err              error
}

func (revoker *recordingOAuthTokenRevoker) RevokeOAuthToken(ctx context.Context, providerID string, token []byte) error {
	revoker.calls++
	revoker.providerID = providerID
	revoker.token = append([]byte(nil), token...)
	records, err := revoker.store.ListConnections(ctx, "subject-revoke")
	if err == nil && len(records) == 1 && records[0].Connection.Status == ConnectionRevoked &&
		len(records[0].Credentials.AccessToken) == 0 && len(records[0].Credentials.RefreshToken) == 0 {
		revoker.sawSanitizedData = true
	}
	return revoker.err
}

func TestConnectionServiceRevokesProviderAfterLocalSanitizationAndPurgesOwnedData(t *testing.T) {
	store := NewMemoryStore()
	revoker := &recordingOAuthTokenRevoker{store: store, err: errors.New("upstream included oauth-access in failure")}
	sink := &recordingActivitySink{}
	service, err := NewConnectionServiceWithRevoker(
		store,
		mustTestCipher(),
		&fixedClock{now: time.Date(2026, 8, 12, 6, 0, 0, 0, time.UTC)},
		&sequentialIDs{},
		revoker,
		sink,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connection, err := service.BeginOAuth(ctx, ConnectInput{
		SubjectID: "subject-revoke", ProviderID: "gitlab", EnvironmentID: "gitlab", IncludePrivate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteOAuth(ctx, connection.ID, TokenCredentials{
		AccessToken: "oauth-access", RefreshToken: "oauth-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSyncJob(ctx, SyncJob{ID: "job-revoke", ConnectionID: connection.ID}); err != nil {
		t.Fatal(err)
	}

	revoked, err := service.Revoke(ctx, connection.ID)
	if err != nil {
		t.Fatalf("Revoke() must ignore provider failure: %v", err)
	}
	if revoked.Status != ConnectionRevoked || revoked.LastError != "" {
		t.Fatalf("Revoke() = %#v", revoked)
	}
	if revoker.calls != 1 || revoker.providerID != "gitlab" || string(revoker.token) != "oauth-access" {
		t.Fatalf("provider revocation = calls %d, provider %q, token %q", revoker.calls, revoker.providerID, revoker.token)
	}
	if !revoker.sawSanitizedData {
		t.Fatal("provider revocation ran before the local credential tombstone was persisted")
	}
	raw, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Connection.Status != ConnectionRevoked || len(raw.Credentials.AccessToken) != 0 || len(raw.Credentials.RefreshToken) != 0 {
		t.Fatalf("retained tombstone = %#v", raw)
	}
	if _, err := service.Get(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(revoked) error = %v, want ErrNotFound", err)
	}
	listed, err := service.List(ctx, "subject-revoke")
	if err != nil || len(listed) != 0 {
		t.Fatalf("List(revoked) = %#v, %v", listed, err)
	}
	jobs, err := store.ListSyncJobs(ctx, connection.ID)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("sync jobs after revoke = %#v, %v", jobs, err)
	}
	if len(sink.replacements) != 1 || sink.replacements[0].Subject != "subject-revoke" {
		t.Fatalf("fact purge requests = %#v", sink.replacements)
	}
}

func TestConnectionServiceUsesSafeLocalOnlyRevocationForTokenAuthAndDecryptFailure(t *testing.T) {
	store := NewMemoryStore()
	revoker := &recordingOAuthTokenRevoker{store: store}
	service, err := NewConnectionServiceWithRevoker(store, mustTestCipher(), nil, &sequentialIDs{}, revoker)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tokenConnection, err := service.ConnectToken(ctx, ConnectTokenInput{
		ConnectInput: ConnectInput{SubjectID: "subject-token", ProviderID: "gitlab", EnvironmentID: "gitlab"},
		Credentials:  TokenCredentials{AccessToken: "personal-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke(ctx, tokenConnection.ID); err != nil {
		t.Fatal(err)
	}
	if revoker.calls != 0 {
		t.Fatalf("token-auth connection made %d provider revocation calls", revoker.calls)
	}

	oauthConnection, err := service.BeginOAuth(ctx, ConnectInput{
		SubjectID: "subject-corrupt", ProviderID: "github", EnvironmentID: "github",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteOAuth(ctx, oauthConnection.ID, TokenCredentials{AccessToken: "cannot-decrypt"}); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetConnection(ctx, oauthConnection.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Credentials.AccessToken = []byte("invalid ciphertext")
	if err := store.SaveConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke(ctx, oauthConnection.ID); err != nil {
		t.Fatalf("Revoke(corrupt ciphertext) error = %v", err)
	}
	if revoker.calls != 0 {
		t.Fatalf("corrupt credential made %d provider revocation calls", revoker.calls)
	}
	record, err = store.GetConnection(ctx, oauthConnection.ID)
	if err != nil || len(record.Credentials.AccessToken) != 0 || record.Connection.Status != ConnectionRevoked {
		t.Fatalf("corrupt credential tombstone = %#v, %v", record, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
