package authadapter

import (
	"context"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

// UnavailablePasskeyVerifier makes an environment without a real WebAuthn
// implementation explicit. It never creates options and never accepts a
// registration or assertion.
type UnavailablePasskeyVerifier struct{}

var _ coreauth.PasskeyVerifier = UnavailablePasskeyVerifier{}

func (UnavailablePasskeyVerifier) CreateRegistrationOptions(context.Context, coreauth.RegistrationOptionsInput) (coreauth.PasskeyVerifierOptions, error) {
	return coreauth.PasskeyVerifierOptions{}, coreauth.ErrVerifierUnavailable
}

func (UnavailablePasskeyVerifier) VerifyRegistration(context.Context, coreauth.RegistrationVerificationInput) (coreauth.RegistrationVerification, error) {
	return coreauth.RegistrationVerification{}, coreauth.ErrVerifierUnavailable
}

func (UnavailablePasskeyVerifier) CreateAuthenticationOptions(context.Context, coreauth.AuthenticationOptionsInput) (coreauth.PasskeyVerifierOptions, error) {
	return coreauth.PasskeyVerifierOptions{}, coreauth.ErrVerifierUnavailable
}

func (UnavailablePasskeyVerifier) VerifyAuthentication(context.Context, coreauth.AuthenticationVerificationInput) (coreauth.AuthenticationVerification, error) {
	return coreauth.AuthenticationVerification{}, coreauth.ErrVerifierUnavailable
}

// TestingPasskeyVerifier is an explicit callback adapter for tests at the
// application boundary. It performs no WebAuthn parsing or cryptography. A nil
// callback fails closed with ErrVerifierUnavailable.
//
// Executable wiring must use UnavailablePasskeyVerifier until a standards-
// compliant WebAuthn library adapter is installed.
type TestingPasskeyVerifier struct {
	CreateRegistrationOptionsFunc   func(context.Context, coreauth.RegistrationOptionsInput) (coreauth.PasskeyVerifierOptions, error)
	VerifyRegistrationFunc          func(context.Context, coreauth.RegistrationVerificationInput) (coreauth.RegistrationVerification, error)
	CreateAuthenticationOptionsFunc func(context.Context, coreauth.AuthenticationOptionsInput) (coreauth.PasskeyVerifierOptions, error)
	VerifyAuthenticationFunc        func(context.Context, coreauth.AuthenticationVerificationInput) (coreauth.AuthenticationVerification, error)
}

var _ coreauth.PasskeyVerifier = (*TestingPasskeyVerifier)(nil)

func (v *TestingPasskeyVerifier) CreateRegistrationOptions(ctx context.Context, input coreauth.RegistrationOptionsInput) (coreauth.PasskeyVerifierOptions, error) {
	if v == nil || v.CreateRegistrationOptionsFunc == nil {
		return coreauth.PasskeyVerifierOptions{}, coreauth.ErrVerifierUnavailable
	}
	return v.CreateRegistrationOptionsFunc(ctx, input)
}

func (v *TestingPasskeyVerifier) VerifyRegistration(ctx context.Context, input coreauth.RegistrationVerificationInput) (coreauth.RegistrationVerification, error) {
	if v == nil || v.VerifyRegistrationFunc == nil {
		return coreauth.RegistrationVerification{}, coreauth.ErrVerifierUnavailable
	}
	return v.VerifyRegistrationFunc(ctx, input)
}

func (v *TestingPasskeyVerifier) CreateAuthenticationOptions(ctx context.Context, input coreauth.AuthenticationOptionsInput) (coreauth.PasskeyVerifierOptions, error) {
	if v == nil || v.CreateAuthenticationOptionsFunc == nil {
		return coreauth.PasskeyVerifierOptions{}, coreauth.ErrVerifierUnavailable
	}
	return v.CreateAuthenticationOptionsFunc(ctx, input)
}

func (v *TestingPasskeyVerifier) VerifyAuthentication(ctx context.Context, input coreauth.AuthenticationVerificationInput) (coreauth.AuthenticationVerification, error) {
	if v == nil || v.VerifyAuthenticationFunc == nil {
		return coreauth.AuthenticationVerification{}, coreauth.ErrVerifierUnavailable
	}
	return v.VerifyAuthenticationFunc(ctx, input)
}
