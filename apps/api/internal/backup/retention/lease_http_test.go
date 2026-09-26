package retention

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syntheticLeaseToken() string { return strings.Repeat("T", 37) }

func leaseHTTPFixture(t *testing.T, handler http.Handler) (*KubernetesLeaseHTTPAPI, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(caPath, cert, 0600); err != nil {
		t.Fatal(err)
	}
	// The synthetic bearer value is generated at runtime, never committed or logged.
	token := syntheticLeaseToken()
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	api, err := NewKubernetesLeaseHTTPAPI(KubernetesLeaseHTTPConfig{Endpoint: server.URL, Namespace: "jandibat", CAFile: caPath, TokenFile: tokenPath, Client: &http.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	return api, token
}

func leaseJSON(rv, holder string) []byte {
	payload := map[string]any{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"name": KubernetesLeaseName, "namespace": "jandibat", "resourceVersion": rv, "annotations": map[string]any{"kustomize.toolkit.fluxcd.io/prune": "disabled"}}, "spec": map[string]any{"holderIdentity": holder, "leaseDurationSeconds": 30, "renewTime": "2026-09-26T12:00:00.123456Z"}}
	result, _ := json.Marshal(payload)
	return result
}

func TestLeaseHTTPRejectsMissingOrWrongPruneProtection(t *testing.T) {
	for _, choice := range []string{"missing", "wrong"} {
		t.Run(choice, func(t *testing.T) {
			lease := map[string]any{}
			if err := json.Unmarshal(leaseJSON("17", ""), &lease); err != nil {
				t.Fatal(err)
			}
			metadata := lease["metadata"].(map[string]any)
			if choice == "missing" {
				delete(metadata, "annotations")
			} else {
				metadata["annotations"].(map[string]any)["kustomize.toolkit.fluxcd.io/prune"] = "enabled"
			}
			body, err := json.Marshal(lease)
			if err != nil {
				t.Fatal(err)
			}
			api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
			if _, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName); err == nil {
				t.Fatal("unprotected Lease accepted")
			}
		})
	}
}

func TestLeaseHTTPStalePutConflictIsSanitized(t *testing.T) {
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write(leaseJSON("17", ""))
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(syntheticLeaseToken()))
	}))
	if _, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName); err != nil {
		t.Fatal(err)
	}
	_, err := api.Update(context.Background(), KubernetesLeaseRecord{Namespace: "jandibat", Name: KubernetesLeaseName, ResourceVersion: "17", HolderIdentity: "retention-job", LeaseDurationSeconds: 30, RenewTime: time.Date(2026, 9, 26, 12, 0, 0, 123456000, time.UTC)})
	if err == nil || strings.Contains(err.Error(), syntheticLeaseToken()) {
		t.Fatalf("stale PUT accepted or leaked response: %v", err)
	}
}

func TestLeaseHTTPGetsOnlyExactNamespacedLeaseOverTLS(t *testing.T) {
	var calls int
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/apis/coordination.k8s.io/v1/namespaces/jandibat/leases/"+KubernetesLeaseName || r.Header.Get("Authorization") != "Bearer "+syntheticLeaseToken() || r.TLS == nil {
			t.Errorf("wrong Lease request: method=%s path=%s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(leaseJSON("17", ""))
	}))
	record, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName)
	if err != nil {
		t.Fatal(err)
	}
	if record.ResourceVersion != "17" || record.HolderIdentity != "" || record.LeaseDurationSeconds != 30 || calls != 1 {
		t.Fatalf("bad GET result: %+v calls=%d", record, calls)
	}
	if _, err := api.Get(context.Background(), "other", KubernetesLeaseName); err == nil {
		t.Fatal("foreign namespace accepted")
	}
	if _, err := api.Get(context.Background(), "jandibat", "other"); err == nil {
		t.Fatal("foreign Lease accepted")
	}
	if calls != 1 {
		t.Fatal("foreign resource reached HTTP server")
	}
}

func TestLeaseHTTPPutCarriesCASAndMicroTime(t *testing.T) {
	var observed map[string]any
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write(leaseJSON("17", ""))
			return
		}
		if r.Method != "PUT" || r.URL.Path != "/apis/coordination.k8s.io/v1/namespaces/jandibat/leases/"+KubernetesLeaseName {
			t.Errorf("wrong PUT target")
		}
		if err := json.NewDecoder(r.Body).Decode(&observed); err != nil {
			t.Error(err)
		}
		_, _ = w.Write(leaseJSON("18", "retention-job"))
	}))
	if _, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 9, 26, 12, 0, 0, 123456000, time.UTC)
	got, err := api.Update(context.Background(), KubernetesLeaseRecord{Namespace: "jandibat", Name: KubernetesLeaseName, ResourceVersion: "17", HolderIdentity: "retention-job", LeaseDurationSeconds: 30, RenewTime: date})
	if err != nil {
		t.Fatal(err)
	}
	meta := observed["metadata"].(map[string]any)
	spec := observed["spec"].(map[string]any)
	if meta["resourceVersion"] != "17" || meta["name"] != KubernetesLeaseName || spec["holderIdentity"] != "retention-job" || spec["leaseDurationSeconds"] != float64(30) || spec["renewTime"] != "2026-09-26T12:00:00.123456Z" || got.ResourceVersion != "18" {
		t.Fatalf("bad CAS request/response metadata=%v spec=%v got=%+v", meta, spec, got)
	}
}

func TestLeaseHTTPPutPreservesParentMetadataAndOtherSpecFields(t *testing.T) {
	var observed map[string]any
	initial := map[string]any{}
	if err := json.Unmarshal(leaseJSON("17", ""), &initial); err != nil {
		t.Fatal(err)
	}
	initial["metadata"].(map[string]any)["annotations"] = map[string]any{"kustomize.toolkit.fluxcd.io/prune": "disabled"}
	initial["spec"].(map[string]any)["leaseTransitions"] = float64(4)
	initialJSON, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write(initialJSON)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&observed); err != nil {
			t.Error(err)
		}
		_, _ = w.Write(leaseJSON("18", "retention-job"))
	}))
	if _, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName); err != nil {
		t.Fatal(err)
	}
	_, err = api.Update(context.Background(), KubernetesLeaseRecord{Namespace: "jandibat", Name: KubernetesLeaseName, ResourceVersion: "17", HolderIdentity: "retention-job", LeaseDurationSeconds: 30, RenewTime: time.Date(2026, 9, 26, 12, 0, 0, 123456000, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	meta := observed["metadata"].(map[string]any)
	spec := observed["spec"].(map[string]any)
	annotations, ok := meta["annotations"].(map[string]any)
	if !ok || annotations["kustomize.toolkit.fluxcd.io/prune"] != "disabled" || spec["leaseTransitions"] != float64(4) {
		t.Fatalf("PUT dropped parent-managed state: metadata=%v spec=%v", meta, spec)
	}
}

func TestLeaseHTTPRejectsStatusAndBodiesWithoutSecret(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusConflict, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(syntheticLeaseToken()))
			}))
			_, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName)
			if err == nil || strings.Contains(err.Error(), syntheticLeaseToken()) {
				t.Fatalf("status leaked or accepted: %v", err)
			}
		})
	}
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 1<<17))) }))
	if _, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestLeaseHTTPRejectsInvalidTLSAndConfiguration(t *testing.T) {
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(leaseJSON("17", "")) }))
	if _, err := NewKubernetesLeaseHTTPAPI(KubernetesLeaseHTTPConfig{Endpoint: "http://127.0.0.1:1234", Namespace: "jandibat"}); err == nil {
		t.Fatal("plain HTTP accepted")
	}
	if _, err := NewKubernetesLeaseHTTPAPI(KubernetesLeaseHTTPConfig{Endpoint: "https://user:pass@example.invalid", Namespace: "jandibat"}); err == nil {
		t.Fatal("URL credentials accepted")
	}
	api.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = x509.NewCertPool() // no trusted roots.
	if _, err := api.Get(context.Background(), "jandibat", KubernetesLeaseName); err == nil {
		t.Fatal("untrusted server certificate accepted")
	}
}

func TestLeaseHTTPRejectsInjectedTLSBypassTransport(t *testing.T) {
	api, _ := leaseHTTPFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(leaseJSON("17", "")) }))
	unsafeClient := &http.Client{Transport: &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) { return nil, nil }}}
	_, err := NewKubernetesLeaseHTTPAPI(KubernetesLeaseHTTPConfig{Endpoint: api.endpoint, Namespace: "jandibat", CAFile: filepath.Join(filepath.Dir(api.tokenFile), "ca.pem"), TokenFile: api.tokenFile, Client: unsafeClient})
	if err == nil {
		t.Fatal("custom TLS dialer can bypass projected CA verification")
	}
}
