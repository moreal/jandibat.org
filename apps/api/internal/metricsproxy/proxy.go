package metricsproxy

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxBody = 8 << 20

// Config contains only the settings needed by the standalone scrape proxy.
type Config struct {
	Source              string
	Token               []byte
	CockroachHost       string
	CockroachCAFile     string
	CockroachServerName string
}

func (c Config) upstream() (string, error) {
	switch c.Source {
	case "api":
		return "http://127.0.0.1:8080/metrics", nil
	case "cockroach":
		if !validDNSName(c.CockroachHost) || !validDNSName(c.CockroachServerName) || c.CockroachCAFile == "" {
			return "", errors.New("invalid cockroach metrics configuration")
		}
		return "https://" + c.CockroachHost + ":8080/_status/vars", nil
	default:
		return "", errors.New("invalid metrics source")
	}
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			b := label[i]
			letterOrDigit := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
			if !letterOrDigit && !(b == '-' && i > 0 && i < len(label)-1) {
				return false
			}
		}
	}
	return true
}

func validToken(token []byte) bool {
	if len(token) < 32 {
		return false
	}
	for _, b := range token {
		if b <= ' ' || b >= 0x7f {
			return false
		}
	}
	return true
}

// NewClient creates a verified, bounded upstream client. Redirects are always rejected.
func NewClient(c Config) (*http.Client, error) {
	if _, err := c.upstream(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if c.Source == "cockroach" {
		pem, err := os.ReadFile(c.CockroachCAFile)
		if err != nil {
			return nil, errors.New("read cockroach metrics CA failed")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid cockroach metrics CA")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: c.CockroachServerName, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// NewHandler exposes only an authenticated metrics scrape of the fixed source.
func NewHandler(c Config, client *http.Client) (http.Handler, error) {
	upstream, err := c.upstream()
	if err != nil {
		return nil, err
	}
	if !validToken(c.Token) {
		return nil, errors.New("invalid metrics token")
	}
	if client == nil {
		client, err = NewClient(c)
		if err != nil {
			return nil, err
		}
	}
	ownedClient := *client
	ownedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if ownedClient.Timeout == 0 || ownedClient.Timeout > 5*time.Second {
		ownedClient.Timeout = 5 * time.Second
	}
	// Own the token bytes so a caller cannot mutate the configured credential.
	token := append([]byte(nil), c.Token...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/metrics" || r.URL.RawQuery != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		for name := range r.Header {
			lower := strings.ToLower(name)
			if lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		auth := r.Header.Values("Authorization")
		if len(auth) != 1 || len(auth[0]) != len("Bearer ")+len(token) || !strings.HasPrefix(auth[0], "Bearer ") ||
			subtle.ConstantTimeCompare([]byte(auth[0][len("Bearer "):]), token) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		resp, err := ownedClient.Do(upstreamReq)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			} else {
				http.Error(w, "bad gateway", http.StatusBadGateway)
			}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			} else {
				http.Error(w, "bad gateway", http.StatusBadGateway)
			}
			return
		}
		if len(body) > maxBody {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}), nil
}
