package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestSessionMetadataCanonicalizesIPAddress(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{name: "IPv4 with port", remote: "192.0.2.20:4567", want: "192.0.2.20"},
		{name: "IPv6 with port", remote: "[2001:0db8::1]:4567", want: "2001:db8::1"},
		{name: "bare IPv6", remote: "2001:0db8:0:0::2", want: "2001:db8::2"},
		{name: "mapped IPv4", remote: "[::ffff:192.0.2.30]:4567", want: "192.0.2.30"},
		{name: "invalid", remote: "client.example:4567", want: ""},
		{name: "empty", remote: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			request.RemoteAddr = test.remote
			if got := sessionMetadata(request).IPAddress; got != test.want {
				t.Fatalf("IPAddress = %q, want %q", got, test.want)
			}
		})
	}
}
