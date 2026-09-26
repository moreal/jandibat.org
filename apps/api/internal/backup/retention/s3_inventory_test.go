package retention

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestS3InventoryRejectsRedirectBeforeSecondRequest(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, destination := range []string{"same host changed key", "http downgrade"} {
			t.Run(fmt.Sprintf("%d %s", status, destination), func(t *testing.T) {
				var original, redirected atomic.Int32
				downgrade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					redirected.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				defer downgrade.Close()
				var source *httptest.Server
				source = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/bucket" || !r.URL.Query().Has("versioning") {
						redirected.Add(1)
						w.WriteHeader(http.StatusOK)
						return
					}
					if !strings.Contains(r.Header.Get("Authorization"), "Credential=inventory-access-sentinel/") {
						t.Error("original inventory request was not signed with the explicit identity")
					}
					original.Add(1)
					location := downgrade.URL + "/bucket/k/secret-key?versioning&token=secret-url"
					if destination == "same host changed key" {
						location = source.URL + "/bucket/k/secret-key?versioning&token=secret-url"
					}
					w.Header().Set("Location", location)
					w.WriteHeader(status)
				}))
				defer source.Close()
				client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: source.URL, Region: "us-east-1", AccessKeyID: "inventory-access-sentinel", SecretAccessKey: "inventory-secret-sentinel", HTTPClient: source.Client()})
				if err != nil {
					t.Fatal("could not construct inventory client")
				}
				listing, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
				if err == nil || len(listing.Versions) != 0 || original.Load() != 1 || redirected.Load() != 0 {
					t.Fatalf("unsafe redirect handling: error=%t versions=%d original=%d redirected=%d", err != nil, len(listing.Versions), original.Load(), redirected.Load())
				}
				for _, secret := range []string{"inventory-access-sentinel", "inventory-secret-sentinel", "secret-key", "secret-url", source.URL, downgrade.URL} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("inventory error leaked credentials or URL")
					}
				}
			})
		}
	}
}

// A key-marker alone drops the second version of k/0500 at the page boundary.
func TestS3InventoryTraversesBothVersionMarkersAndChecksEachVersion(t *testing.T) {
	var listings, retentionReads, holdReads int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "AWS4-HMAC-SHA256 ") {
			t.Errorf("request was not signed: %q", got)
		}
		query := r.URL.Query()
		switch {
		case query.Has("versioning"):
			fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case query.Has("object-lock"):
			fmt.Fprint(w, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>35</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
		case query.Has("versions"):
			listings++
			if listings == 1 {
				if query.Get("key-marker") != "" || query.Get("version-id-marker") != "" {
					t.Error("first page has markers")
				}
				fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>true</IsTruncated><NextKeyMarker>k/0500</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker><Version><Key>k/0500</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest><Size>11</Size></Version></ListVersionsResult>`)
			} else {
				if query.Get("key-marker") != "k/0500" || query.Get("version-id-marker") != "v1" {
					t.Errorf("missing compound cursor: %q", r.URL.RawQuery)
				}
				fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated><Version><Key>k/0500</Key><VersionId>v0</VersionId><IsLatest>false</IsLatest><Size>9</Size></Version></ListVersionsResult>`)
			}
		case query.Has("retention"):
			retentionReads++
			if query.Get("versionId") == "" {
				t.Error("retention lacked exact version")
			}
			fmt.Fprint(w, `<Retention><Mode>GOVERNANCE</Mode><RetainUntilDate>2099-01-01T00:00:00Z</RetainUntilDate></Retention>`)
		case query.Has("legal-hold"):
			holdReads++
			if query.Get("versionId") == "" {
				t.Error("hold lacked exact version")
			}
			fmt.Fprint(w, `<LegalHold><Status>OFF</Status></LegalHold>`)
		default:
			t.Errorf("unexpected request: %s", r.URL.EscapedPath())
		}
	}))
	defer server.Close()

	client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake-access", SecretAccessKey: "fake-secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
	if err != nil {
		t.Fatal(err)
	}
	if listings != 2 || retentionReads != 2 || holdReads != 2 || len(got.Versions) != 2 {
		t.Fatalf("incomplete inventory: pages=%d retention=%d hold=%d versions=%d", listings, retentionReads, holdReads, len(got.Versions))
	}
	if got.Versions[0].VersionID != "v1" || got.Versions[1].VersionID != "v0" || !got.Versions[0].Locked {
		t.Fatalf("incorrect version/lock mapping: %+v", got)
	}
}

func TestS3InventoryRejectsAmbiguousPagesAndUncertainProtection(t *testing.T) {
	const first = `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>true</IsTruncated><NextKeyMarker>k/a</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker><Version><Key>k/a</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest><Size>1</Size></Version></ListVersionsResult>`
	const last = `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated><Version><Key>k/a</Key><VersionId>v0</VersionId><IsLatest>false</IsLatest><Size>1</Size></Version></ListVersionsResult>`
	cases := []struct {
		name, versioning, first, next, protection, want string
	}{
		{name: "suspended versioning", versioning: "Suspended", first: first, next: last, want: "versioning_not_enabled"},
		{name: "missing version marker", first: strings.Replace(first, "<NextVersionIdMarker>v1</NextVersionIdMarker>", "", 1), next: last, want: "invalid_cursor"},
		{name: "empty truncated page", first: `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>true</IsTruncated><NextKeyMarker>k/a</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker></ListVersionsResult>`, next: last, want: "invalid_page"},
		{name: "repeating cursor", first: first, next: first, want: "duplicate_version"},
		{name: "foreign prefix", first: strings.Replace(first, "<Key>k/a</Key>", "<Key>other/a</Key>", 1), next: last, want: "invalid_version"},
		{name: "null version", first: strings.Replace(first, "<VersionId>v1</VersionId>", "<VersionId>null</VersionId>", 1), next: last, want: "invalid_version"},
		{name: "no latest version", first: `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated><Version><Key>k/a</Key><VersionId>v0</VersionId><IsLatest>false</IsLatest><Size>1</Size></Version></ListVersionsResult>`, want: "invalid_version"},
		{name: "malformed XML", first: `<ListVersionsResult><Version>`, next: last, want: "list_failed"},
		{name: "retention forbidden", first: first, next: last, protection: "retention_403", want: "retention_read_failed"},
		{name: "retention missing version", first: first, next: last, protection: "retention_404", want: "retention_read_failed"},
		{name: "forbidden no-policy claim", first: first, next: last, protection: "retention_policy_403", want: "retention_read_failed"},
		{name: "legal hold forbidden", first: first, next: last, protection: "hold_403", want: "legal_hold_read_failed"},
		{name: "legal hold missing version", first: first, next: last, protection: "hold_404", want: "legal_hold_read_failed"},
		{name: "unknown retention body", first: first, next: last, protection: "retention_empty", want: "invalid_retention"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pages int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				switch {
				case query.Has("versioning"):
					state := tc.versioning
					if state == "" {
						state = "Enabled"
					}
					fmt.Fprintf(w, `<VersioningConfiguration><Status>%s</Status></VersioningConfiguration>`, state)
				case query.Has("object-lock"):
					fmt.Fprint(w, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>35</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
				case query.Has("versions"):
					pages++
					if pages == 1 {
						fmt.Fprint(w, tc.first)
					} else {
						fmt.Fprint(w, tc.next)
					}
				case query.Has("retention"):
					if tc.protection == "retention_policy_403" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code><Message>fake-secret-sentinel</Message></Error>`)
					} else if strings.HasPrefix(tc.protection, "retention_4") {
						if tc.protection == "retention_403" {
							w.WriteHeader(http.StatusForbidden)
						} else {
							w.WriteHeader(http.StatusNotFound)
						}
						fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>fake-secret-sentinel</Message></Error>`)
					} else if tc.protection == "retention_empty" {
						fmt.Fprint(w, `<Retention></Retention>`)
					} else {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code><Message>fake-secret-sentinel</Message></Error>`)
					}
				case query.Has("legal-hold"):
					if strings.HasPrefix(tc.protection, "hold_4") {
						if tc.protection == "hold_403" {
							w.WriteHeader(http.StatusForbidden)
						} else {
							w.WriteHeader(http.StatusNotFound)
						}
						fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>fake-secret-sentinel</Message></Error>`)
					} else {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code><Message>fake-secret-sentinel</Message></Error>`)
					}
				}
			}))
			defer server.Close()
			client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake-key-sentinel", SecretAccessKey: "fake-secret-sentinel", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "fake-secret-sentinel") || strings.Contains(err.Error(), "fake-key-sentinel") || len(got.Versions) != 0 {
				t.Fatalf("expected sanitized %q and no partial listing; err=%v count=%d", tc.want, err, len(got.Versions))
			}
		})
	}
}

func TestS3InventoryPreservesOverThousandVersionsAcrossSplitKey(t *testing.T) {
	var pages, protection int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("versioning"):
			fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case q.Has("object-lock"):
			fmt.Fprint(w, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>35</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
		case q.Has("versions"):
			pages++
			if pages == 1 {
				fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>true</IsTruncated><NextKeyMarker>k/0999</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker>`)
				for i := range 1000 {
					fmt.Fprintf(w, `<Version><Key>k/%04d</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest><Size>1</Size></Version>`, i)
				}
				fmt.Fprint(w, `</ListVersionsResult>`)
			} else {
				if q.Get("key-marker") != "k/0999" || q.Get("version-id-marker") != "v1" {
					t.Error("lost split-key cursor")
				}
				fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated><Version><Key>k/0999</Key><VersionId>v0</VersionId><IsLatest>false</IsLatest><Size>1</Size></Version></ListVersionsResult>`)
			}
		case q.Has("retention"):
			protection++
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code></Error>`)
		case q.Has("legal-hold"):
			protection++
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code></Error>`)
		}
	}))
	defer server.Close()
	client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake", SecretAccessKey: "fake", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || protection != 2002 || len(got.Versions) != 1001 || got.Versions[1000].VersionID != "v0" {
		t.Fatalf("incomplete 1001-version inventory: pages=%d checks=%d versions=%d", pages, protection, len(got.Versions))
	}
}

func TestS3InventoryKeepsLegalHoldSeparateFromRetention(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("versioning"):
			fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case q.Has("object-lock"):
			fmt.Fprint(w, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>35</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
		case q.Has("versions"):
			fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated><Version><Key>k/held</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest><Size>2</Size></Version></ListVersionsResult>`)
		case q.Has("retention"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code></Error>`)
		case q.Has("legal-hold"):
			fmt.Fprint(w, `<LegalHold><Status>ON</Status></LegalHold>`)
		}
	}))
	defer server.Close()
	client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake", SecretAccessKey: "fake", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != 1 || !got.Versions[0].LegalHold || !got.Versions[0].Locked || got.Versions[0].Retention != nil {
		t.Fatalf("legal hold must be explicit and blocking: %+v", got)
	}
}

func TestS3InventoryRequiresReadableEnabledBucketObjectLock(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("versioning"):
			fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case q.Has("object-lock"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchObjectLockConfiguration</Code></Error>`)
		case q.Has("versions"):
			fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated></ListVersionsResult>`)
		}
	}))
	defer server.Close()
	client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake", SecretAccessKey: "fake", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
	if err == nil || !strings.Contains(err.Error(), "bucket_lock_read_failed") || len(listing.Versions) != 0 {
		t.Fatalf("missing bucket Object Lock must refuse before listing: err=%v count=%d", err, len(listing.Versions))
	}
}

func TestS3InventoryRefusesDeleteMarkerWithUnverifiableProtection(t *testing.T) {
	var checkedMarker bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("versioning"):
			fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case q.Has("object-lock"):
			fmt.Fprint(w, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>35</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
		case q.Has("versions"):
			fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated><DeleteMarker><Key>k/tombstone</Key><VersionId>d1</VersionId><IsLatest>true</IsLatest></DeleteMarker></ListVersionsResult>`)
		case q.Has("retention"):
			checkedMarker = q.Get("versionId") == "d1"
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, `<Error><Code>MethodNotAllowed</Code></Error>`)
		}
	}))
	defer server.Close()
	client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake", SecretAccessKey: "fake", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
	if err == nil || !strings.Contains(err.Error(), "retention_read_failed") || !checkedMarker || len(got.Versions) != 0 {
		t.Fatalf("unverifiable delete marker must refuse with no partial inventory: err=%v checked=%v count=%d", err, checkedMarker, len(got.Versions))
	}
}

func TestS3InventoryReportsBucketDefaultWithoutImposingChainAge(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch q := r.URL.Query(); {
		case q.Has("versioning"):
			fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
		case q.Has("object-lock"):
			fmt.Fprint(w, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
		case q.Has("versions"):
			fmt.Fprint(w, `<ListVersionsResult><Name>bucket</Name><Prefix>k/</Prefix><IsTruncated>false</IsTruncated></ListVersionsResult>`)
		}
	}))
	defer server.Close()
	client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake", SecretAccessKey: "fake", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
	if err != nil {
		t.Fatal(err)
	}
	if got.BucketLock != (S3BucketLockPolicy{Mode: "GOVERNANCE", Days: 1}) {
		t.Fatalf("observed policy must be reported for separate operator gate: %+v", got.BucketLock)
	}
}

func TestS3InventoryRejectsUnknownBucketLockRules(t *testing.T) {
	cases := []struct{ name, xml string }{
		{"disabled", `<ObjectLockConfiguration><ObjectLockEnabled>Disabled</ObjectLockEnabled></ObjectLockConfiguration>`},
		{"missing default", `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`},
		{"unknown mode", `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>UNKNOWN</Mode><Days>35</Days></DefaultRetention></Rule></ObjectLockConfiguration>`},
		{"two periods", `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>35</Days><Years>1</Years></DefaultRetention></Rule></ObjectLockConfiguration>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch q := r.URL.Query(); {
				case q.Has("versioning"):
					fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
				case q.Has("object-lock"):
					fmt.Fprint(w, tc.xml)
				case q.Has("versions"):
					t.Error("list must not run without a known lock policy")
				}
			}))
			defer server.Close()
			client, err := NewS3InventoryClient(S3InventoryConfig{Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "fake", SecretAccessKey: "fake", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ListVersions(context.Background(), StorageNamespace{Bucket: "bucket", Prefix: "k/"})
			if err == nil || !strings.HasPrefix(err.Error(), "s3 inventory: bucket_lock_") || len(got.Versions) != 0 {
				t.Fatalf("unknown bucket lock must refuse: err=%v count=%d", err, len(got.Versions))
			}
		})
	}
}
