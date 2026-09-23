// Package cursor encodes opaque, versioned keyset positions for GraphQL connections.
package cursor

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Kind separates cursor namespaces even when two connections contain the same IDs.
type Kind string

const (
	Subject            Kind = "Subject"
	Session            Kind = "Session"
	SyncJob            Kind = "SyncJob"
	ProviderConnection Kind = "ProviderConnection"
	CustomProvider     Kind = "CustomProvider"
)

// Position is a stable pagination tuple, independent of whether its row still exists.
type Position struct {
	Timestamp time.Time
	ID        string
}

const (
	version          = "v1"
	maxDecodedLength = 512
)

var errInvalidCursor = errors.New("invalid cursor")

// Encode returns a cursor for a connection kind and sort tuple.
// Base64 is an encoding, not encryption; IDs must not contain secrets.
func Encode(kind Kind, position Position) (string, error) {
	if !allowed(kind) || position.Timestamp.IsZero() || position.Timestamp.UTC().Year() < 1 || position.Timestamp.UTC().Year() > 9999 || !validID(kind, position.ID) {
		return "", errInvalidCursor
	}
	payload := version + "\x00" + string(kind) + "\x00" + position.Timestamp.UTC().Format(time.RFC3339Nano) + "\x00" + position.ID
	if len(payload) > maxDecodedLength {
		return "", errInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString([]byte(payload)), nil
}

// DecodeAs accepts only a canonical cursor issued for the expected connection.
// A deleted anchor is valid because the cursor carries the sort tuple directly.
func DecodeAs(expected Kind, token string) (Position, error) {
	if !allowed(expected) || token == "" || len(token) > base64.RawURLEncoding.EncodedLen(maxDecodedLength) {
		return Position{}, errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(payload) > maxDecodedLength || base64.RawURLEncoding.EncodeToString(payload) != token {
		return Position{}, errInvalidCursor
	}
	parts := strings.Split(string(payload), "\x00")
	if len(parts) != 4 || parts[0] != version || Kind(parts[1]) != expected || !validID(expected, parts[3]) {
		return Position{}, errInvalidCursor
	}
	timestamp, err := time.Parse(time.RFC3339Nano, parts[2])
	if err != nil || timestamp.IsZero() || timestamp.Year() < 1 || timestamp.Year() > 9999 || timestamp.UTC().Format(time.RFC3339Nano) != parts[2] {
		return Position{}, errInvalidCursor
	}
	return Position{Timestamp: timestamp.UTC(), ID: parts[3]}, nil
}

func allowed(kind Kind) bool {
	switch kind {
	case Subject, Session, SyncJob, ProviderConnection, CustomProvider:
		return true
	default:
		return false
	}
}

func validID(kind Kind, id string) bool {
	switch kind {
	case Session, SyncJob, ProviderConnection, CustomProvider:
		parsed, err := uuid.Parse(id)
		return err == nil && parsed.String() == id
	case Subject:
		if !utf8.ValidString(id) || strings.TrimSpace(id) == "" || utf8.RuneCountInString(id) > 64 {
			return false
		}
		for _, character := range id {
			if unicode.IsControl(character) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
