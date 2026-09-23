// Package relayid encodes and validates opaque GraphQL Node IDs.
package relayid

import (
	"encoding/base64"
	"errors"
	"strings"
)

// Kind identifies one durable entity type. Value objects do not have Node IDs.
type Kind string

const (
	Subject            Kind = "Subject"
	ProviderConnection Kind = "ProviderConnection"
	CustomProvider     Kind = "CustomProvider"
	SyncJob            Kind = "SyncJob"
	Session            Kind = "Session"
)

const (
	version          = "v1"
	maxDecodedLength = 512
)

var errInvalidID = errors.New("invalid global ID")

// Encode returns an opaque ID, or an empty string for an invalid kind or raw ID.
// Base64 is an encoding, not encryption; callers must not use secrets as raw IDs.
func Encode(kind Kind, raw string) string {
	if !allowed(kind) || raw == "" || strings.ContainsRune(raw, '\x00') {
		return ""
	}
	payload := version + "\x00" + string(kind) + "\x00" + raw
	if len(payload) > maxDecodedLength {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// Decode validates a Node ID and returns its kind and raw entity ID.
// Every invalid input returns the same error without reflecting the input.
func Decode(global string) (Kind, string, error) {
	if global == "" || len(global) > base64.RawURLEncoding.EncodedLen(maxDecodedLength) {
		return "", "", errInvalidID
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(global)
	if err != nil || len(payload) > maxDecodedLength || base64.RawURLEncoding.EncodeToString(payload) != global {
		return "", "", errInvalidID
	}
	parts := strings.Split(string(payload), "\x00")
	if len(parts) != 3 || parts[0] != version || !allowed(Kind(parts[1])) || parts[2] == "" {
		return "", "", errInvalidID
	}
	return Kind(parts[1]), parts[2], nil
}

// DecodeAs rejects an ID issued for a different Node type.
func DecodeAs(expected Kind, global string) (string, error) {
	kind, raw, err := Decode(global)
	if err != nil || kind != expected {
		return "", errInvalidID
	}
	return raw, nil
}

func allowed(kind Kind) bool {
	switch kind {
	case Subject, ProviderConnection, CustomProvider, SyncJob, Session:
		return true
	default:
		return false
	}
}
