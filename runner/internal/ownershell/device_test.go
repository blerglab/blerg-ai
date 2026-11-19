package ownershell

import "testing"

func TestDeviceLabelsExcludeArbitraryInput(t *testing.T) {
	for _, tc := range []struct{ platform, id, want string }{
		{"Linux", "aabbccdd", "Linux browser aabbccdd"},
		{"Windows", "", "Windows"},
		{"Bearer private-fixture", "secret", "unknown"},
		{"macOS", "https://example.com", "macOS"},
	} {
		if got := DeviceLabel(tc.platform, tc.id); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
