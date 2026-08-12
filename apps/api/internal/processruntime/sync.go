package processruntime

import (
	"fmt"
	"net/http"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/syncer"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func NewProviderHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func BuildSyncRegistry(client *http.Client) (*integrations.SyncRegistry, error) {
	github, err := syncer.NewGitHub(syncer.Config{HTTPClient: client, Timezone: "UTC"})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct GitHub syncer: %w", err)
	}
	gitlab, err := syncer.NewGitLab(syncer.Config{HTTPClient: client, Timezone: "UTC"})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct GitLab syncer: %w", err)
	}
	codeberg, err := syncer.NewCodeberg(syncer.Config{HTTPClient: client, Timezone: "UTC"})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct Codeberg syncer: %w", err)
	}
	registry, err := integrations.NewSyncRegistry(github, gitlab, codeberg)
	if err != nil {
		return nil, fmt.Errorf("runtime: construct sync registry: %w", err)
	}
	return registry, nil
}
