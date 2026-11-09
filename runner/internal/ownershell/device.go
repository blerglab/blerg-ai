package ownershell

import (
	"encoding/hex"
	"strings"
)

// DeviceLabel is presentation only. Its short browser-profile ID is not a credential,
// hardware fingerprint, network address, or authority for a control handoff.
func DeviceLabel(platform, id string) string {
	switch platform {
	case "Windows", "macOS", "Linux", "Android", "iOS":
	default:
		platform = "unknown"
	}
	if len(id) == 8 {
		if _, err := hex.DecodeString(id); err == nil {
			return platform + " browser " + strings.ToLower(id)
		}
	}
	return platform
}

func normalizeDevice(label string) string {
	platform, id, _ := strings.Cut(label, " browser ")
	return DeviceLabel(platform, id)
}
