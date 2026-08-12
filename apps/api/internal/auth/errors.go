package auth

import "errors"

var (
	ErrInvalidInput                      = errors.New("auth: invalid input")
	ErrNotFound                          = errors.New("auth: not found")
	ErrConflict                          = errors.New("auth: conflict")
	ErrExpired                           = errors.New("auth: expired")
	ErrConsumed                          = errors.New("auth: already consumed")
	ErrUserDisabled                      = errors.New("auth: user is disabled")
	ErrInvalidMagicLink                  = errors.New("auth: invalid magic link")
	ErrInvalidSession                    = errors.New("auth: invalid session")
	ErrInvalidCeremony                   = errors.New("auth: invalid passkey ceremony")
	ErrPasskeyVerification               = errors.New("auth: passkey verification failed")
	ErrCredentialExists                  = errors.New("auth: passkey credential already exists")
	ErrInvalidSignCount                  = errors.New("auth: passkey signature counter did not advance")
	ErrMagicLinkReauthenticationRequired = errors.New("auth: magic-link reauthentication required")
	ErrVerifierUnavailable               = errors.New("auth: passkey verifier is unavailable")
)
