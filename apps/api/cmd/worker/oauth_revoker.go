package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/oauth"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
)

var errUnsupportedOAuthRevocationProvider = errors.New("worker: unsupported OAuth revocation provider")

type workerOAuthRevoker struct {
	revokers  map[string]oauth.TokenRevoker
	localOnly map[string]struct{}
}

func (registry *workerOAuthRevoker) RevokeOAuthToken(ctx context.Context, providerID string, token []byte) error {
	if registry == nil {
		return errUnsupportedOAuthRevocationProvider
	}
	revoker := registry.revokers[providerID]
	if revoker == nil {
		if _, ok := registry.localOnly[providerID]; ok {
			// Codeberg has no compatible official remote revoke contract.
			return nil
		}
		// Never acknowledge and delete a durable queue job merely because a
		// corrupt or newly introduced provider has no registered adapter.
		return errUnsupportedOAuthRevocationProvider
	}
	return revoker.RevokeToken(ctx, token)
}

func buildWorkerOAuthRevoker(settings config.Config, client *http.Client) (*workerOAuthRevoker, error) {
	if client == nil || settings.PublicURL == nil {
		return nil, fmt.Errorf("worker: OAuth revoker requires HTTP client and public URL")
	}
	if settings.Environment == config.EnvironmentProduction && (strings.TrimSpace(settings.GitHubClientID) == "" || strings.TrimSpace(settings.GitHubClientSecret) == "" || strings.TrimSpace(settings.GitLabClientID) == "" || strings.TrimSpace(settings.GitLabClientSecret) == "") {
		return nil, fmt.Errorf("worker: %w: GitHub and GitLab OAuth credentials are required for durable revocation", config.ErrMissingSecret)
	}
	stateStore := oauth.NewMemoryStateStore(nil)
	type definition struct {
		id, clientID, clientSecret string
		construct                  func(oauth.Config) (*oauth.Adapter, error)
	}
	definitions := []definition{
		{id: "github", clientID: settings.GitHubClientID, clientSecret: settings.GitHubClientSecret, construct: oauth.NewGitHub},
		{id: "gitlab", clientID: settings.GitLabClientID, clientSecret: settings.GitLabClientSecret, construct: oauth.NewGitLab},
	}
	registry := &workerOAuthRevoker{
		revokers:  make(map[string]oauth.TokenRevoker, len(definitions)),
		localOnly: map[string]struct{}{"codeberg": {}},
	}
	for _, item := range definitions {
		if strings.TrimSpace(item.clientID) == "" && strings.TrimSpace(item.clientSecret) == "" {
			continue
		}
		redirect := *settings.PublicURL
		redirect.Path = strings.TrimRight(redirect.Path, "/") + "/v1/integrations/" + item.id + "/callback"
		redirect.RawQuery, redirect.Fragment = "", ""
		adapter, err := item.construct(oauth.Config{
			ClientID: item.clientID, ClientSecret: item.clientSecret, RedirectURI: redirect.String(),
			HTTPClient: client, StateStore: stateStore, RequireSessionBinding: true,
		})
		if err != nil {
			return nil, fmt.Errorf("worker: construct %s OAuth revoker: %w", item.id, err)
		}
		registry.revokers[item.id] = adapter
	}
	return registry, nil
}
