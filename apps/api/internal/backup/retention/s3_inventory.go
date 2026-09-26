package retention

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	maxS3InventoryPages   = 100
	maxS3InventoryEntries = 100_000
	maxS3ResponseBytes    = 8 << 20
)

// S3InventoryConfig uses explicit, separate read-only S3 credentials. It never
// loads the SDK's ambient credential chain (including IMDS). HTTPClient is for
// controlled TLS transport and offline tests; production should leave it nil.
type S3InventoryConfig struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	HTTPClient      *http.Client
}

type S3InventoryClient struct{ client *s3.Client }

// S3ListedVersion deliberately has no Cockroach catalog ObjectID. The catalog
// to S3-key mapping must be proved independently before the planner can use it.
type S3ListedVersion struct {
	Key          string
	VersionID    string
	SizeBytes    int64
	Current      bool
	DeleteMarker bool
	Retention    *time.Time
	LegalHold    bool
	Locked       bool
}

type S3VersionListing struct {
	Namespace  StorageNamespace
	BucketLock S3BucketLockPolicy
	Versions   []S3ListedVersion
}

// S3BucketLockPolicy is observed provider configuration, not approval to
// delete. A separate operator policy gate must compare this exact value with
// the approved bucket default; this adapter does not impose a blanket age.
type S3BucketLockPolicy struct {
	Mode  string
	Days  int32
	Years int32
}

// S3InventoryError carries only a fixed category; SDK errors may include
// signed URLs, endpoint information, object keys, or provider response text.
type S3InventoryError string

func (e S3InventoryError) Error() string { return "s3 inventory: " + string(e) }

func NewS3InventoryClient(c S3InventoryConfig) (*S3InventoryClient, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, S3InventoryError("invalid_config")
	}
	transport := c.HTTPClient
	if transport == nil {
		transport = &http.Client{Timeout: 10 * time.Second}
	}
	config := aws.Config{
		Region: c.Region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Source: "explicit-static"}, nil
		}),
		HTTPClient:       limitedS3Client{inner: transport},
		RetryMaxAttempts: 1,
	}
	return &S3InventoryClient{client: s3.NewFromConfig(config, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(c.Endpoint)
		o.UsePathStyle = true
	})}, nil
}

type limitedS3Client struct{ inner *http.Client }

func (c limitedS3Client) Do(req *http.Request) (*http.Response, error) {
	response, err := c.inner.Do(req)
	if err != nil {
		return nil, err
	}
	response.Body = &limitedS3Body{ReadCloser: response.Body, Reader: io.LimitReader(response.Body, maxS3ResponseBytes+1)}
	return response, nil
}

type limitedS3Body struct {
	io.ReadCloser
	Reader io.Reader
}

func (r *limitedS3Body) Read(p []byte) (int, error) { return r.Reader.Read(p) }

// ListVersions returns no partial inventory on any ambiguous page, lock read,
// permission failure, or timeout. Both S3 continuation markers are mandatory
// on truncated pages, including boundaries splitting versions of one key.
func (c *S3InventoryClient) ListVersions(parent context.Context, ns StorageNamespace) (S3VersionListing, error) {
	if c == nil || c.client == nil {
		return S3VersionListing{}, S3InventoryError("invalid_client")
	}
	if err := validateNamespace(ns); err != nil {
		return S3VersionListing{}, S3InventoryError("invalid_namespace")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	status, err := c.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(ns.Bucket)})
	if err != nil {
		return S3VersionListing{}, S3InventoryError("versioning_read_failed")
	}
	if status.Status != types.BucketVersioningStatusEnabled {
		return S3VersionListing{}, S3InventoryError("versioning_not_enabled")
	}
	lock, err := c.client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(ns.Bucket)})
	if err != nil {
		return S3VersionListing{}, S3InventoryError("bucket_lock_read_failed")
	}
	if lock == nil || lock.ObjectLockConfiguration == nil || lock.ObjectLockConfiguration.ObjectLockEnabled != types.ObjectLockEnabledEnabled || lock.ObjectLockConfiguration.Rule == nil || lock.ObjectLockConfiguration.Rule.DefaultRetention == nil {
		return S3VersionListing{}, S3InventoryError("bucket_lock_not_configured")
	}
	defaultRetention := lock.ObjectLockConfiguration.Rule.DefaultRetention
	if defaultRetention.Mode != types.ObjectLockRetentionModeCompliance && defaultRetention.Mode != types.ObjectLockRetentionModeGovernance {
		return S3VersionListing{}, S3InventoryError("bucket_lock_invalid_policy")
	}
	policy := S3BucketLockPolicy{Mode: string(defaultRetention.Mode)}
	if defaultRetention.Days != nil && defaultRetention.Years == nil && *defaultRetention.Days > 0 {
		policy.Days = *defaultRetention.Days
	} else if defaultRetention.Years != nil && defaultRetention.Days == nil && *defaultRetention.Years > 0 {
		policy.Years = *defaultRetention.Years
	} else {
		return S3VersionListing{}, S3InventoryError("bucket_lock_invalid_policy")
	}

	result := S3VersionListing{Namespace: ns, BucketLock: policy, Versions: []S3ListedVersion{}}
	seenVersions := map[string]bool{}
	seenCurrent := map[string]bool{}
	seenCursors := map[string]bool{}
	var keyMarker, versionMarker *string
	for page := 0; page < maxS3InventoryPages; page++ {
		input := &s3.ListObjectVersionsInput{Bucket: aws.String(ns.Bucket), Prefix: aws.String(ns.Prefix), KeyMarker: keyMarker, VersionIdMarker: versionMarker}
		output, err := c.client.ListObjectVersions(ctx, input)
		if err != nil {
			return S3VersionListing{}, S3InventoryError("list_failed")
		}
		if output.IsTruncated == nil || aws.ToString(output.Name) != ns.Bucket || aws.ToString(output.Prefix) != ns.Prefix || len(output.CommonPrefixes) != 0 || len(output.Versions)+len(output.DeleteMarkers) > 1000 {
			return S3VersionListing{}, S3InventoryError("invalid_page")
		}
		if len(result.Versions)+len(output.Versions)+len(output.DeleteMarkers) > maxS3InventoryEntries {
			return S3VersionListing{}, S3InventoryError("inventory_limit")
		}
		if *output.IsTruncated && len(output.Versions)+len(output.DeleteMarkers) == 0 {
			return S3VersionListing{}, S3InventoryError("invalid_page")
		}
		for _, version := range output.Versions {
			if version.Size == nil || *version.Size < 0 {
				return S3VersionListing{}, S3InventoryError("invalid_version")
			}
			listed, err := listedS3Version(ns, version.Key, version.VersionId, version.IsLatest, *version.Size, false, seenVersions, seenCurrent)
			if err != nil {
				return S3VersionListing{}, err
			}
			result.Versions = append(result.Versions, listed)
		}
		for _, marker := range output.DeleteMarkers {
			listed, err := listedS3Version(ns, marker.Key, marker.VersionId, marker.IsLatest, 0, true, seenVersions, seenCurrent)
			if err != nil {
				return S3VersionListing{}, err
			}
			result.Versions = append(result.Versions, listed)
		}
		if !*output.IsTruncated {
			if output.NextKeyMarker != nil || output.NextVersionIdMarker != nil {
				return S3VersionListing{}, S3InventoryError("invalid_page")
			}
			for _, version := range result.Versions {
				if !seenCurrent[version.Key] {
					return S3VersionListing{}, S3InventoryError("invalid_version")
				}
			}
			for i := range result.Versions {
				if err := c.readProtection(ctx, ns.Bucket, &result.Versions[i]); err != nil {
					return S3VersionListing{}, err
				}
			}
			return result, nil
		}
		if output.NextKeyMarker == nil || output.NextVersionIdMarker == nil || *output.NextKeyMarker == "" || *output.NextVersionIdMarker == "" {
			return S3VersionListing{}, S3InventoryError("invalid_cursor")
		}
		cursor := *output.NextKeyMarker + "\x00" + *output.NextVersionIdMarker
		if seenCursors[cursor] || (keyMarker != nil && *keyMarker == *output.NextKeyMarker && *versionMarker == *output.NextVersionIdMarker) {
			return S3VersionListing{}, S3InventoryError("invalid_cursor")
		}
		seenCursors[cursor] = true
		keyMarker, versionMarker = output.NextKeyMarker, output.NextVersionIdMarker
	}
	return S3VersionListing{}, S3InventoryError("inventory_limit")
}

func listedS3Version(ns StorageNamespace, key, id *string, current *bool, size int64, marker bool, seenVersions, seenCurrent map[string]bool) (S3ListedVersion, error) {
	if key == nil || id == nil || current == nil || !strings.HasPrefix(*key, ns.Prefix) || *id == "" || strings.EqualFold(*id, "null") {
		return S3ListedVersion{}, S3InventoryError("invalid_version")
	}
	identity := *key + "\x00" + *id
	if seenVersions[identity] || (*current && seenCurrent[*key]) {
		return S3ListedVersion{}, S3InventoryError("duplicate_version")
	}
	seenVersions[identity] = true
	if *current {
		seenCurrent[*key] = true
	}
	return S3ListedVersion{Key: *key, VersionID: *id, SizeBytes: size, Current: *current, DeleteMarker: marker}, nil
}

func (c *S3InventoryClient) readProtection(ctx context.Context, bucket string, v *S3ListedVersion) error {
	input := &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String(v.Key), VersionId: aws.String(v.VersionID)}
	retention, err := c.client.GetObjectRetention(ctx, input)
	if err != nil && !noObjectLockPolicy(err) {
		return S3InventoryError("retention_read_failed")
	}
	if err == nil {
		if retention == nil || retention.Retention == nil || retention.Retention.Mode == "" || retention.Retention.RetainUntilDate == nil {
			return S3InventoryError("invalid_retention")
		}
		until := retention.Retention.RetainUntilDate.UTC()
		v.Retention = &until
		v.Locked = true
	}
	hold, err := c.client.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String(v.Key), VersionId: aws.String(v.VersionID)})
	if err != nil && !noObjectLockPolicy(err) {
		return S3InventoryError("legal_hold_read_failed")
	}
	if err == nil {
		if hold == nil || hold.LegalHold == nil {
			return S3InventoryError("invalid_legal_hold")
		}
		switch hold.LegalHold.Status {
		case types.ObjectLockLegalHoldStatusOn:
			v.LegalHold, v.Locked = true, true
		case types.ObjectLockLegalHoldStatusOff:
		default:
			return S3InventoryError("invalid_legal_hold")
		}
	}
	return nil
}

func noObjectLockPolicy(err error) bool {
	var apiError smithy.APIError
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchObjectLockConfiguration" &&
		errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}
