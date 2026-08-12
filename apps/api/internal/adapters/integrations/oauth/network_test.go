package oauth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fixedhttp"
)

type oauthResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (resolve oauthResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return resolve(ctx, network, host)
}

type oauthDialerFunc func(context.Context, string, string) (net.Conn, error)

func (dial oauthDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return dial(ctx, network, address)
}

func TestOAuthServerEndpointsRejectUnsafeResolvedAddresses(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		address  string
	}{
		{name: "token private", endpoint: "https://github.com/login/oauth/access_token", address: "10.0.0.9"},
		{name: "identity loopback", endpoint: "https://api.github.com/user", address: "127.0.0.1"},
		{name: "revocation IPv4-mapped loopback", endpoint: "https://api.github.com/applications/client-id/token", address: "::ffff:127.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dialed := false
			adapter, err := NewGitHub(Config{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				RedirectURI:  "https://app.example.test/callback",
				Network: HTTPNetwork{
					Resolver: oauthResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
						return []netip.Addr{netip.MustParseAddr(test.address)}, nil
					}),
					Dialer: oauthDialerFunc(func(context.Context, string, string) (net.Conn, error) {
						dialed = true
						return nil, errors.New("unexpected dial")
					}),
				},
			})
			if err != nil {
				t.Fatalf("NewGitHub: %v", err)
			}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, test.endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.http.Do(request)
			if !errors.Is(err, fixedhttp.ErrUnsafeResolvedAddress) || dialed {
				t.Fatalf("error=%v dialed=%v", err, dialed)
			}
		})
	}
}

func TestOAuthClientRejectsOriginsOutsideServerEndpointSet(t *testing.T) {
	adapter, err := NewGitHub(Config{
		ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURI: "https://app.example.test/callback",
	})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://attacker.example/collect", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.http.Do(request)
	if !errors.Is(err, fixedhttp.ErrDialTargetNotAllowed) {
		t.Fatalf("error=%v", err)
	}
}
