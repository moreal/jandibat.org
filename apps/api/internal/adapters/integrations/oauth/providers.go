package oauth

var githubDefinition = providerDefinition{
	id: "github",
	defaultEndpoints: Endpoints{
		AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL:         "https://github.com/login/oauth/access_token",
		IdentityURL:      "https://api.github.com/user",
		RevocationURL:    "https://api.github.com/applications",
	},
	defaultScopes:   []string{"read:user"},
	revocationStyle: revocationGitHub,
}

var gitlabDefinition = providerDefinition{
	id: "gitlab",
	defaultEndpoints: Endpoints{
		AuthorizationURL: "https://gitlab.com/oauth/authorize",
		TokenURL:         "https://gitlab.com/oauth/token",
		IdentityURL:      "https://gitlab.com/api/v4/user",
		RevocationURL:    "https://gitlab.com/oauth/revoke",
	},
	defaultScopes:   []string{"read_user"},
	usernameField:   "username",
	revocationStyle: revocationOAuthForm,
}

var codebergDefinition = providerDefinition{
	id: "codeberg",
	defaultEndpoints: Endpoints{
		AuthorizationURL: "https://codeberg.org/login/oauth/authorize",
		TokenURL:         "https://codeberg.org/login/oauth/access_token",
		IdentityURL:      "https://codeberg.org/api/v1/user",
	},
	defaultScopes:   []string{"read:user"},
	revocationStyle: revocationLocalOnly,
}

func NewGitHub(config Config) (*Adapter, error) {
	return newAdapter(githubDefinition, config)
}

func NewGitLab(config Config) (*Adapter, error) {
	return newAdapter(gitlabDefinition, config)
}

func NewCodeberg(config Config) (*Adapter, error) {
	return newAdapter(codebergDefinition, config)
}
