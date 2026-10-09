package main

import (
	"os"
	"testing"
)

// TestSandboxNetworkSetting: unset means the compose default network, an
// explicit empty value disables joining any network, anything else is a name.
func TestSandboxNetworkSetting(t *testing.T) {
	t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", "")
	os.Unsetenv("BLERG_RUNNER_SANDBOX_NETWORK")
	if got := sandboxNetworkSetting(); got != "blerg-sandbox" {
		t.Errorf("unset: got %q, want %q", got, "blerg-sandbox")
	}
	t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", "")
	if got := sandboxNetworkSetting(); got != "" {
		t.Errorf("empty: got %q, want disabled", got)
	}
	t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", "my-agents")
	if got := sandboxNetworkSetting(); got != "my-agents" {
		t.Errorf("custom: got %q, want %q", got, "my-agents")
	}
	// Docker's special network modes (and anything that would parse as a
	// flag) would drop the sandbox's isolation: refused, treated as disabled.
	for _, bad := range []string{"host", "none", "bridge", "HOST", "container:abc", "container:", "-v", "--privileged"} {
		t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", bad)
		if got := sandboxNetworkSetting(); got != "" {
			t.Errorf("%q: got %q, want disabled", bad, got)
		}
	}
	// Ordinary names that merely resemble a special mode are kept.
	for _, ok := range []string{"blerg-sandbox", "my-bridge-net", "hostnet", "none-such", "containers"} {
		t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", ok)
		if got := sandboxNetworkSetting(); got != ok {
			t.Errorf("%q: got %q, want it kept", ok, got)
		}
	}
}

func TestPrependPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	bin := "/home/u/.local/bin"

	tests := []struct {
		name string
		list string
		dir  string
		want string
	}{
		{"empty list", "", bin, bin},
		{"empty dir is no-op", "/usr/bin", "", "/usr/bin"},
		{"prepends when missing", "/usr/bin" + sep + "/bin", bin, bin + sep + "/usr/bin" + sep + "/bin"},
		{"no change when already first", bin + sep + "/usr/bin", bin, bin + sep + "/usr/bin"},
		{"no change when already present later", "/usr/bin" + sep + bin, bin, "/usr/bin" + sep + bin},
		{"does not match prefix substring", "/usr/bin" + sep + bin + "/extra", bin, bin + sep + "/usr/bin" + sep + bin + "/extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prependPath(tt.list, tt.dir); got != tt.want {
				t.Errorf("prependPath(%q, %q) = %q, want %q", tt.list, tt.dir, got, tt.want)
			}
		})
	}
}

// TestServerHTTPSetting: BLERG_RUNNER_SERVER_HTTP wins as it is; without it the
// base is derived from a deprecated BLERG_RUNNER_PREVIEW_URL ending in
// /api/preview, and from nothing else.
func TestServerHTTPSetting(t *testing.T) {
	for _, c := range []struct {
		name, serverHTTP, previewURL, want string
		derived                            bool
	}{
		{"neither set", "", "", "", false},
		{"server http only", "http://runner.example.test", "", "http://runner.example.test", false},
		{"server http wins", "http://runner.example.test", "http://other.example.test/api/preview", "http://runner.example.test", false},
		{"derived from the preview url", "", "http://runner.example.test/api/preview", "http://runner.example.test", true},
		{"trailing slash and spaces", "", " https://runner.example.test/base/api/preview/ ", "https://runner.example.test/base", true},
		{"not the preview endpoint", "", "http://runner.example.test/preview", "", false},
		{"only the suffix", "", "/api/preview", "", false},
	} {
		got, derived := serverHTTPSetting(c.serverHTTP, c.previewURL)
		if got != c.want || derived != c.derived {
			t.Errorf("%s: serverHTTPSetting(%q, %q) = %q, %v; want %q, %v",
				c.name, c.serverHTTP, c.previewURL, got, derived, c.want, c.derived)
		}
	}
}
