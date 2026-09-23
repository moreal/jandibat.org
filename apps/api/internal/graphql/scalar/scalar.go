package scalar

import (
	"errors"
	"io"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
)

var errInvalidDate = errors.New("Date must be a valid YYYY-MM-DD calendar day")
var errInvalidDateTime = errors.New("DateTime must be an RFC 3339 instant")
var errInvalidTimeZone = errors.New("TimeZone must be an IANA timezone name")
var errInvalidCursor = errors.New("Cursor must be a nonempty opaque string of at most 1024 bytes")

// Date is a real calendar day serialized without a timezone.
type Date string

func (date Date) MarshalGQL(w io.Writer) {
	graphql.MarshalString(string(date)).MarshalGQL(w)
}

func (date *Date) UnmarshalGQL(value any) error {
	text, ok := value.(string)
	if !ok || len(text) != len("2006-01-02") {
		return errInvalidDate
	}
	parsed, err := time.Parse("2006-01-02", text)
	if err != nil || parsed.Format("2006-01-02") != text {
		return errInvalidDate
	}
	*date = Date(text)
	return nil
}

// DateTime is an instant rendered as UTC RFC 3339.
type DateTime time.Time

func (instant DateTime) MarshalGQL(w io.Writer) {
	graphql.MarshalString(time.Time(instant).UTC().Format(time.RFC3339Nano)).MarshalGQL(w)
}

func (instant *DateTime) UnmarshalGQL(value any) error {
	text, ok := value.(string)
	if !ok {
		return errInvalidDateTime
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return errInvalidDateTime
	}
	*instant = DateTime(parsed.UTC())
	return nil
}

// TimeZone is an IANA timezone name, including the canonical UTC name.
type TimeZone string

func (zone TimeZone) MarshalGQL(w io.Writer) {
	graphql.MarshalString(string(zone)).MarshalGQL(w)
}

func (zone *TimeZone) UnmarshalGQL(value any) error {
	text, ok := value.(string)
	if !ok || text == "" || text == "Local" || strings.HasPrefix(text, "/") {
		return errInvalidTimeZone
	}
	if _, err := time.LoadLocation(text); err != nil {
		return errInvalidTimeZone
	}
	*zone = TimeZone(text)
	return nil
}

// Cursor is an opaque pagination token; its internal version and kind are checked by the cursor codec.
type Cursor string

func (cursor Cursor) MarshalGQL(w io.Writer) {
	graphql.MarshalString(string(cursor)).MarshalGQL(w)
}

func (cursor *Cursor) UnmarshalGQL(value any) error {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > 1024 {
		return errInvalidCursor
	}
	*cursor = Cursor(text)
	return nil
}

var (
	_ graphql.Marshaler   = Date("")
	_ graphql.Unmarshaler = (*Date)(nil)
	_ graphql.Marshaler   = DateTime(time.Time{})
	_ graphql.Unmarshaler = (*DateTime)(nil)
	_ graphql.Marshaler   = TimeZone("")
	_ graphql.Unmarshaler = (*TimeZone)(nil)
	_ graphql.Marshaler   = Cursor("")
	_ graphql.Unmarshaler = (*Cursor)(nil)
)
