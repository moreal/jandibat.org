package cursor

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const testUUID = "123e4567-e89b-42d3-a456-426614174000"

func TestRoundTripConnectionKindsAndTuple(t *testing.T) {
	for _, kind := range []Kind{Subject, Session, SyncJob, ProviderConnection, CustomProvider} {
		t.Run(string(kind), func(t *testing.T) {
			id := testUUID
			if kind == Subject {
				id = "subject-a"
			}
			position := Position{Timestamp: time.Date(2026, 9, 24, 3, 2, 1, 123456789, time.FixedZone("KST", 9*60*60)), ID: id}
			token, err := Encode(kind, position)
			if err != nil || token == "" || strings.Contains(token, id) {
				t.Fatalf("Encode() = (%q, %v)", token, err)
			}
			got, err := DecodeAs(kind, token)
			if err != nil || !got.Timestamp.Equal(position.Timestamp) || got.Timestamp.Location() != time.UTC || got.ID != position.ID {
				t.Fatalf("DecodeAs() = (%+v, %v)", got, err)
			}
		})
	}
}

func TestRejectsCrossConnectionCursor(t *testing.T) {
	token, err := Encode(Session, Position{Timestamp: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), ID: testUUID})
	if err != nil {
		t.Fatal(err)
	}
	if position, err := DecodeAs(SyncJob, token); err == nil || position != (Position{}) {
		t.Fatalf("DecodeAs(SyncJob, Session cursor) = (%+v, %v)", position, err)
	}
	if position, err := DecodeAs(Subject, token); err == nil || position != (Position{}) {
		t.Fatalf("DecodeAs(Subject, Session cursor) = (%+v, %v)", position, err)
	}
	for _, kind := range []Kind{ProviderConnection, CustomProvider} {
		if position, err := DecodeAs(kind, token); err == nil || position != (Position{}) {
			t.Fatalf("DecodeAs(%s, Session cursor) = (%+v, %v)", kind, position, err)
		}
	}
}

func TestRejectsMalformedAndNoncanonicalTokens(t *testing.T) {
	valid := encoded("v1\x00Session\x002026-09-24T00:00:00Z\x00" + testUUID)
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"invalid base64", "***"},
		{"trailing newline", valid + "\n"},
		{"embedded carriage return", valid[:8] + "\r" + valid[8:]},
		{"padding", base64.URLEncoding.EncodeToString([]byte("v1\x00Session\x002026-09-24T00:00:00Z\x00" + testUUID))},
		{"unsupported version", encoded("v2\x00Session\x002026-09-24T00:00:00Z\x00" + testUUID)},
		{"wrong kind", encoded("v1\x00Subject\x002026-09-24T00:00:00Z\x00" + testUUID)},
		{"missing tuple field", encoded("v1\x00Session\x002026-09-24T00:00:00Z")},
		{"extra tuple field", encoded("v1\x00Session\x002026-09-24T00:00:00Z\x00" + testUUID + "\x00extra")},
		{"empty id", encoded("v1\x00Session\x002026-09-24T00:00:00Z\x00")},
		{"non UTC timestamp", encoded("v1\x00Session\x002026-09-24T09:00:00+09:00\x00" + testUUID)},
		{"noncanonical zero fraction", encoded("v1\x00Session\x002026-09-24T00:00:00.000Z\x00" + testUUID)},
		{"invalid timestamp", encoded("v1\x00Session\x00not-a-time\x00" + testUUID)},
		{"year zero timestamp", encoded("v1\x00Session\x000000-01-01T00:00:00Z\x00" + testUUID)},
		{"oversized", encoded("v1\x00Session\x002026-09-24T00:00:00Z\x00" + strings.Repeat("x", 512))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position, err := DecodeAs(Session, tc.token)
			if err == nil || position != (Position{}) {
				t.Fatalf("DecodeAs() = (%+v, %v); want rejection", position, err)
			}
		})
	}
}

func TestEncodeRejectsInvalidPositionWithoutEchoingInput(t *testing.T) {
	secret := "secret-bearing-id"
	for _, tc := range []struct {
		kind     Kind
		position Position
	}{
		{Kind("UnknownConnection"), Position{Timestamp: time.Now(), ID: secret}},
		{Session, Position{ID: secret}},
		{Session, Position{Timestamp: time.Now(), ID: ""}},
		{Session, Position{Timestamp: time.Now(), ID: "a\x00b"}},
		{Session, Position{Timestamp: time.Now(), ID: strings.Repeat(secret, 50)}},
		{Session, Position{Timestamp: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), ID: testUUID}},
	} {
		token, err := Encode(tc.kind, tc.position)
		if token != "" || err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("Encode(invalid) = (%q, %v)", token, err)
		}
	}
}

func TestDecodedPositionSurvivesDeletedAnchor(t *testing.T) {
	// Keyset continuation must not require the anchor row to still exist.
	anchor := Position{Timestamp: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), ID: testUUID}
	token, err := Encode(Session, anchor)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAs(Session, token)
	if err != nil || got != anchor {
		t.Fatalf("DecodeAs(deleted anchor cursor) = (%+v, %v)", got, err)
	}
}

func TestEncodeRejectsKindSpecificInvalidIDs(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		kind Kind
		id   string
	}{
		{Session, "not-a-uuid"},
		{Session, strings.ToUpper(testUUID)},
		{SyncJob, strings.ReplaceAll(testUUID, "-", "")},
		{ProviderConnection, "not-a-uuid"},
		{ProviderConnection, strings.ToUpper(testUUID)},
		{CustomProvider, strings.ReplaceAll(testUUID, "-", "")},
		{Subject, "\nsubject"},
		{Subject, "subject\x7f"},
		{Subject, " \t "},
		{Subject, string([]byte{0xff})},
		{Subject, strings.Repeat("é", 65)},
	} {
		if token, err := Encode(tc.kind, Position{Timestamp: now, ID: tc.id}); token != "" || err == nil {
			t.Errorf("Encode(%q, invalid ID) = (%q, %v)", tc.kind, token, err)
		}
	}
	if token, err := Encode(Subject, Position{Timestamp: now, ID: strings.Repeat("é", 64)}); err != nil || token == "" {
		t.Fatalf("Encode(Subject, 64 Unicode runes) = (%q, %v)", token, err)
	}
}

func TestDecodeRejectsKindSpecificInvalidIDs(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		id   string
	}{
		{Session, "not-a-uuid"},
		{Session, strings.ToUpper(testUUID)},
		{SyncJob, "no-dashes"},
		{ProviderConnection, "not-a-uuid"},
		{ProviderConnection, strings.ToUpper(testUUID)},
		{CustomProvider, "no-dashes"},
		{Subject, "subject\n"},
		{Subject, "subject\x7f"},
		{Subject, "  "},
		{Subject, string([]byte{0xff})},
		{Subject, strings.Repeat("é", 65)},
	} {
		token := encoded("v1\x00" + string(tc.kind) + "\x002026-09-24T00:00:00Z\x00" + tc.id)
		if position, err := DecodeAs(tc.kind, token); position != (Position{}) || err == nil {
			t.Errorf("DecodeAs(%q, invalid ID) = (%+v, %v)", tc.kind, position, err)
		}
	}
}

func encoded(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
