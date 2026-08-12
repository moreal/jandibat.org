package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestPasskeyLabelValidationMapsToStableBadRequest(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/register/finish", nil)
	response := httptest.NewRecorder()
	server := &Server{}

	server.writeError(response, request, fmt.Errorf("%w: passkey label must be at most 100 characters", auth.ErrInvalidInput))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	var body problem
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Code != "invalid_request" || body.Status != http.StatusBadRequest {
		t.Fatalf("problem = %+v", body)
	}
}

func TestPasskeyCloneRejectionRequiresMagicLinkReauthentication(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", nil)
	response := httptest.NewRecorder()
	server := &Server{}

	server.writeError(response, request, fmt.Errorf("%w: %w", auth.ErrInvalidSignCount, auth.ErrMagicLinkReauthenticationRequired))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	var body problem
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Code != "magic_link_reauthentication_required" || body.Title != "Reauthentication Required" ||
		body.Detail != "Magic-link reauthentication is required before continuing." {
		t.Fatalf("problem = %+v", body)
	}
}
