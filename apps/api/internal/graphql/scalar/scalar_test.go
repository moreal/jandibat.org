package scalar

import (
	"bytes"
	"strings"
	"testing"
)

func TestDateAcceptsOnlyRealISOCalendarDays(t *testing.T) {
	var date Date
	if err := date.UnmarshalGQL("2024-02-29"); err != nil {
		t.Fatal(err)
	}
	if date != Date("2024-02-29") {
		t.Fatalf("date = %q", date)
	}
	for _, value := range []any{"2023-02-29", "2024-2-29", "2024-02-29T00:00:00Z", 20240229} {
		if err := date.UnmarshalGQL(value); err == nil {
			t.Errorf("accepted invalid date %v", value)
		}
	}
	var encoded bytes.Buffer
	date.MarshalGQL(&encoded)
	if got := encoded.String(); got != `"2024-02-29"` {
		t.Fatalf("date JSON = %s", got)
	}
}

func TestDateTimeNormalizesOffsetToUTC(t *testing.T) {
	var instant DateTime
	if err := instant.UnmarshalGQL("2024-02-29T12:30:00+09:00"); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	instant.MarshalGQL(&encoded)
	if got := encoded.String(); got != `"2024-02-29T03:30:00Z"` {
		t.Fatalf("instant JSON = %s", got)
	}
	for _, value := range []any{"2024-02-29", "2024-02-29T12:30:00", 1709177400} {
		if err := instant.UnmarshalGQL(value); err == nil {
			t.Errorf("accepted invalid instant %v", value)
		}
	}
}

func TestTimeZoneValidatesIANAName(t *testing.T) {
	var zone TimeZone
	if err := zone.UnmarshalGQL("Asia/Seoul"); err != nil {
		t.Fatal(err)
	}
	if zone != TimeZone("Asia/Seoul") {
		t.Fatalf("timezone = %q", zone)
	}
	for _, value := range []any{"Mars/Olympus", "Local", "", 9} {
		if err := zone.UnmarshalGQL(value); err == nil {
			t.Errorf("accepted invalid timezone %v", value)
		}
	}
}

func TestCursorRejectsEmptyOversizedAndNonStringValuesWithoutEchoingInput(t *testing.T) {
	var cursor Cursor
	if err := cursor.UnmarshalGQL("opaque-token"); err != nil {
		t.Fatal(err)
	}
	if cursor != Cursor("opaque-token") {
		t.Fatalf("cursor = %q", cursor)
	}
	for _, value := range []any{"", strings.Repeat("s", 1025), 9} {
		if err := cursor.UnmarshalGQL(value); err == nil {
			t.Errorf("accepted invalid cursor")
		} else if strings.Contains(err.Error(), "ssss") {
			t.Errorf("cursor error leaked input: %s", err)
		}
	}
}
