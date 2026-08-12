// Package identity owns canonical representations used at authentication and
// account-lifecycle boundaries. Keeping this logic shared prevents deletion
// tombstones and login checks from deriving different identities.
package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/mail"
	"strings"
)

// CanonicalEmail accepts one mailbox and returns the lower-case, trimmed form
// used by user uniqueness and deletion tombstone derivation.
func CanonicalEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	parsed, err := mail.ParseAddress(value)
	return value, err == nil && parsed.Address == value
}

// EmailHMAC returns the non-enumerable, version-keyed tombstone digest for a
// canonical email. Callers must use dedicated key material of at least 32
// bytes; the boolean is false for invalid input or insufficient key material.
func EmailHMAC(email string, key []byte) ([32]byte, bool) {
	canonical, ok := CanonicalEmail(email)
	if !ok || len(key) < 32 {
		return [32]byte{}, false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(canonical))
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest, true
}
