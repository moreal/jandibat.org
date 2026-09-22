// Package fixedhttp builds HTTP clients whose outbound connections are pinned
// to a small, construction-time set of endpoint origins.
package fixedhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 10 * time.Second

// AddressResolver is the DNS boundary used immediately before a fixed-endpoint
// connection is opened. net.DefaultResolver implements this interface.
type AddressResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// ContextDialer opens a connection to an already-resolved IP address.
// *net.Dialer implements this interface.
type ContextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Network contains injectable boundaries for deterministic resolution and
// dialing tests. Zero values select net.DefaultResolver and a bounded dialer.
type Network struct {
	Resolver AddressResolver
	Dialer   ContextDialer
}

// TransportWrapper lets a passive transport decorator retain its behavior
// while fixedhttp replaces its underlying transport with the policy-enforcing
// one. Implementations must delegate every request to the supplied transport.
type TransportWrapper interface {
	ProviderBaseTransport() http.RoundTripper
	WrapProviderTransport(http.RoundTripper) http.RoundTripper
}

var (
	ErrInvalidEndpoint       = errors.New("fixed HTTP client: invalid endpoint")
	ErrUnsupportedTransport  = errors.New("fixed HTTP client: unsupported transport")
	ErrDialTargetNotAllowed  = errors.New("fixed HTTP client: dial target not allowed")
	ErrUnsafeResolvedAddress = errors.New("fixed HTTP client: unsafe resolved address")
)

type target struct {
	host                 string
	port                 string
	allowLoopbackFixture bool
}

// NewClient clones base and restricts it to the origins of endpoints. Each
// connection resolves the original hostname, rejects the complete DNS answer
// if it contains a non-public address, and dials an accepted IP directly.
// Redirects are always refused. Plain HTTP and loopback targets are accepted
// only for explicit local fixtures using a supplied base client.
func NewClient(base *http.Client, endpoints []string, network Network, maximumTimeout time.Duration, redirectError error) (*http.Client, error) {
	if len(endpoints) == 0 {
		return nil, ErrInvalidEndpoint
	}
	suppliedClient := base != nil
	if base == nil {
		base = &http.Client{}
	}
	clone := *base
	if maximumTimeout <= 0 {
		maximumTimeout = defaultTimeout
	}
	if clone.Timeout <= 0 || clone.Timeout > maximumTimeout {
		clone.Timeout = maximumTimeout
	}
	if redirectError == nil {
		redirectError = http.ErrUseLastResponse
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return redirectError }

	targets := make(map[string]target, len(endpoints))
	origins := make(map[string]struct{}, len(endpoints))
	for _, raw := range endpoints {
		parsed, err := url.Parse(raw)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidEndpoint, raw)
		}
		host := strings.ToLower(parsed.Hostname())
		port, ok := endpointPort(parsed)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrInvalidEndpoint, raw)
		}
		loopbackFixture := suppliedClient && isLoopbackHost(host)
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackFixture) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidEndpoint, raw)
		}
		key := targetKey(host, port)
		targets[key] = target{host: host, port: port, allowLoopbackFixture: loopbackFixture}
		origins[originKey(parsed.Scheme, host, port)] = struct{}{}
	}

	transport, err := secureTransport(clone.Transport, targets, network, 0)
	if err != nil {
		return nil, err
	}
	clone.Transport = &originTransport{next: transport, allowed: origins}
	return &clone, nil
}

func endpointPort(endpoint *url.URL) (string, bool) {
	if port := endpoint.Port(); port != "" {
		return port, true
	}
	switch endpoint.Scheme {
	case "http":
		return "80", true
	case "https":
		return "443", true
	default:
		return "", false
	}
}

func targetKey(host, port string) string { return strings.ToLower(host) + "\x00" + port }
func originKey(scheme, host, port string) string {
	return strings.ToLower(scheme) + "\x00" + targetKey(host, port)
}

type originTransport struct {
	next    http.RoundTripper
	allowed map[string]struct{}
}

func (transport *originTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.User != nil || request.URL.Opaque != "" {
		return nil, ErrDialTargetNotAllowed
	}
	port, ok := endpointPort(request.URL)
	if !ok {
		return nil, ErrDialTargetNotAllowed
	}
	key := originKey(request.URL.Scheme, request.URL.Hostname(), port)
	if _, allowed := transport.allowed[key]; !allowed {
		return nil, ErrDialTargetNotAllowed
	}
	return transport.next.RoundTrip(request)
}

func secureTransport(base http.RoundTripper, targets map[string]target, network Network, depth int) (http.RoundTripper, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	if wrapper, ok := base.(TransportWrapper); ok {
		if depth >= 8 {
			return nil, fmt.Errorf("%w: too many wrappers", ErrUnsupportedTransport)
		}
		secured, err := secureTransport(wrapper.ProviderBaseTransport(), targets, network, depth+1)
		if err != nil {
			return nil, err
		}
		return wrapper.WrapProviderTransport(secured), nil
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, ErrUnsupportedTransport
	}
	clone := transport.Clone()
	clone.Proxy = nil
	pinnedDialer := newDialer(targets, network)
	clone.DialContext = pinnedDialer.DialContext
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	clone.TLSClientConfig.InsecureSkipVerify = false
	// Leave ServerName empty so crypto/tls derives and verifies the exact host
	// from each request in a client that permits more than one fixed origin.
	clone.TLSClientConfig.ServerName = ""
	clone.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := pinnedDialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			_ = connection.Close()
			return nil, ErrDialTargetNotAllowed
		}
		config := clone.TLSClientConfig.Clone()
		config.ServerName = host
		secured := tls.Client(connection, config)
		if err := secured.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return secured, nil
	}
	return clone, nil
}

// These ranges are globally non-routable or are reserved for local,
// documentation, benchmarking, protocol, and cloud-instance metadata use.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type dialer struct {
	targets  map[string]target
	resolver AddressResolver
	dialer   ContextDialer
}

func newDialer(targets map[string]target, network Network) *dialer {
	resolver := network.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	connectionDialer := network.Dialer
	if connectionDialer == nil {
		connectionDialer = &net.Dialer{Timeout: defaultTimeout, KeepAlive: 30 * time.Second}
	}
	return &dialer{targets: targets, resolver: resolver, dialer: connectionDialer}
}

func (dialer *dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrDialTargetNotAllowed
	}
	target, allowed := dialer.targets[targetKey(host, port)]
	if !allowed || strings.ToLower(host) != target.host || port != target.port {
		return nil, ErrDialTargetNotAllowed
	}
	lookupNetwork, err := lookupNetwork(network)
	if err != nil {
		return nil, err
	}
	addresses, err := dialer.resolve(ctx, lookupNetwork, host)
	if err != nil {
		return nil, fmt.Errorf("fixed HTTP client: resolve endpoint: %w", err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("fixed HTTP client: resolve endpoint: no addresses")
	}
	for _, resolved := range addresses {
		if unsafeAddress(resolved, target.allowLoopbackFixture) {
			return nil, ErrUnsafeResolvedAddress
		}
	}

	var dialErrors []error
	for _, resolved := range addresses {
		resolved = resolved.Unmap()
		if !matchesNetwork(resolved, lookupNetwork) {
			continue
		}
		connection, dialErr := dialer.dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		dialErrors = append(dialErrors, dialErr)
	}
	if len(dialErrors) == 0 {
		return nil, fmt.Errorf("fixed HTTP client: resolve endpoint: no addresses for %s", lookupNetwork)
	}
	return nil, fmt.Errorf("fixed HTTP client: connect endpoint: %w", errors.Join(dialErrors...))
}

func (dialer *dialer) resolve(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address}, nil
	}
	return dialer.resolver.LookupNetIP(ctx, network, host)
}

func lookupNetwork(network string) (string, error) {
	switch network {
	case "tcp":
		return "ip", nil
	case "tcp4":
		return "ip4", nil
	case "tcp6":
		return "ip6", nil
	default:
		return "", ErrDialTargetNotAllowed
	}
}

func matchesNetwork(address netip.Addr, network string) bool {
	switch network {
	case "ip4":
		return address.Is4()
	case "ip6":
		return address.Is6()
	default:
		return true
	}
}

func unsafeAddress(address netip.Addr, allowLoopbackFixture bool) bool {
	if !address.IsValid() || address.Zone() != "" {
		return true
	}
	address = address.Unmap()
	if address.IsLoopback() {
		return !allowLoopbackFixture
	}
	if address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || address.IsUnspecified() || !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
