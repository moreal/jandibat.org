package relayid

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNodeIDRoundTripForDurableKinds(t *testing.T) {
	for _, kind := range []Kind{Subject, ProviderConnection, CustomProvider, SyncJob, Session} {
		t.Run(string(kind), func(t *testing.T) {
			global := Encode(kind, "same-database-id")
			if global == "" || strings.Contains(global, "same-database-id") {
				t.Fatalf("Encode(%q) produced non-opaque ID %q", kind, global)
			}
			gotKind, raw, err := Decode(global)
			if err != nil || gotKind != kind || raw != "same-database-id" {
				t.Fatalf("Decode(Encode(%q)) = (%q, %q, %v)", kind, gotKind, raw, err)
			}
		})
	}
}

func TestNodeIDRejectsMalformedOrUnsupportedInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"invalid base64", "***"},
		{"trailing newline", opaque("v1\x00Subject\x00abc") + "\n"},
		{"embedded newline", opaque("v1\x00Subject\x00abc")[:8] + "\n" + opaque("v1\x00Subject\x00abc")[8:]},
		{"embedded carriage return", opaque("v1\x00Subject\x00abc")[:8] + "\r" + opaque("v1\x00Subject\x00abc")[8:]},
		{"padded base64", base64.URLEncoding.EncodeToString([]byte("v1\x00Subject\x00abc"))},
		{"truncated payload", opaque("v1\x00Subject")},
		{"wrong version", opaque("v2\x00Subject\x00abc")},
		{"unknown kind", opaque("v1\x00User\x00abc")},
		{"empty kind", opaque("v1\x00\x00abc")},
		{"empty raw ID", opaque("v1\x00Subject\x00")},
		{"extra separator", opaque("v1\x00Subject\x00abc\x00def")},
		{"too long", opaque("v1\x00Subject\x00" + strings.Repeat("a", 512))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, raw, err := Decode(tc.id)
			if err == nil || kind != "" || raw != "" {
				t.Fatalf("Decode returned (%q, %q, %v); want no identity and an error", kind, raw, err)
			}
		})
	}
}

func TestNodeIDEncodeRejectsInvalidParts(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		raw  string
	}{
		{Kind("User"), "abc"},
		{Subject, ""},
		{Subject, "a\x00b"},
		{Subject, strings.Repeat("a", 512)},
	} {
		if got := Encode(tc.kind, tc.raw); got != "" {
			t.Errorf("Encode(%q, raw) = %q; want rejection", tc.kind, got)
		}
	}
}

func TestNodeIDRejectsCrossTypeSubstitution(t *testing.T) {
	global := Encode(Subject, "same-database-id")
	if raw, err := DecodeAs(ProviderConnection, global); err == nil || raw != "" {
		t.Fatalf("DecodeAs(ProviderConnection, Subject ID) = (%q, %v); want rejection", raw, err)
	}
	if raw, err := DecodeAs(Subject, global); err != nil || raw != "same-database-id" {
		t.Fatalf("DecodeAs(Subject, Subject ID) = (%q, %v)", raw, err)
	}
}

func TestNodeIDErrorsDoNotEchoTokenOrRawID(t *testing.T) {
	const secret = "secret-bearing-token"
	for _, global := range []string{secret, opaque("v1\x00Subject\x00" + secret + "\x00tamper")} {
		_, _, err := Decode(global)
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), global) {
			t.Fatalf("Decode error leaked input or did not reject")
		}
	}
}

func opaque(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
