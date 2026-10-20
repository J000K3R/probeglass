package api

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// TestRequestIsHTTPS pins the rule the session cookie's Secure flag follows:
// the connection decides, and a reverse proxy is believed when it says the
// browser used HTTPS. A plain-HTTP homelab must keep getting a cookie without
// Secure, or the browser never sends it back and nobody can sign in.
func TestRequestIsHTTPS(t *testing.T) {
	cases := []struct {
		name  string
		tls   bool
		proto string
		want  bool
	}{
		{name: "plain HTTP", want: false},
		{name: "direct TLS", tls: true, want: true},
		{name: "proxy says https", proto: "https", want: true},
		{name: "proxy list takes the first", proto: "https, http", want: true},
		{name: "proxy says http", proto: "http", want: false},
		{name: "case does not matter", proto: "HTTPS", want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if c.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			if got := requestIsHTTPS(r); got != c.want {
				t.Errorf("requestIsHTTPS = %v, want %v", got, c.want)
			}
		})
	}
}
