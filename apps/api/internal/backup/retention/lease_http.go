package retention

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const leaseHTTPMaxBody = 16 << 10
const leaseHTTPMaxToken = 4096
const leaseHTTPTimeout = 10 * time.Second

type KubernetesLeaseHTTPConfig struct {
	Endpoint  string
	Namespace string
	CAFile    string
	TokenFile string
	Client    *http.Client
}

// KubernetesLeaseHTTPAPI is a bounded, TLS-verified connector for the single
// precreated coordination.k8s.io/v1 Lease. It has no create/list/delete path.
type KubernetesLeaseHTTPAPI struct {
	endpoint  string
	namespace string
	tokenFile string
	client    *http.Client
	mu        sync.Mutex
	lastRaw   []byte
	lastRV    string
}

func NewKubernetesLeaseHTTPAPI(config KubernetesLeaseHTTPConfig) (*KubernetesLeaseHTTPAPI, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.Opaque != "" {
		return nil, errors.New("invalid kubernetes Lease API endpoint")
	}
	if len(config.Namespace) == 0 || len(config.Namespace) > 63 || !kubeNamespace.MatchString(config.Namespace) || config.CAFile == "" || config.TokenFile == "" {
		return nil, errors.New("invalid kubernetes Lease API configuration")
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil || len(caPEM) == 0 || len(caPEM) > 1<<20 {
		return nil, errors.New("kubernetes Lease CA unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("kubernetes Lease CA invalid")
	}
	client := http.Client{}
	if config.Client != nil {
		client = *config.Client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if client.Transport != nil {
		return nil, errors.New("kubernetes Lease custom HTTP transport refused")
	}
	// Only the service-account CA and URL hostname establish TLS trust.
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	transport.Proxy = nil
	client.Transport = transport
	if client.Timeout <= 0 || client.Timeout > leaseHTTPTimeout {
		client.Timeout = leaseHTTPTimeout
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("kubernetes Lease redirect refused") }
	return &KubernetesLeaseHTTPAPI{endpoint: strings.TrimSuffix(config.Endpoint, "/"), namespace: config.Namespace, tokenFile: config.TokenFile, client: &client}, nil
}

func (a *KubernetesLeaseHTTPAPI) resourceURL(namespace, name string) (string, error) {
	if a == nil || a.client == nil || namespace != a.namespace || name != KubernetesLeaseName {
		return "", errors.New("kubernetes Lease resource identity mismatch")
	}
	return a.endpoint + "/apis/coordination.k8s.io/v1/namespaces/" + a.namespace + "/leases/" + KubernetesLeaseName, nil
}

func (a *KubernetesLeaseHTTPAPI) bearer() (string, error) {
	f, err := os.Open(a.tokenFile)
	if err != nil {
		return "", errors.New("kubernetes Lease token unavailable")
	}
	defer f.Close()
	value, err := io.ReadAll(io.LimitReader(f, leaseHTTPMaxToken+1))
	if err != nil || len(value) == 0 || len(value) > leaseHTTPMaxToken {
		return "", errors.New("kubernetes Lease token invalid")
	}
	token := strings.TrimSpace(string(value))
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= ' ' || r == 127 }) >= 0 {
		return "", errors.New("kubernetes Lease token invalid")
	}
	return token, nil
}

func (a *KubernetesLeaseHTTPAPI) call(ctx context.Context, method, resource string, body []byte) (KubernetesLeaseRecord, error) {
	var empty KubernetesLeaseRecord
	if ctx == nil {
		return empty, errors.New("kubernetes Lease context missing")
	}
	token, err := a.bearer()
	if err != nil {
		return empty, err
	}
	req, err := http.NewRequestWithContext(ctx, method, resource, bytes.NewReader(body))
	if err != nil {
		return empty, errors.New("kubernetes Lease request invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(req)
	if err != nil {
		return empty, errors.New("kubernetes Lease API request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return empty, errors.New("kubernetes Lease API status rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, leaseHTTPMaxBody+1))
	if err != nil || len(data) == 0 || len(data) > leaseHTTPMaxBody {
		return empty, errors.New("kubernetes Lease API response invalid")
	}
	var envelope struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Namespace       string `json:"namespace"`
			Name            string `json:"name"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			HolderIdentity       string `json:"holderIdentity"`
			LeaseDurationSeconds int32  `json:"leaseDurationSeconds"`
			RenewTime            string `json:"renewTime"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.APIVersion != "coordination.k8s.io/v1" || envelope.Kind != "Lease" || envelope.Metadata.Namespace != a.namespace || envelope.Metadata.Name != KubernetesLeaseName || envelope.Metadata.ResourceVersion == "" {
		return empty, errors.New("kubernetes Lease API object invalid")
	}
	record := KubernetesLeaseRecord{Namespace: envelope.Metadata.Namespace, Name: envelope.Metadata.Name, ResourceVersion: envelope.Metadata.ResourceVersion, HolderIdentity: envelope.Spec.HolderIdentity, LeaseDurationSeconds: envelope.Spec.LeaseDurationSeconds}
	if envelope.Spec.RenewTime != "" {
		parsed, err := time.Parse(time.RFC3339Nano, envelope.Spec.RenewTime)
		if err != nil {
			return empty, errors.New("kubernetes Lease API time invalid")
		}
		record.RenewTime = parsed.UTC()
	}
	if method == http.MethodGet {
		a.mu.Lock()
		a.lastRaw = append(a.lastRaw[:0], data...)
		a.lastRV = record.ResourceVersion
		a.mu.Unlock()
	}
	return record, nil
}

func (a *KubernetesLeaseHTTPAPI) Get(ctx context.Context, namespace, name string) (KubernetesLeaseRecord, error) {
	resource, err := a.resourceURL(namespace, name)
	if err != nil {
		return KubernetesLeaseRecord{}, err
	}
	return a.call(ctx, http.MethodGet, resource, nil)
}

func (a *KubernetesLeaseHTTPAPI) Update(ctx context.Context, record KubernetesLeaseRecord) (KubernetesLeaseRecord, error) {
	resource, err := a.resourceURL(record.Namespace, record.Name)
	if err != nil {
		return KubernetesLeaseRecord{}, err
	}
	if record.ResourceVersion == "" || record.LeaseDurationSeconds != 30 || record.RenewTime.IsZero() || record.RenewTime.Nanosecond()%1000 != 0 {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease CAS input invalid")
	}
	a.mu.Lock()
	if a.lastRV != record.ResourceVersion || len(a.lastRaw) == 0 {
		a.mu.Unlock()
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease CAS snapshot unavailable")
	}
	snapshot := append([]byte(nil), a.lastRaw...)
	a.lastRaw = nil
	a.lastRV = ""
	a.mu.Unlock()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &object); err != nil {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease CAS snapshot invalid")
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(object["metadata"], &metadata); err != nil || metadata == nil {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease CAS metadata invalid")
	}
	var spec map[string]json.RawMessage
	if raw := object["spec"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &spec); err != nil {
			return KubernetesLeaseRecord{}, errors.New("kubernetes Lease CAS spec invalid")
		}
	}
	if spec == nil {
		spec = map[string]json.RawMessage{}
	}
	metadata["resourceVersion"], _ = json.Marshal(record.ResourceVersion)
	spec["holderIdentity"], _ = json.Marshal(record.HolderIdentity)
	spec["leaseDurationSeconds"], _ = json.Marshal(record.LeaseDurationSeconds)
	spec["renewTime"], _ = json.Marshal(record.RenewTime.UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
	object["metadata"], _ = json.Marshal(metadata)
	object["spec"], _ = json.Marshal(spec)
	body, err := json.Marshal(object)
	if err != nil {
		return KubernetesLeaseRecord{}, errors.New("kubernetes Lease CAS encoding failed")
	}
	return a.call(ctx, http.MethodPut, resource, body)
}
