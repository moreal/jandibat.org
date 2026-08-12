package integrations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type ConnectionService struct {
	store           ConnectionStore
	cipher          SecretCipher
	tokenRevoker    OAuthTokenRevoker
	environmentSink ActivitySink
	clock           Clock
	ids             IDGenerator
}

func NewConnectionService(store ConnectionStore, cipher SecretCipher, clock Clock, ids IDGenerator, environmentSinks ...ActivitySink) (*ConnectionService, error) {
	return NewConnectionServiceWithRevoker(store, cipher, clock, ids, nil, environmentSinks...)
}

func NewConnectionServiceWithRevoker(store ConnectionStore, cipher SecretCipher, clock Clock, ids IDGenerator, tokenRevoker OAuthTokenRevoker, environmentSinks ...ActivitySink) (*ConnectionService, error) {
	if store == nil {
		return nil, ErrMissingStore
	}
	if cipher == nil {
		return nil, ErrMissingCipher
	}
	service := &ConnectionService{
		store:        store,
		cipher:       cipher,
		tokenRevoker: tokenRevoker,
		clock:        clockOrDefault(clock),
		ids:          idGeneratorOrDefault(ids),
	}
	if len(environmentSinks) != 0 {
		service.environmentSink = environmentSinks[0]
	}
	return service, nil
}

type ConnectInput struct {
	SubjectID            string
	ProviderID           string
	EnvironmentID        string
	ExternalAccountID    string
	ExternalAccountLogin string
	Scopes               []string
	IncludePrivate       bool
}

type ConnectTokenInput struct {
	ConnectInput
	Credentials TokenCredentials
}

type UpdateConnectionInput struct {
	ID      string
	Token   *string
	Scopes  *[]string
	Enabled *bool
}

func (service *ConnectionService) Update(ctx context.Context, input UpdateConnectionInput) (ProviderConnection, error) {
	if input.ID == "" {
		return ProviderConnection{}, ErrEmptyConnectionID
	}
	if input.Token == nil && input.Scopes == nil && input.Enabled == nil {
		return ProviderConnection{}, ErrInvalidConnectionStatus
	}
	record, err := service.store.GetConnection(ctx, input.ID)
	if err != nil {
		return ProviderConnection{}, err
	}
	if record.Connection.Status == ConnectionRevoked {
		return ProviderConnection{}, ErrInvalidConnectionStatus
	}
	if input.Token != nil {
		if !record.Connection.PrivateDataEnabled {
			// Token rotation is meaningful only for an explicitly private-data
			// connection. Reject before touching the cipher so an opt-out row can
			// never accumulate dormant credential material in any repository.
			return ProviderConnection{}, ErrPrivateDataUnsupported
		}
		if strings.TrimSpace(*input.Token) == "" {
			return ProviderConnection{}, ErrEmptyAccessToken
		}
		if len(*input.Token) > 4096 {
			return ProviderConnection{}, ErrAccessTokenTooLong
		}
		if record.Connection.AuthMethod != AuthToken {
			return ProviderConnection{}, ErrUnsupportedAuthMethod
		}
		record.Credentials, err = service.encryptCredentials(ctx, TokenCredentials{AccessToken: *input.Token})
		if err != nil {
			return ProviderConnection{}, err
		}
		record.Connection.TokenExpiresAt = nil
		record.Connection.Status = ConnectionActive
		record.Connection.LastError = ""
	}
	if input.Scopes != nil {
		if err := validateScopes(*input.Scopes); err != nil {
			return ProviderConnection{}, err
		}
		record.Connection.Scopes = normalizeStrings(*input.Scopes)
	}
	if input.Enabled != nil {
		if !*input.Enabled {
			record.Connection.Status = ConnectionDisabled
			record.Connection.LastError = ""
		} else {
			record.Connection.Status = ConnectionActive
		}
	}
	record.Connection.UpdatedAt = service.clock.Now()
	if err := service.store.SaveConnection(ctx, record); err != nil {
		return ProviderConnection{}, err
	}
	return cloneConnection(record.Connection), nil
}

func (service *ConnectionService) ConnectToken(ctx context.Context, input ConnectTokenInput) (ProviderConnection, error) {
	if strings.TrimSpace(input.Credentials.AccessToken) == "" {
		return ProviderConnection{}, ErrEmptyAccessToken
	}
	if len(input.Credentials.AccessToken) > 4096 {
		return ProviderConnection{}, ErrAccessTokenTooLong
	}
	provider, err := validateConnectInput(input.ConnectInput)
	if err != nil {
		return ProviderConnection{}, err
	}
	if !provider.SupportsToken {
		return ProviderConnection{}, ErrUnsupportedAuthMethod
	}
	if err := validatePrivateDataConsent(provider, AuthToken, input.IncludePrivate); err != nil {
		return ProviderConnection{}, err
	}

	var credentials EncryptedCredentials
	if input.IncludePrivate {
		credentials, err = service.encryptCredentials(ctx, input.Credentials)
		if err != nil {
			return ProviderConnection{}, err
		}
	}
	connection, err := service.newConnection(input.ConnectInput, AuthToken, ConnectionActive)
	if err != nil {
		return ProviderConnection{}, err
	}
	if input.IncludePrivate {
		connection.TokenExpiresAt = copyTimePointer(input.Credentials.ExpiresAt)
	}
	if err := service.saveNewConnection(ctx, ConnectionRecord{Connection: connection, Credentials: credentials}); err != nil {
		return ProviderConnection{}, err
	}
	return cloneConnection(connection), nil
}

func (service *ConnectionService) ConnectPublic(ctx context.Context, input ConnectInput) (ProviderConnection, error) {
	provider, err := validateConnectInput(input)
	if err != nil {
		return ProviderConnection{}, err
	}
	if err := validatePrivateDataConsent(provider, AuthNone, input.IncludePrivate); err != nil {
		return ProviderConnection{}, err
	}
	connection, err := service.newConnection(input, AuthNone, ConnectionActive)
	if err != nil {
		return ProviderConnection{}, err
	}
	if err := service.saveNewConnection(ctx, ConnectionRecord{Connection: connection}); err != nil {
		return ProviderConnection{}, err
	}
	return cloneConnection(connection), nil
}

func (service *ConnectionService) BeginOAuth(ctx context.Context, input ConnectInput) (ProviderConnection, error) {
	provider, err := validateConnectInput(input)
	if err != nil {
		return ProviderConnection{}, err
	}
	if !provider.SupportsOAuth {
		return ProviderConnection{}, ErrUnsupportedAuthMethod
	}
	if err := validatePrivateDataConsent(provider, AuthOAuth2, input.IncludePrivate); err != nil {
		return ProviderConnection{}, err
	}
	connection, err := service.newConnection(input, AuthOAuth2, ConnectionPending)
	if err != nil {
		return ProviderConnection{}, err
	}
	if err := service.saveNewConnection(ctx, ConnectionRecord{Connection: connection}); err != nil {
		return ProviderConnection{}, err
	}
	return cloneConnection(connection), nil
}

func (service *ConnectionService) CompleteOAuth(ctx context.Context, connectionID string, credentials TokenCredentials) (ProviderConnection, error) {
	return service.CompleteOAuthConnection(ctx, connectionID, credentials, "", "", nil)
}

func (service *ConnectionService) CompleteOAuthConnection(ctx context.Context, connectionID string, credentials TokenCredentials, externalAccountID, externalAccountLogin string, scopes []string) (ProviderConnection, error) {
	if connectionID == "" {
		return ProviderConnection{}, ErrEmptyConnectionID
	}
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return ProviderConnection{}, ErrEmptyAccessToken
	}
	if len(credentials.AccessToken) > 4096 {
		return ProviderConnection{}, ErrAccessTokenTooLong
	}
	if scopes != nil {
		if err := validateScopes(scopes); err != nil {
			return ProviderConnection{}, err
		}
	}
	record, err := service.store.GetConnection(ctx, connectionID)
	if err != nil {
		return ProviderConnection{}, err
	}
	if record.Connection.AuthMethod != AuthOAuth2 || record.Connection.Status != ConnectionPending {
		return ProviderConnection{}, ErrInvalidConnectionStatus
	}
	if record.Connection.PrivateDataEnabled {
		record.Credentials, err = service.encryptCredentials(ctx, credentials)
		if err != nil {
			return ProviderConnection{}, err
		}
	} else {
		record.Credentials = EncryptedCredentials{}
	}
	now := service.clock.Now()
	record.Connection.Status = ConnectionActive
	if record.Connection.PrivateDataEnabled {
		record.Connection.TokenExpiresAt = copyTimePointer(credentials.ExpiresAt)
	} else {
		record.Connection.TokenExpiresAt = nil
	}
	if externalAccountID != "" {
		record.Connection.ExternalAccountID = externalAccountID
	}
	if externalAccountLogin != "" {
		record.Connection.ExternalAccountLogin = externalAccountLogin
	}
	if scopes != nil {
		record.Connection.Scopes = normalizeStrings(scopes)
	}
	record.Connection.LastError = ""
	record.Connection.UpdatedAt = now
	if err := service.store.SaveConnection(ctx, record); err != nil {
		return ProviderConnection{}, err
	}
	return cloneConnection(record.Connection), nil
}

func (service *ConnectionService) Revoke(ctx context.Context, connectionID string) (ProviderConnection, error) {
	if connectionID == "" {
		return ProviderConnection{}, ErrEmptyConnectionID
	}
	if store, ok := service.store.(AtomicConnectionRevocationStore); ok {
		return store.RevokeConnectionAggregate(ctx, connectionID, service.clock.Now())
	}
	record, err := service.store.GetConnection(ctx, connectionID)
	if err != nil {
		return ProviderConnection{}, err
	}
	var accessToken []byte
	if record.Connection.AuthMethod == AuthOAuth2 && service.tokenRevoker != nil && len(record.Credentials.AccessToken) != 0 {
		// Decrypt before destroying the only persisted copy. Failure to recover
		// the provider token must never prevent the local disconnect.
		accessToken, _ = service.cipher.Decrypt(ctx, record.Credentials.AccessToken)
		defer clearBytes(accessToken)
	}
	record.Credentials = EncryptedCredentials{}
	record.Connection.Status = ConnectionRevoked
	record.Connection.LastError = ""
	record.Connection.TokenExpiresAt = nil
	record.Connection.UpdatedAt = service.clock.Now()
	if err := service.store.SaveConnection(ctx, record); err != nil {
		if len(accessToken) != 0 {
			_ = service.tokenRevoker.RevokeOAuthToken(ctx, record.Connection.ProviderID, accessToken)
		}
		return ProviderConnection{}, err
	}
	var cleanupErr error
	if record.Connection.AuthMethod != AuthNone {
		if replacer, ok := service.environmentSink.(interface {
			ReplaceFacts(context.Context, activity.LoadFactsInput, []activity.EnvironmentID, []activity.Fact) error
		}); ok {
			if err := replacer.ReplaceFacts(ctx, activity.LoadFactsInput{
				Subject: activity.SubjectID(record.Connection.SubjectID),
			}, []activity.EnvironmentID{activity.EnvironmentID(record.Connection.EnvironmentID)}, nil); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("purge connection activity facts: %w", err))
			}
		}
	}
	if err := service.store.PurgeConnectionData(ctx, connectionID); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("purge connection data: %w", err))
	}
	if len(accessToken) != 0 {
		// Provider revocation is intentionally best-effort. Local credentials
		// and connection-owned data have already been destroyed, and adapter
		// failures are not exposed through the public disconnect path.
		_ = service.tokenRevoker.RevokeOAuthToken(ctx, record.Connection.ProviderID, accessToken)
	}
	if cleanupErr != nil {
		return ProviderConnection{}, cleanupErr
	}
	return cloneConnection(record.Connection), nil
}

func (service *ConnectionService) Get(ctx context.Context, connectionID string) (ProviderConnection, error) {
	if connectionID == "" {
		return ProviderConnection{}, ErrEmptyConnectionID
	}
	record, err := service.store.GetConnection(ctx, connectionID)
	if err != nil {
		return ProviderConnection{}, err
	}
	if record.Connection.Status == ConnectionRevoked {
		return ProviderConnection{}, ErrNotFound
	}
	return cloneConnection(record.Connection), nil
}

func (service *ConnectionService) List(ctx context.Context, subjectID string) ([]ProviderConnection, error) {
	if subjectID == "" {
		return nil, ErrEmptySubjectID
	}
	records, err := service.store.ListConnections(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	connections := make([]ProviderConnection, 0, len(records))
	for index := range records {
		if records[index].Connection.Status == ConnectionRevoked {
			continue
		}
		connections = append(connections, cloneConnection(records[index].Connection))
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].ID < connections[j].ID })
	return connections, nil
}

func (service *ConnectionService) newConnection(input ConnectInput, method AuthMethod, status ConnectionStatus) (ProviderConnection, error) {
	id, err := service.ids.NewID()
	if err != nil {
		return ProviderConnection{}, fmt.Errorf("generate connection id: %w", err)
	}
	environmentID := input.EnvironmentID
	if method != AuthNone {
		// Credential-backed connections are private to their subject. They must
		// never share the provider's global environment because that would mix
		// private facts into the anonymously readable provider timeline.
		environmentID = "connection:" + id
	}
	now := service.clock.Now()
	return ProviderConnection{
		ID:                   id,
		SubjectID:            input.SubjectID,
		ProviderID:           input.ProviderID,
		EnvironmentID:        environmentID,
		AuthMethod:           method,
		ExternalAccountID:    input.ExternalAccountID,
		ExternalAccountLogin: input.ExternalAccountLogin,
		Status:               status,
		Scopes:               normalizeStrings(input.Scopes),
		PrivateDataEnabled:   input.IncludePrivate,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

func (service *ConnectionService) saveNewConnection(ctx context.Context, record ConnectionRecord) error {
	if service.environmentSink != nil {
		provider, ok := BuiltInProvider(record.Connection.ProviderID)
		if !ok {
			return ErrInvalidProvider
		}
		environment := activity.Environment{
			ID:    activity.EnvironmentID(record.Connection.EnvironmentID),
			Key:   provider.ID,
			Name:  provider.Name,
			Scope: activity.EnvironmentScopeGlobal,
			Metadata: map[string]string{
				"provider_id":          provider.ID,
				"private_data_enabled": fmt.Sprintf("%t", record.Connection.PrivateDataEnabled),
			},
		}
		if record.Connection.AuthMethod != AuthNone {
			owner := activity.SubjectID(record.Connection.SubjectID)
			environment.Key = record.Connection.EnvironmentID
			environment.Scope = activity.EnvironmentScopeSubject
			environment.OwnerSubject = &owner
			environment.Metadata["visibility"] = "private"
			environment.Metadata["connection_id"] = record.Connection.ID
		}
		if err := service.environmentSink.SaveEnvironments(ctx, activity.SaveEnvironmentsInput{Environments: []activity.Environment{environment}}); err != nil {
			return fmt.Errorf("save connection environment: %w", err)
		}
	}
	return service.store.SaveConnection(ctx, record)
}

func (service *ConnectionService) encryptCredentials(ctx context.Context, credentials TokenCredentials) (EncryptedCredentials, error) {
	accessPlaintext := []byte(credentials.AccessToken)
	defer clearBytes(accessPlaintext)
	accessCiphertext, err := service.cipher.Encrypt(ctx, accessPlaintext)
	if err != nil {
		return EncryptedCredentials{}, fmt.Errorf("encrypt access token: %w", err)
	}
	result := EncryptedCredentials{AccessToken: accessCiphertext}
	if credentials.RefreshToken == "" {
		return result, nil
	}
	refreshPlaintext := []byte(credentials.RefreshToken)
	defer clearBytes(refreshPlaintext)
	refreshCiphertext, err := service.cipher.Encrypt(ctx, refreshPlaintext)
	if err != nil {
		return EncryptedCredentials{}, fmt.Errorf("encrypt refresh token: %w", err)
	}
	result.RefreshToken = refreshCiphertext
	return result, nil
}

func validateConnectInput(input ConnectInput) (ProviderCatalogItem, error) {
	if input.SubjectID == "" {
		return ProviderCatalogItem{}, ErrEmptySubjectID
	}
	if input.EnvironmentID == "" {
		return ProviderCatalogItem{}, ErrEmptyEnvironmentID
	}
	if err := validateScopes(input.Scopes); err != nil {
		return ProviderCatalogItem{}, err
	}
	provider, ok := BuiltInProvider(input.ProviderID)
	if !ok {
		return ProviderCatalogItem{}, ErrInvalidProvider
	}
	return provider, nil
}

func validateScopes(scopes []string) error {
	if len(scopes) > 50 {
		return ErrTooManyScopes
	}
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		normalized := strings.TrimSpace(scope)
		if len(normalized) == 0 || len(normalized) > 128 || !validScope(normalized) {
			return ErrInvalidScope
		}
		if _, duplicate := seen[normalized]; duplicate {
			return ErrDuplicateScope
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func validatePrivateDataConsent(provider ProviderCatalogItem, method AuthMethod, includePrivate bool) error {
	if method == AuthNone {
		if includePrivate {
			return ErrPrivateDataUnsupported
		}
		return nil
	}
	if includePrivate && !provider.SupportsPrivateData {
		return ErrPrivateDataUnsupported
	}
	return nil
}

func validScope(value string) bool {
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case strings.ContainsRune("._:/-", character):
		default:
			return false
		}
	}
	return true
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneConnection(value ProviderConnection) ProviderConnection {
	value.Scopes = copyStrings(value.Scopes)
	value.TokenExpiresAt = copyTimePointer(value.TokenExpiresAt)
	value.LastSyncedAt = copyTimePointer(value.LastSyncedAt)
	return value
}
