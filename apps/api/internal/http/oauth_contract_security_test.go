package apihttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapteroauth "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

type invalidRequestOAuthFlow struct{}

func (invalidRequestOAuthFlow) ProviderID() string { return "github" }
func (invalidRequestOAuthFlow) Begin(context.Context, adapteroauth.AuthorizationRequest) (adapteroauth.AuthorizationResult, error) {
	return adapteroauth.AuthorizationResult{}, adapteroauth.ErrInvalidRequest
}
func (invalidRequestOAuthFlow) Complete(context.Context, adapteroauth.CallbackRequest) (adapteroauth.CallbackResult, error) {
	return adapteroauth.CallbackResult{}, adapteroauth.ErrInvalidRequest
}

func TestOAuthBrowserEndpointsRejectBearerOnlyAuthentication(t *testing.T) {
	connection := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github",
		AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionPending,
	}
	flow := &recordingOAuthFlow{}
	connections := staticConnections{item: connection}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, Connections: connections, OAuthConnections: connections,
		OAuthFlows: map[string]adapteroauth.Flow{"github": flow}, OAuthWebURL: "https://app.example",
		AllowedRedirects: []string{"https://app.example/settings/providers"},
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections", strings.NewReader(`{"providerId":"github","authMethod":"oauth2"}`))
	request.Header.Set("Authorization", "Bearer api-session")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bearer-only OAuth create status = %d, want 401: %s", recorder.Code, recorder.Body.String())
	}
	if flow.begin.SessionBinding != "" {
		t.Fatalf("bearer-only OAuth create reached flow with binding %q", flow.begin.SessionBinding)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/integrations/github/callback?state=opaque&code=code", nil)
	request.Header.Set("Authorization", "Bearer api-session")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bearer-only OAuth callback status = %d, want 401: %s", recorder.Code, recorder.Body.String())
	}
	if flow.complete.State != "" {
		t.Fatalf("bearer-only callback reached OAuth flow: %#v", flow.complete)
	}
}

func TestOAuthInvalidRequestUsesRFC9457BadRequest(t *testing.T) {
	connection := integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github",
		AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionPending,
	}
	connections := staticConnections{item: connection}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, Connections: connections,
		OAuthFlows:     map[string]adapteroauth.Flow{"github": invalidRequestOAuthFlow{}},
		AllowedOrigins: []string{"https://app.example"},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/subjects/subject-1/provider-connections", strings.NewReader(`{"providerId":"github","authMethod":"oauth2"}`))
	request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "browser-session"})
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("OAuth invalid request status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}
	var body struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if body.Status != http.StatusBadRequest || body.Title != "Bad Request" || body.Code != "invalid_request" || body.Type != "https://jandibat.org/problems/invalid_request" {
		t.Fatalf("unexpected OAuth problem: %#v", body)
	}
}

func TestOAuthCallbackRejectsContractQueryBoundsBeforeExchange(t *testing.T) {
	flow := &recordingOAuthFlow{}
	connections := staticConnections{item: integrations.ProviderConnection{
		ID: "11111111-1111-4111-8111-111111111111", SubjectID: "subject-1", ProviderID: "github",
		AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionPending,
	}}
	router := apihttp.NewRouter(apihttp.Dependencies{
		Auth: fakeAuth{}, SubjectAuthorizer: allowOwner{}, OAuthConnections: connections,
		OAuthFlows: map[string]adapteroauth.Flow{"github": flow},
	})
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "short state", query: "state=short&code=code"},
		{name: "long state", query: "state=" + strings.Repeat("s", 1025)},
		{name: "long code", query: "state=" + strings.Repeat("s", 32) + "&code=" + strings.Repeat("c", 4097)},
		{name: "long error", query: "state=" + strings.Repeat("s", 32) + "&error=" + strings.Repeat("e", 257)},
		{name: "long description", query: "state=" + strings.Repeat("s", 32) + "&error_description=" + strings.Repeat("d", 1025)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/integrations/github/callback?"+test.query, nil)
			request.AddCookie(&http.Cookie{Name: "jandibat_session", Value: "browser-session"})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || flow.complete.State != "" {
				t.Fatalf("response=%d flow=%#v body=%s", response.Code, flow.complete, response.Body.String())
			}
		})
	}
}

func TestPasskeyCredentialEnvelopeRejectsNestedContractViolations(t *testing.T) {
	router := apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}})
	validPrefix := `{"ceremonyId":"11111111-1111-4111-8111-111111111111","credential":`
	properties := make([]string, 21)
	for index := range properties {
		properties[index] = `"p` + string(rune('a'+index)) + `":true`
	}
	for _, test := range []struct {
		name       string
		credential string
	}{
		{name: "unknown field", credential: `{"id":"AQ","rawId":"AQ","type":"public-key","response":{},"clientExtensionResults":{},"unknown":true}`},
		{name: "id too long", credential: `{"id":"` + strings.Repeat("i", 2049) + `","rawId":"AQ","type":"public-key","response":{},"clientExtensionResults":{}}`},
		{name: "raw id too long", credential: `{"id":"AQ","rawId":"` + strings.Repeat("A", 2049) + `","type":"public-key","response":{},"clientExtensionResults":{}}`},
		{name: "invalid type", credential: `{"id":"AQ","rawId":"AQ","type":"password","response":{},"clientExtensionResults":{}}`},
		{name: "response properties", credential: `{"id":"AQ","rawId":"AQ","type":"public-key","response":{` + strings.Join(properties, ",") + `},"clientExtensionResults":{}}`},
		{name: "extension properties", credential: `{"id":"AQ","rawId":"AQ","type":"public-key","response":{},"clientExtensionResults":{` + strings.Join(properties, ",") + `}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/passkey/sign-in/finish", strings.NewReader(validPrefix+test.credential+`}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
