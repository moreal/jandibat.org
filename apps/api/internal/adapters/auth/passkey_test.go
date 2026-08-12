package authadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestUnavailablePasskeyVerifierFailsEveryOperationClosed(t *testing.T) {
	t.Parallel()
	verifier := UnavailablePasskeyVerifier{}
	if _, err := verifier.CreateRegistrationOptions(context.Background(), coreauth.RegistrationOptionsInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("CreateRegistrationOptions() error = %v", err)
	}
	if _, err := verifier.VerifyRegistration(context.Background(), coreauth.RegistrationVerificationInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("VerifyRegistration() error = %v", err)
	}
	if _, err := verifier.CreateAuthenticationOptions(context.Background(), coreauth.AuthenticationOptionsInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("CreateAuthenticationOptions() error = %v", err)
	}
	if _, err := verifier.VerifyAuthentication(context.Background(), coreauth.AuthenticationVerificationInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("VerifyAuthentication() error = %v", err)
	}
}

func TestTestingPasskeyVerifierFailsClosedForMissingCallbacks(t *testing.T) {
	t.Parallel()
	var nilVerifier *TestingPasskeyVerifier
	if _, err := nilVerifier.VerifyRegistration(context.Background(), coreauth.RegistrationVerificationInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("nil VerifyRegistration() error = %v", err)
	}
	verifier := &TestingPasskeyVerifier{}
	if _, err := verifier.CreateRegistrationOptions(context.Background(), coreauth.RegistrationOptionsInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("CreateRegistrationOptions() error = %v", err)
	}
	if _, err := verifier.VerifyRegistration(context.Background(), coreauth.RegistrationVerificationInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("VerifyRegistration() error = %v", err)
	}
	if _, err := verifier.CreateAuthenticationOptions(context.Background(), coreauth.AuthenticationOptionsInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("CreateAuthenticationOptions() error = %v", err)
	}
	if _, err := verifier.VerifyAuthentication(context.Background(), coreauth.AuthenticationVerificationInput{}); !errors.Is(err, coreauth.ErrVerifierUnavailable) {
		t.Fatalf("VerifyAuthentication() error = %v", err)
	}
}

func TestTestingPasskeyVerifierDelegatesWithoutPretendingToVerify(t *testing.T) {
	t.Parallel()
	registrationInput := coreauth.RegistrationOptionsInput{Challenge: "registration-challenge"}
	authenticationInput := coreauth.AuthenticationVerificationInput{Challenge: "authentication-challenge"}
	wantRegistrationOptions := coreauth.PasskeyVerifierOptions{
		PublicKey: json.RawMessage(`{"publicKey":{"challenge":"registration-challenge"}}`),
		Session:   json.RawMessage(`{"session":"registration"}`),
	}
	wantAuthenticationResult := coreauth.AuthenticationVerification{SignCount: 42}

	verifier := &TestingPasskeyVerifier{
		CreateRegistrationOptionsFunc: func(_ context.Context, input coreauth.RegistrationOptionsInput) (coreauth.PasskeyVerifierOptions, error) {
			if !reflect.DeepEqual(input, registrationInput) {
				t.Fatalf("registration input = %+v", input)
			}
			return wantRegistrationOptions, nil
		},
		VerifyAuthenticationFunc: func(_ context.Context, input coreauth.AuthenticationVerificationInput) (coreauth.AuthenticationVerification, error) {
			if !reflect.DeepEqual(input, authenticationInput) {
				t.Fatalf("authentication input = %+v", input)
			}
			return wantAuthenticationResult, nil
		},
	}

	options, err := verifier.CreateRegistrationOptions(context.Background(), registrationInput)
	if err != nil || !reflect.DeepEqual(options, wantRegistrationOptions) {
		t.Fatalf("CreateRegistrationOptions() = %+v, %v", options, err)
	}
	result, err := verifier.VerifyAuthentication(context.Background(), authenticationInput)
	if err != nil || !reflect.DeepEqual(result, wantAuthenticationResult) {
		t.Fatalf("VerifyAuthentication() = %+v, %v", result, err)
	}
}
