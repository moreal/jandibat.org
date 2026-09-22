package fixedhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (resolve resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return resolve(ctx, network, host)
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (dial dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return dial(ctx, network, address)
}

func TestDialerResolvesAndDialsOnlyResolvedPublicIP(t *testing.T) {
	var resolvedNetwork, resolvedHost string
	var dialedNetwork, dialedAddress string
	resolver := resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		resolvedNetwork, resolvedHost = network, host
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	dial := dialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		dialedNetwork, dialedAddress = network, address
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	})
	policy := newDialer(map[string]target{
		targetKey("api.github.com", "443"): {host: "api.github.com", port: "443"},
	}, Network{Resolver: resolver, Dialer: dial})

	connection, err := policy.DialContext(context.Background(), "tcp", "api.github.com:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = connection.Close()
	if resolvedNetwork != "ip" || resolvedHost != "api.github.com" {
		t.Fatalf("resolved network=%q host=%q", resolvedNetwork, resolvedHost)
	}
	if dialedNetwork != "tcp" || dialedAddress != "8.8.8.8:443" {
		t.Fatalf("dialed network=%q address=%q", dialedNetwork, dialedAddress)
	}
}

func TestDialerRejectsUnsafeResolvedAddresses(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{name: "loopback IPv4", address: "127.0.0.1"},
		{name: "loopback IPv6", address: "::1"},
		{name: "private IPv4", address: "10.0.0.7"},
		{name: "private IPv6", address: "fd00::7"},
		{name: "link local IPv4 metadata", address: "169.254.169.254"},
		{name: "link local IPv6", address: "fe80::1"},
		{name: "multicast IPv4", address: "224.0.0.1"},
		{name: "multicast IPv6", address: "ff02::1"},
		{name: "unspecified IPv4", address: "0.0.0.0"},
		{name: "unspecified IPv6", address: "::"},
		{name: "IPv4 mapped loopback", address: "::ffff:127.0.0.1"},
		{name: "IPv4 mapped metadata", address: "::ffff:169.254.169.254"},
		{name: "shared address space", address: "100.64.0.1"},
		{name: "Alibaba metadata", address: "100.100.100.200"},
		{name: "Azure platform metadata", address: "168.63.129.16"},
		{name: "Oracle metadata", address: "192.0.0.192"},
		{name: "benchmark range", address: "198.18.0.1"},
		{name: "documentation IPv4", address: "203.0.113.10"},
		{name: "documentation IPv6", address: "2001:db8::1"},
		{name: "reserved IPv4", address: "240.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dialed bool
			policy := newDialer(map[string]target{
				targetKey("provider.example", "443"): {host: "provider.example", port: "443"},
			}, Network{
				Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(test.address)}, nil
				}),
				Dialer: dialerFunc(func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, errors.New("unexpected dial")
				}),
			})
			_, err := policy.DialContext(context.Background(), "tcp", "provider.example:443")
			if !errors.Is(err, ErrUnsafeResolvedAddress) || dialed {
				t.Fatalf("error=%v dialed=%v", err, dialed)
			}
		})
	}
}

func TestDialerRejectsMixedDNSAnswerAndRebinding(t *testing.T) {
	var mu sync.Mutex
	lookups := 0
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		switch lookups {
		case 1:
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		case 2:
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
		default:
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
	})
	dialCount := 0
	policy := newDialer(map[string]target{
		targetKey("provider.example", "443"): {host: "provider.example", port: "443"},
	}, Network{
		Resolver: resolver,
		Dialer: dialerFunc(func(context.Context, string, string) (net.Conn, error) {
			dialCount++
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		}),
	})

	connection, err := policy.DialContext(context.Background(), "tcp", "provider.example:443")
	if err != nil {
		t.Fatalf("first DialContext: %v", err)
	}
	_ = connection.Close()
	for request := 2; request <= 3; request++ {
		if _, err := policy.DialContext(context.Background(), "tcp", "provider.example:443"); !errors.Is(err, ErrUnsafeResolvedAddress) {
			t.Fatalf("DialContext %d error=%v", request, err)
		}
	}
	if lookups != 3 || dialCount != 1 {
		t.Fatalf("lookups=%d dials=%d", lookups, dialCount)
	}
}

func TestDialerEnforcesExactHostPortAndNetwork(t *testing.T) {
	lookups := 0
	policy := newDialer(map[string]target{
		targetKey("api.github.com", "443"): {host: "api.github.com", port: "443"},
	}, Network{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})})
	for _, input := range []struct {
		network string
		address string
	}{
		{network: "tcp", address: "github.com:443"},
		{network: "tcp", address: "api.github.com:80"},
		{network: "tcp", address: "api.github.com.:443"},
		{network: "udp", address: "api.github.com:443"},
		{network: "tcp", address: "api.github.com"},
	} {
		if _, err := policy.DialContext(context.Background(), input.network, input.address); !errors.Is(err, ErrDialTargetNotAllowed) {
			t.Errorf("DialContext(%q, %q) error=%v", input.network, input.address, err)
		}
	}
	if lookups != 0 {
		t.Fatalf("resolver calls=%d", lookups)
	}
}

func TestNewClientPinsOriginsTLSAndTimeout(t *testing.T) {
	baseTransport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "attacker.example"}}
	base := &http.Client{Transport: baseTransport, Timeout: time.Minute}
	client, err := NewClient(base, []string{
		"https://github.com/login/oauth/access_token",
		"https://api.github.com/user",
	}, Network{}, 10*time.Second, errors.New("redirect refused"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client == base || client.Timeout != 10*time.Second || base.Timeout != time.Minute {
		t.Fatalf("client clone/timeout: client=%p/%s base=%p/%s", client, client.Timeout, base, base.Timeout)
	}
	guard, ok := client.Transport.(*originTransport)
	if !ok {
		t.Fatalf("outer transport=%T", client.Transport)
	}
	transport, ok := guard.next.(*http.Transport)
	if !ok {
		t.Fatalf("inner transport=%T", guard.next)
	}
	if transport == baseTransport || transport.TLSClientConfig == baseTransport.TLSClientConfig {
		t.Fatal("transport or TLS config was not cloned")
	}
	if transport.Proxy != nil || transport.DialContext == nil || transport.DialTLSContext == nil {
		t.Fatal("transport can bypass pinned direct dialing")
	}
	if connection, err := transport.DialTLSContext(context.Background(), "tcp", "attacker.example:443"); !errors.Is(err, ErrDialTargetNotAllowed) {
		if connection != nil {
			_ = connection.Close()
		}
		t.Fatalf("TLS dial to foreign origin error=%v", err)
	}
	if transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "" {
		t.Fatalf("TLS config insecure=%v serverName=%q", transport.TLSClientConfig.InsecureSkipVerify, transport.TLSClientConfig.ServerName)
	}
	request, err := http.NewRequest(http.MethodGet, "https://attacker.example/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Transport.RoundTrip(request); !errors.Is(err, ErrDialTargetNotAllowed) {
		t.Fatalf("foreign origin error=%v", err)
	}
}

func TestNewClientAllowsOnlyExplicitLoopbackFixtures(t *testing.T) {
	if _, err := NewClient(nil, []string{"http://127.0.0.1:8080/token"}, Network{}, time.Second, nil); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("default loopback client error=%v", err)
	}
	if _, err := NewClient(&http.Client{}, []string{"http://127.0.0.1:8080/token"}, Network{}, time.Second, nil); err != nil {
		t.Fatalf("explicit loopback fixture: %v", err)
	}
	if _, err := NewClient(&http.Client{}, []string{"http://provider.example/token"}, Network{}, time.Second, nil); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("non-TLS public endpoint error=%v", err)
	}
}

func TestNewClientRejectsUnenforceableRoundTripper(t *testing.T) {
	_, err := NewClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not called")
	})}, []string{"https://api.github.com/user"}, Network{}, time.Second, nil)
	if !errors.Is(err, ErrUnsupportedTransport) {
		t.Fatalf("error=%v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
