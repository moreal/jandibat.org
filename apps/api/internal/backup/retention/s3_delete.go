package retention

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3DeleteConfig requires a separate delete-scoped identity and an exact
// approved bucket/prefix. It never consults the ambient SDK credential chain.
type S3DeleteConfig struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Namespace       StorageNamespace
	HTTPClient      *http.Client
	RequestTimeout  time.Duration
}

type S3DeleteClient struct {
	client    *s3.Client
	namespace StorageNamespace
	timeout   time.Duration
}

type S3DeleteError string

func (e S3DeleteError) Error() string { return "s3 delete: " + string(e) }

func NewS3DeleteClient(c S3DeleteConfig) (*S3DeleteClient, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" || validateNamespace(c.Namespace) != nil ||
		c.RequestTimeout < 0 || c.RequestTimeout > deleteTimeout {
		return nil, S3DeleteError("invalid_config")
	}
	timeout := c.RequestTimeout
	if timeout == 0 {
		timeout = deleteTimeout
	}
	transport := c.HTTPClient
	if transport == nil {
		transport = &http.Client{Timeout: timeout}
	}
	// Clone even an injected client: changing its policy in place would affect
	// other callers, while its default policy replays 307/308 DELETE requests.
	noRedirect := *transport
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("s3 delete redirect refused")
	}
	config := aws.Config{
		Region: c.Region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Source: "explicit-delete-scoped"}, nil
		}),
		HTTPClient:       limitedS3Client{inner: &noRedirect},
		RetryMaxAttempts: 1,
	}
	client := s3.NewFromConfig(config, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(c.Endpoint)
		o.UsePathStyle = true
	})
	return &S3DeleteClient{client: client, namespace: c.Namespace, timeout: timeout}, nil
}

// DeleteVersion sends one bounded, exact-version DELETE. The executor owns
// approval, lease, journal, and fresh post-delete inventory proof.
func (c *S3DeleteClient) DeleteVersion(parent context.Context, target TargetVersion) error {
	if c == nil || c.client == nil || parent == nil {
		return S3DeleteError("invalid_client")
	}
	if target.Bucket != c.namespace.Bucket || !strings.HasPrefix(target.Key, c.namespace.Prefix) ||
		len(target.Key) <= len(c.namespace.Prefix) || strings.Contains(target.Key, "\\") ||
		strings.Contains(target.Key, "/../") || strings.Contains(target.Key, "/./") ||
		strings.HasSuffix(target.Key, "/..") || strings.HasSuffix(target.Key, "/.") ||
		hasControl(target.Key) || target.VersionID == "" || strings.EqualFold(target.VersionID, "null") || hasControl(target.VersionID) {
		return S3DeleteError("invalid_target")
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(target.Bucket), Key: aws.String(target.Key), VersionId: aws.String(target.VersionID),
	})
	if err != nil {
		return S3DeleteError("request_failed")
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
