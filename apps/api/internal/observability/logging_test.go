package observability

import (
	"strings"
	"testing"
)

func TestFormatLogIncludesResourceAndEscapesUntrustedMessage(t *testing.T) {
	formatted := FormatLog(Resource{BuildSHA: "abc", Environment: "staging", Region: "icn"}, "http.audit_failed", "request=one\nsecret=forged")
	for _, fragment := range []string{
		`build_sha="abc"`, `environment="staging"`, `region="icn"`,
		`event="http.audit_failed"`, `message="request=one\nsecret=[REDACTED]"`,
	} {
		if !strings.Contains(formatted, fragment) {
			t.Errorf("structured log missing %q: %s", fragment, formatted)
		}
	}
	if strings.Count(formatted, "\n") != 0 {
		t.Fatalf("structured log contains injected newline: %q", formatted)
	}
}

func TestFormatLogBoundsEventAndDefaultsResource(t *testing.T) {
	formatted := FormatLog(Resource{}, "ATTACK event", "safe")
	for _, fragment := range []string{
		`build_sha="unknown"`, `environment="unknown"`, `region="unknown"`, `event="unknown"`,
	} {
		if !strings.Contains(formatted, fragment) {
			t.Errorf("structured log missing %q: %s", fragment, formatted)
		}
	}
}

func TestFormatLogRedactsCommonCredentialShapes(t *testing.T) {
	formatted := FormatLog(Resource{}, "api.process_stopped", "Bearer token-value access_token=secret postgresql://user:password@db/app")
	for _, secret := range []string{"token-value", "access_token=secret", "user:password@"} {
		if strings.Contains(formatted, secret) {
			t.Errorf("structured log leaked %q: %s", secret, formatted)
		}
	}
	if strings.Count(formatted, "[REDACTED]") != 3 {
		t.Fatalf("structured log redactions missing: %s", formatted)
	}
}
