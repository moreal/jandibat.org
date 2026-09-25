package metricsproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureToken = "0123456789abcdef0123456789abcdef"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyAllowsOnlyAuthenticatedMetricsAndDropsCallerHeaders(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "http://127.0.0.1:8080/metrics" || r.Method != http.MethodGet || len(r.Header) != 0 {
			t.Errorf("upstream request = %s %s headers=%v", r.Method, r.URL, r.Header)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("fixture_metric 1\n"))}, nil
	})}
	h, err := NewHandler(Config{Source: "api", Token: []byte(fixtureToken)}, client)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	r.Header.Set("X-Request-ID", "caller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "fixture_metric 1\n" || calls.Load() != 1 {
		t.Fatalf("response=%d %q calls=%d", w.Code, w.Body.String(), calls.Load())
	}

	for _, tc := range []struct {
		name, method, path, auth, header string
		want                             int
	}{
		{"method", "POST", "/metrics", "Bearer " + fixtureToken, "", 405},
		{"root", "GET", "/", "Bearer " + fixtureToken, "", 404},
		{"extra", "GET", "/metrics/extra", "Bearer " + fixtureToken, "", 404},
		{"query", "GET", "/metrics?probe=1", "Bearer " + fixtureToken, "", 404},
		{"absent", "GET", "/metrics", "", "", 403},
		{"wrong", "GET", "/metrics", "Bearer wrong", "", 403},
		{"forwarded", "GET", "/metrics", "Bearer " + fixtureToken, "Forwarded", 403},
		{"xff", "GET", "/metrics", "Bearer " + fixtureToken, "x-forwarded-for", 403},
		{"xhost", "GET", "/metrics", "Bearer " + fixtureToken, "X-Forwarded-Host", 403},
		{"realip", "GET", "/metrics", "Bearer " + fixtureToken, "x-real-ip", 403},
		{"empty forwarded", "GET", "/metrics", "Bearer " + fixtureToken, "Forwarded", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if tc.header != "" {
				value := "spoof"
				if tc.name == "empty forwarded" {
					value = ""
				}
				r.Header.Set(tc.header, value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || calls.Load() != before {
				t.Fatalf("status=%d calls=%d before=%d", w.Code, calls.Load(), before)
			}
			if tc.want == 405 && w.Header().Get("Allow") != "GET" {
				t.Fatalf("Allow=%q", w.Header().Get("Allow"))
			}
		})
	}
}

func TestProxyRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []Config{{Source: "unknown", Token: []byte(fixtureToken)}, {Source: "api", Token: []byte("short")}, {Source: "cockroach", Token: []byte(fixtureToken)}} {
		if _, err := NewHandler(cfg, nil); err == nil {
			t.Fatalf("accepted %#v", cfg.Source)
		}
	}
	for _, host := range []string{"https://db.local", "db.local:8080", "db.local/path", "db.local?x=1", "db.local%2fother", "db..local"} {
		cfg := Config{Source: "cockroach", Token: []byte(fixtureToken), CockroachHost: host, CockroachCAFile: "ca.pem", CockroachServerName: "db.local"}
		if _, err := NewHandler(cfg, &http.Client{}); err == nil {
			t.Errorf("accepted invalid host %q", host)
		}
	}
}

func TestProxyRejectsUnsafeUpstreamResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport roundTripFunc
		want      int
	}{
		{"redirect", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader("upstream-secret"))}, nil
		}, 502},
		{"error status", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("upstream-secret"))}, nil
		}, 502},
		{"oversize", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 8<<20+1)))}, nil
		}, 502},
		{"timeout", func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }, 504},
		{"transport error", func(*http.Request) (*http.Response, error) { return nil, errors.New("upstream-secret") }, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewHandler(Config{Source: "api", Token: []byte(fixtureToken)}, &http.Client{Transport: tc.transport, Timeout: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/metrics", nil)
			r.Header.Set("Authorization", "Bearer "+fixtureToken)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "upstream-secret") || strings.Contains(w.Body.String(), fixtureToken) {
				t.Fatalf("response=%d %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyRespectsCanceledContext(t *testing.T) {
	h, _ := NewHandler(Config{Source: "api", Token: []byte(fixtureToken)}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/metrics", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 504 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestProxyNeverFollowsRedirect(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/secret"}}, Body: io.NopCloser(strings.NewReader("secret"))}, nil
	})}
	h, err := NewHandler(Config{Source: "api", Token: []byte(fixtureToken)}, client)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 502 || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestCockroachClientVerifiesCAAndServerName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_status/vars" || r.Header.Get("Authorization") != "" {
			t.Errorf("unsafe upstream request: %s headers=%v", r.URL, r.Header)
		}
		_, _ = io.WriteString(w, "cockroach_metric 1\n")
	}))
	defer server.Close()
	cert := server.Certificate()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	serverName := cert.DNSNames[0]
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(99), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}, &x509.Certificate{SerialNumber: big.NewInt(99), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	wrongCAFile := filepath.Join(t.TempDir(), "wrong-ca.pem")
	if err := os.WriteFile(wrongCAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: unrelated}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ca, serverName string
		want                 int
	}{
		{"trusted", caFile, serverName, 200},
		{"wrong CA", wrongCAFile, serverName, 502},
		{"wrong server name", caFile, "wrong.example", 502},
		{"missing CA", filepath.Join(t.TempDir(), "missing.pem"), serverName, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Source: "cockroach", Token: []byte(fixtureToken), CockroachHost: "db.internal", CockroachCAFile: tc.ca, CockroachServerName: tc.serverName}
			client, err := NewClient(cfg)
			if tc.want == -1 {
				if err == nil {
					t.Fatal("accepted missing CA")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			transport := client.Transport.(*http.Transport)
			transport.DialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", server.Listener.Addr().String())
			}
			h, err := NewHandler(cfg, client)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/metrics", nil)
			r.Header.Set("Authorization", "Bearer "+fixtureToken)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
}
