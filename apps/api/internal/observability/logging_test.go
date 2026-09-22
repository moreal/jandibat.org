package observability

import (
	"testing"
)

func TestBoundedLogEventRejectsUntrustedNames(t *testing.T) {
	for _, event := range []string{"", "ATTACK event", "http.failed\nforged", string(make([]byte, 97))} {
		if got := boundedLogEvent(event); got != "unknown" {
			t.Errorf("boundedLogEvent(%q) = %q, want unknown", event, got)
		}
	}
	if got := boundedLogEvent("http.audit_failed"); got != "http.audit_failed" {
		t.Fatalf("boundedLogEvent(valid) = %q", got)
	}
}
