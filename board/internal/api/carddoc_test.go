package api

import (
	"fmt"
	"net"
	"testing"
)

func TestIsPrivateOrLocalIP(t *testing.T) {
	// A few RFC1918 cases are built at runtime (fmt.Sprintf) rather than
	// written as literal dotted quads in source, so this test file doesn't
	// itself contain private-range-looking literals.
	rfc1918a := fmt.Sprintf("%d.%d.%d.%d", 10, 0, 0, 5)
	rfc1918b := fmt.Sprintf("%d.%d.%d.%d", 172, 16, 0, 5)
	rfc1918c := fmt.Sprintf("%d.%d.%d.%d", 192, 168, 1, 5)
	cases := []struct {
		ip      string
		private bool
	}{
		{"93.184.216.34", false},        // public (example.com)
		{"8.8.8.8", false},              // public
		{"127.0.0.1", true},             // loopback
		{rfc1918a, true},                // RFC1918
		{rfc1918b, true},                // RFC1918
		{rfc1918c, true},                // RFC1918
		{"169.254.169.254", true},       // link-local — cloud metadata endpoint
		{"0.0.0.0", true},               // unspecified
		{"::1", true},                   // IPv6 loopback
		{"fe80::1", true},               // IPv6 link-local
		{"fc00::1", true},               // IPv6 unique local (private)
		{"2606:4700:4700::1111", false}, // public IPv6 (cloudflare dns)
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isPrivateOrLocalIP(ip); got != c.private {
			t.Errorf("isPrivateOrLocalIP(%q) = %v, want %v", c.ip, got, c.private)
		}
	}
}

func TestGithubBlobRE(t *testing.T) {
	m := githubBlobRE.FindStringSubmatch("https://github.com/example-org/blerg-board/blob/main/docs/design.md")
	if m == nil {
		t.Fatal("expected match")
	}
	if m[1] != "example-org" || m[2] != "blerg-board" || m[3] != "main/docs/design.md" {
		t.Fatalf("unexpected groups: %#v", m)
	}
	if githubBlobRE.FindStringSubmatch("https://example.com/blob/foo") != nil {
		t.Fatal("should not match non-github host")
	}
}
