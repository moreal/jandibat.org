package processruntime

import (
	"fmt"
	"net/url"
	"strings"

	authadapter "github.com/moreal/jandibat.org/apps/api/internal/adapters/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/config"
)

// BuildMagicLinkMailer constructs the worker-owned SMTP side effect. The API
// process never calls this in production and cannot receive SMTP credentials.
func BuildMagicLinkMailer(settings config.Config) (auth.Mailer, error) {
	if settings.Environment == config.EnvironmentDevelopment && allBlankRuntime(
		settings.SMTPAddress, settings.SMTPUsername, settings.SMTPPassword, settings.SMTPFrom,
	) {
		return auth.NewMemoryMailer(), nil
	}
	magicLinkURL := cloneRuntimeURL(settings.WebURL)
	magicLinkURL.Fragment = "auth"
	mailer, err := authadapter.NewSMTPMailer(authadapter.SMTPConfig{
		Address: settings.SMTPAddress, Username: settings.SMTPUsername, Password: settings.SMTPPassword,
		From: settings.SMTPFrom, MagicLinkURL: magicLinkURL, AllowedRedirectURIs: MagicLinkAllowedRedirects(settings.WebURL),
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: construct SMTP mailer: %w", err)
	}
	return mailer, nil
}

func MagicLinkAllowedRedirects(webURL *url.URL) []string {
	base := cloneRuntimeURL(webURL)
	base.RawQuery = ""
	base.Fragment = ""
	plain := base.String()
	magic := cloneRuntimeURL(base)
	if magic.Path == "" {
		magic.Path = "/"
	}
	query := magic.Query()
	query.Set("auth", "magic")
	magic.RawQuery = query.Encode()
	magic.Fragment = "auth"
	connections := cloneRuntimeURL(base)
	if connections.Path == "" {
		connections.Path = "/"
	}
	connections.Fragment = "connections"
	providers := cloneRuntimeURL(base)
	providers.Path = strings.TrimRight(providers.Path, "/") + "/settings/providers"
	seen := map[string]struct{}{}
	result := make([]string, 0, 4)
	for _, value := range []string{plain, magic.String(), connections.String(), providers.String()} {
		if _, exists := seen[value]; value == "" || exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneRuntimeURL(value *url.URL) *url.URL {
	if value == nil {
		return &url.URL{}
	}
	cloned := *value
	return &cloned
}

func allBlankRuntime(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
