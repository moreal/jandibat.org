package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"testing"
)

func TestCanonicalEmailAndHMAC(t *testing.T) {
	canonical, ok := CanonicalEmail(" PERSON@Example.COM ")
	if !ok || canonical != "person@example.com" {
		t.Fatalf("CanonicalEmail = %q, %t", canonical, ok)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	digest, ok := EmailHMAC(" PERSON@Example.COM ", key)
	if !ok {
		t.Fatal("EmailHMAC rejected valid input")
	}
	wantMAC := hmac.New(sha256.New, key)
	_, _ = wantMAC.Write([]byte("person@example.com"))
	if !hmac.Equal(digest[:], wantMAC.Sum(nil)) {
		t.Fatalf("EmailHMAC = %x", digest)
	}
	if _, ok := EmailHMAC("person@example.com", []byte("short")); ok {
		t.Fatal("EmailHMAC accepted short key")
	}
}
