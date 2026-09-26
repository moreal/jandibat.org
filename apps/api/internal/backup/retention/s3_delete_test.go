package retention

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Removing VersionId or changing the approved scope must make these tests fail.
func TestS3DeleteRemovesOnlyExactVersionInScopedBucket(t *testing.T) {
	versions := map[string]bool{"old": true, "new": true}
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.EscapedPath() != "/bucket/k/object" || r.URL.Query().Get("versionId") != "old" || r.URL.Query().Get("x-id") != "DeleteObject" || len(r.URL.Query()) != 2 || !strings.Contains(r.Header.Get("Authorization"), "Credential=delete-only/") {
			t.Errorf("unexpected deletion request: method=%s path=%s query=%s", r.Method, r.URL.EscapedPath(), r.URL.RawQuery)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !versions["old"] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(versions, "old")
		w.Header().Set("x-amz-version-id", "old")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewS3DeleteClient(S3DeleteConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "delete-only", SecretAccessKey: "fake-secret", Namespace: StorageNamespace{Bucket: "bucket", Prefix: "k/"}, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteVersion(context.Background(), TargetVersion{Bucket: "bucket", Key: "k/object", VersionID: "old"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || versions["old"] || !versions["new"] {
		t.Fatalf("unexpected state: calls=%d versions=%v", calls, versions)
	}
}

func TestExecuteWithS3DeleteRequiresPostDeleteInventoryProof(t *testing.T) {
	for _, mutate := range []struct {
		name   string
		remove bool
	}{{"removed", true}, {"provider acknowledged without removal", false}} {
		t.Run(mutate.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			var mu sync.Mutex
			present := make(map[string]bool)
			for _, v := range f.inventory.Pages[0].Versions {
				present[v.Key+"\x00"+v.VersionID] = true
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.EscapedPath(), "/backup-prod/backups/") || r.URL.Query().Get("versionId") == "" || !strings.Contains(r.Header.Get("Authorization"), "Credential=delete-only/") {
					t.Errorf("unscoped request: %s %s", r.Method, r.URL.EscapedPath())
					w.WriteHeader(http.StatusForbidden)
					return
				}
				key := strings.TrimPrefix(r.URL.Path, "/backup-prod/")
				identity := key + "\x00" + r.URL.Query().Get("versionId")
				mu.Lock()
				defer mu.Unlock()
				if !present[identity] {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if mutate.remove {
					delete(present, identity)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client, err := NewS3DeleteClient(S3DeleteConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "delete-only", SecretAccessKey: "fake-secret", Namespace: f.request.Plan.Namespace, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			f.deps.DeleteVersion = client.DeleteVersion
			f.deps.Inventory = func(context.Context) (VersionInventory, error) {
				mu.Lock()
				defer mu.Unlock()
				versions := make([]ObjectVersion, 0, len(present))
				for _, v := range f.inventory.Pages[0].Versions {
					if present[v.Key+"\x00"+v.VersionID] {
						versions = append(versions, v)
					}
				}
				return VersionInventory{Pages: []VersionPage{{Versions: versions, Complete: true}}}, nil
			}
			result, err := Execute(context.Background(), f.request, f.deps)
			if mutate.remove {
				if err != nil || len(result.Deleted) != 2 || f.lease.releases != 1 {
					t.Fatalf("completed deletion: result=%+v err=%v lease=%+v", result, err, f.lease)
				}
			} else if err == nil || f.lease.releases != 0 {
				t.Fatalf("accepted unproven deletion: result=%+v err=%v lease=%+v", result, err, f.lease)
			}
		})
	}
}

func TestS3DeleteRejectsInvalidConfigAndTargetsWithoutRequest(t *testing.T) {
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	base := S3DeleteConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "delete-only", SecretAccessKey: "fake-secret", Namespace: StorageNamespace{Bucket: "bucket", Prefix: "k/"}, HTTPClient: server.Client()}
	for _, change := range []struct {
		name   string
		mutate func(*S3DeleteConfig)
	}{
		{"missing credentials", func(c *S3DeleteConfig) { c.AccessKeyID = ""; c.SecretAccessKey = "" }},
		{"http endpoint", func(c *S3DeleteConfig) { c.Endpoint = "http://example.invalid" }},
		{"malformed scope", func(c *S3DeleteConfig) { c.Namespace.Prefix = "../" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			c := base
			change.mutate(&c)
			if _, err := NewS3DeleteClient(c); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
	client, err := NewS3DeleteClient(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []TargetVersion{
		{Bucket: "other", Key: "k/object", VersionID: "v1"},
		{Bucket: "bucket", Key: "other/object", VersionID: "v1"},
		{Bucket: "bucket", Key: "k/object", VersionID: ""},
		{Bucket: "bucket", Key: "k/object", VersionID: "null"},
		{Bucket: "bucket", Key: "k/../outside", VersionID: "v1"},
		{Bucket: "bucket", Key: "k/object", VersionID: "v1\n"},
	} {
		if err := client.DeleteVersion(context.Background(), target); err == nil {
			t.Fatalf("accepted invalid target: %+v", target)
		}
	}
	if calls != 0 {
		t.Fatalf("unsafe requests: %d", calls)
	}
}

func TestS3DeleteRedactsProviderErrorsAndBoundsRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("versionId") == "slow" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>secret-sentinel</Message></Error>`)
	}))
	defer server.Close()
	client, err := NewS3DeleteClient(S3DeleteConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "delete-only", SecretAccessKey: "fake-secret", Namespace: StorageNamespace{Bucket: "bucket", Prefix: "k/"}, HTTPClient: server.Client(), RequestTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"forbidden", "slow"} {
		err := client.DeleteVersion(context.Background(), TargetVersion{Bucket: "bucket", Key: "k/secret-key", VersionID: version})
		if err == nil || strings.Contains(err.Error(), "secret-sentinel") || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), version) {
			t.Fatalf("unredacted or absent error: %v", err)
		}
	}
}
