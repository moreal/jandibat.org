package graphql

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

var errInvalidPasskeyCredential = errors.New("invalid passkey credential")

const maxPasskeyCredentialJSONBytes = 32 << 10

const passkeyCompensationTimeout = 2 * time.Second
const passkeyCompensationAttempts = 3

type PasskeyMutationService interface {
	BeginPasskeyRegistration(context.Context, string) (auth.PasskeyOptions, error)
	CompletePasskeyRegistrationForUser(context.Context, string, string, json.RawMessage, string) (auth.PasskeyCredential, error)
	BeginPasskeyLogin(context.Context, string) (auth.PasskeyOptions, error)
	CompletePasskeyLogin(context.Context, string, []byte, json.RawMessage, auth.SessionMetadata) (auth.SessionGrant, error)
	GetSessionByID(context.Context, string, string) (auth.Session, error)
	RevokeSession(context.Context, string) error
}

type passkeyMutationServiceContextKey struct{}
type passkeySessionMetadataContextKey struct{}
type postAuthAuditActorPublisherContextKey struct{}

// ContextWithPasskeyMutationService installs an application port at the
// trusted GraphQL transport boundary, never from client-controlled input.
func ContextWithPasskeyMutationService(ctx context.Context, service PasskeyMutationService) context.Context {
	return context.WithValue(ctx, passkeyMutationServiceContextKey{}, service)
}

// ContextWithPasskeySessionMetadata carries trusted request metadata into
// session issuance. GraphQL variables cannot set this value.
func ContextWithPasskeySessionMetadata(ctx context.Context, metadata auth.SessionMetadata) context.Context {
	return context.WithValue(ctx, passkeySessionMetadataContextKey{}, metadata)
}

// ContextWithPostAuthAuditActorPublisher installs a trusted transport callback
// for attributing a successful anonymous sign-in's audit outcome. The callback
// receives only the verified user ID, never the newly issued bearer token.
func ContextWithPostAuthAuditActorPublisher(ctx context.Context, publish func(string)) context.Context {
	return context.WithValue(ctx, postAuthAuditActorPublisherContextKey{}, publish)
}

func passkeyService(ctx context.Context) PasskeyMutationService {
	service, _ := ctx.Value(passkeyMutationServiceContextKey{}).(PasskeyMutationService)
	return service
}

func verifiedPasskeyActor(ctx context.Context) (string, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed || identity.userID == "" {
		return "", errNodeAuthentication
	}
	return identity.userID, nil
}

func projectPasskeyOptions(options auth.PasskeyOptions) (*model.PasskeyOptions, error) {
	if options.CeremonyID == "" || options.ExpiresAt.IsZero() || !json.Valid(options.PublicKey) || !bytes.HasPrefix(bytes.TrimSpace(options.PublicKey), []byte("{")) || len(options.PublicKey) > maxPasskeyCredentialJSONBytes {
		return nil, errNodeLookup
	}
	return &model.PasskeyOptions{CeremonyID: options.CeremonyID, PublicKeyJSON: string(options.PublicKey), ExpiresAt: scalar.DateTime(options.ExpiresAt)}, nil
}

func passkeyError(code, message, field string) []*model.MutationError {
	item := &model.MutationError{Code: code, Message: message}
	if field != "" {
		item.Field = &field
	}
	return []*model.MutationError{item}
}

func passkeyDomainError(err error) ([]*model.MutationError, error) {
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		return passkeyError("BAD_USER_INPUT", "Invalid passkey input.", "input"), nil
	case errors.Is(err, auth.ErrInvalidCeremony), errors.Is(err, auth.ErrPasskeyVerification):
		return passkeyError("PASSKEY_VERIFICATION_FAILED", "Passkey verification failed.", ""), nil
	case errors.Is(err, auth.ErrMagicLinkReauthenticationRequired), errors.Is(err, auth.ErrInvalidSignCount):
		return passkeyError("REAUTHENTICATION_REQUIRED", "Sign in with a magic link before using this passkey again.", ""), nil
	case errors.Is(err, auth.ErrCredentialExists):
		return passkeyError("CONFLICT", "Passkey is already registered.", ""), nil
	case errors.Is(err, auth.ErrUserDisabled), errors.Is(err, auth.ErrInvalidSession):
		return nil, errNodeAuthentication
	default:
		return nil, errNodeLookup
	}
}

func (r *mutationResolver) resolveBeginPasskeyRegistration(ctx context.Context) (*model.BeginPasskeyRegistrationPayload, error) {
	actor, err := verifiedPasskeyActor(ctx)
	if err != nil {
		return nil, err
	}
	service := passkeyService(ctx)
	if service == nil {
		return nil, errNodeLookup
	}
	options, err := service.BeginPasskeyRegistration(ctx, actor)
	if err != nil {
		items, transportErr := passkeyDomainError(err)
		return &model.BeginPasskeyRegistrationPayload{Errors: items}, transportErr
	}
	projected, err := projectPasskeyOptions(options)
	if err != nil {
		return nil, err
	}
	return &model.BeginPasskeyRegistrationPayload{Errors: []*model.MutationError{}, Options: projected}, nil
}

func (r *mutationResolver) resolveFinishPasskeyRegistration(ctx context.Context, input model.FinishPasskeyRegistrationInput) (*model.FinishPasskeyRegistrationPayload, error) {
	actor, err := verifiedPasskeyActor(ctx)
	if err != nil {
		return nil, err
	}
	service := passkeyService(ctx)
	if service == nil {
		return nil, errNodeLookup
	}
	_, document, err := decodePasskeyCredentialJSON(input.CredentialJSON)
	if err != nil || strings.TrimSpace(input.CeremonyID) == "" {
		return &model.FinishPasskeyRegistrationPayload{Errors: passkeyError("BAD_USER_INPUT", "Invalid passkey input.", "credentialJSON")}, nil
	}
	label := ""
	if input.Label != nil {
		label = *input.Label
	}
	credential, err := service.CompletePasskeyRegistrationForUser(ctx, actor, input.CeremonyID, document, label)
	if err != nil {
		items, transportErr := passkeyDomainError(err)
		return &model.FinishPasskeyRegistrationPayload{Errors: items}, transportErr
	}
	if credential.ID == "" || credential.UserID != actor || credential.CreatedAt.IsZero() {
		return nil, errNodeLookup
	}
	var labelPtr *string
	if credential.Label != "" {
		value := credential.Label
		labelPtr = &value
	}
	transports := append([]string{}, credential.Transports...)
	return &model.FinishPasskeyRegistrationPayload{Errors: []*model.MutationError{}, Credential: &model.PasskeyCredential{
		ID: credential.ID, Label: labelPtr, Transports: transports, CreatedAt: scalar.DateTime(credential.CreatedAt), LastUsedAt: optionalDateTime(credential.LastUsedAt),
	}}, nil
}

func (r *mutationResolver) resolveBeginPasskeySignIn(ctx context.Context) (*model.BeginPasskeySignInPayload, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return nil, errNodeAuthentication
	}
	service := passkeyService(ctx)
	if service == nil {
		return nil, errNodeLookup
	}
	options, err := service.BeginPasskeyLogin(ctx, "")
	if err != nil {
		items, transportErr := passkeyDomainError(err)
		return &model.BeginPasskeySignInPayload{Errors: items}, transportErr
	}
	projected, err := projectPasskeyOptions(options)
	if err != nil {
		return nil, err
	}
	return &model.BeginPasskeySignInPayload{Errors: []*model.MutationError{}, Options: projected}, nil
}

func (r *mutationResolver) resolveFinishPasskeySignIn(ctx context.Context, input model.FinishPasskeySignInInput) (*model.FinishPasskeySignInPayload, error) {
	identity, _ := ctx.Value(viewerContextKey{}).(viewerIdentity)
	if identity.failed {
		return nil, errNodeAuthentication
	}
	service := passkeyService(ctx)
	if service == nil {
		return nil, errNodeLookup
	}
	credentialID, document, err := decodePasskeyCredentialJSON(input.CredentialJSON)
	if err != nil || strings.TrimSpace(input.CeremonyID) == "" {
		return &model.FinishPasskeySignInPayload{Errors: passkeyError("BAD_USER_INPUT", "Invalid passkey input.", "credentialJSON")}, nil
	}
	sink, ok := authTransportFromContext(ctx)
	if !ok {
		return nil, errNodeLookup
	}
	reporter, ok := authMutationFailureReporterFromContext(ctx)
	if !ok {
		return nil, errNodeLookup
	}
	metadata, _ := ctx.Value(passkeySessionMetadataContextKey{}).(auth.SessionMetadata)
	grant, err := service.CompletePasskeyLogin(ctx, input.CeremonyID, credentialID, document, metadata)
	if err != nil {
		markPasskeySignInFailureCommit(ctx, err)
		items, transportErr := passkeyDomainError(err)
		return &model.FinishPasskeySignInPayload{Errors: items}, transportErr
	}
	if grant.Token == "" || grant.UserID == "" || grant.SessionID == "" || grant.ExpiresAt.IsZero() {
		compensateIssuedSession(ctx, service, reporter, grant.Token)
		return nil, errNodeLookup
	}
	session, err := service.GetSessionByID(ctx, grant.UserID, grant.SessionID)
	if err != nil || session.UserID != grant.UserID || session.ID != grant.SessionID || session.RevokedAt != nil {
		compensateIssuedSession(ctx, service, reporter, grant.Token)
		return nil, errNodeLookup
	}
	if _, err := uuid.Parse(session.ID); err != nil {
		compensateIssuedSession(ctx, service, reporter, grant.Token)
		return nil, errNodeLookup
	}
	if err := sink.SetSessionCookie(grant.Token, grant.ExpiresAt); err != nil {
		// The trusted sink guarantees that a failed Set did not flush a
		// session cookie. Clear is additional defense if that contract is
		// violated; the server-side revoke is authoritative.
		_ = sink.ClearSessionCookie()
		compensateIssuedSession(ctx, service, reporter, grant.Token)
		return nil, errNodeLookup
	}
	if publish, ok := ctx.Value(postAuthAuditActorPublisherContextKey{}).(func(string)); ok && publish != nil {
		publish(session.UserID)
	}
	PublishMutationAuditTarget(ctx, "session", session.ID)
	return &model.FinishPasskeySignInPayload{Errors: []*model.MutationError{}, Session: projectSession(session)}, nil
}

// A valid ceremony can already be consumed when authentication rejects it.
// Preserve that replay guard and the correlated failed audit outcome in the
// same transaction. Unrelated input and persistence errors keep rolling back.
func markPasskeySignInFailureCommit(ctx context.Context, err error) {
	if errors.Is(err, auth.ErrInvalidCeremony) ||
		errors.Is(err, auth.ErrPasskeyVerification) ||
		errors.Is(err, auth.ErrInvalidSignCount) ||
		errors.Is(err, auth.ErrMagicLinkReauthenticationRequired) ||
		errors.Is(err, auth.ErrUserDisabled) {
		operations.MarkMutationFailureCommit(ctx)
	}
}

func compensateIssuedSession(ctx context.Context, service PasskeyMutationService, reporter AuthMutationFailureReporter, token string) {
	// The request can be cancelled after session issuance. Do not let its
	// cancellation suppress revocation, but cap cleanup work.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), passkeyCompensationTimeout)
	defer cancel()
	if token != "" {
		for attempt := 0; attempt < passkeyCompensationAttempts && cleanupCtx.Err() == nil; attempt++ {
			if service.RevokeSession(cleanupCtx, token) == nil {
				return
			}
		}
	}
	// Fixed event only: neither bearer token nor persistence error reaches
	// the reporter or the GraphQL response.
	reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer reportCancel()
	reporter.ReportSessionCompensationFailure(reportCtx)
}

func decodePasskeyCredentialJSON(raw string) ([]byte, json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxPasskeyCredentialJSONBytes {
		return nil, nil, errInvalidPasskeyCredential
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil || envelope == nil {
		return nil, nil, errInvalidPasskeyCredential
	}
	// A duplicate top-level key can make an authenticator and an intermediary
	// interpret different credential IDs. Reject it before decoding the ID.
	decoder := json.NewDecoder(strings.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, nil, errInvalidPasskeyCredential
	}
	seen := make(map[string]bool, len(envelope))
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return nil, nil, errInvalidPasskeyCredential
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, nil, errInvalidPasskeyCredential
		}
	}
	allowed := map[string]bool{
		"id": true, "rawId": true, "type": true, "authenticatorAttachment": true,
		"response": true, "clientExtensionResults": true,
	}
	for key := range envelope {
		if !allowed[key] {
			return nil, nil, errInvalidPasskeyCredential
		}
	}
	readString := func(key string, max int) (string, bool) {
		value, ok := envelope[key]
		if !ok {
			return "", false
		}
		var decoded string
		if json.Unmarshal(value, &decoded) != nil || len(decoded) == 0 || len(decoded) > max {
			return "", false
		}
		return decoded, true
	}
	encodedID, ok := readString("id", 2048)
	if !ok {
		return nil, nil, errInvalidPasskeyCredential
	}
	rawID, ok := readString("rawId", 2048)
	if !ok || encodedID != rawID {
		return nil, nil, errInvalidPasskeyCredential
	}
	typeName, ok := readString("type", len("public-key"))
	if !ok || typeName != "public-key" {
		return nil, nil, errInvalidPasskeyCredential
	}
	if attachment, ok := envelope["authenticatorAttachment"]; ok && !bytes.Equal(attachment, []byte("null")) {
		var value string
		if json.Unmarshal(attachment, &value) != nil || (value != "platform" && value != "cross-platform") {
			return nil, nil, errInvalidPasskeyCredential
		}
	}
	for _, key := range []string{"response", "clientExtensionResults"} {
		value, ok := envelope[key]
		if !ok {
			return nil, nil, errInvalidPasskeyCredential
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil || object == nil || len(object) > 20 {
			return nil, nil, errInvalidPasskeyCredential
		}
	}
	id, err := base64.RawURLEncoding.DecodeString(rawID)
	if err != nil || len(id) == 0 || base64.RawURLEncoding.EncodeToString(id) != rawID {
		return nil, nil, errInvalidPasskeyCredential
	}
	return id, json.RawMessage(raw), nil
}
