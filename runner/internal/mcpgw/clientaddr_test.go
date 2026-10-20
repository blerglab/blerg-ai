package mcpgw

import (
	"net/http/httptest"
	"testing"
)

// Behind a trusted proxy the public route throttles the real client: the right-most hop of
// X-Forwarded-For that is not itself a trusted proxy. A peer outside the list is taken as is,
// whatever headers it sends.
func TestPublicClientAddress(t *testing.T) {
	trusted, err := ParseTrustedProxies("203.0.113.0/24, 192.0.2.10/32")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		peer, xff, real, want string
	}{
		{"198.51.100.200:4000", "198.51.100.1", "", "198.51.100.200"},           // untrusted peer: headers ignored
		{"203.0.113.5:4000", "198.51.100.1", "", "198.51.100.1"},                // trusted ingress
		{"203.0.113.5:4000", "198.51.100.66, 198.51.100.1", "", "198.51.100.1"}, // a client-forged left entry is not used
		{"203.0.113.5:4000", "198.51.100.1, 192.0.2.10", "", "198.51.100.1"},    // a second trusted hop is skipped
		{"203.0.113.5:4000", "", "198.51.100.7", "198.51.100.7"},                // X-Real-IP when no X-Forwarded-For
		{"203.0.113.5:4000", "not-an-address", "", "203.0.113.5"},               // unparsable: the peer
		{"203.0.113.5:4000", "", "", "203.0.113.5"},
	} {
		r := httptest.NewRequest("POST", "/mcp-gw/x", nil)
		r.RemoteAddr = tc.peer
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.real != "" {
			r.Header.Set("X-Real-IP", tc.real)
		}
		if got := publicClientIP(r, trusted); got != tc.want {
			t.Errorf("peer %s xff %q real %q: %s, want %s", tc.peer, tc.xff, tc.real, got, tc.want)
		}
	}
	// No list: the peer, as before.
	r := httptest.NewRequest("POST", "/mcp-gw/x", nil)
	r.RemoteAddr = "203.0.113.5:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := publicClientIP(r, nil); got != "203.0.113.5" {
		t.Errorf("no trusted proxies: %s", got)
	}
	if _, err := ParseTrustedProxies("203.0.113.0/24,bogus"); err == nil {
		t.Error("a bad entry was accepted")
	}
	if p, err := ParseTrustedProxies(""); err != nil || p != nil {
		t.Errorf("empty: %v %v", p, err)
	}
}
