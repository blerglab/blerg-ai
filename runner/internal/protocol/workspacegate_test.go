package protocol

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceRuntimeReleasePolicy(t *testing.T) {
	t.Setenv("BLERG_WORKSPACE_GATE_VERIFIED", "1")
	t.Setenv("BLERG_RUNTIME_SHELL_GATE_VERIFIED", "1")
	for _, tc := range []struct {
		runtime, platform string
		want              bool
	}{
		{"host", "linux", true}, {"sandbox", "linux", true}, {"sandbox", "darwin", true},
		{"host", "darwin", false}, {"host", "windows", false}, {"sandbox", "windows", false}, {"cluster", "linux", true}, {"cluster", "darwin", false}, {"cluster", "windows", false}, {"unknown", "linux", false},
	} {
		t.Run(tc.runtime+"/"+tc.platform, func(t *testing.T) {
			c := WorkspaceCapabilitiesFor(tc.runtime, tc.platform)
			if WorkspaceRuntimeEnabled(tc.runtime, tc.platform) != tc.want || c.Supports(tc.runtime, false) != tc.want || c.Supports(tc.runtime, true) != tc.want {
				t.Fatal("runtime or shell policy mismatch", c)
			}
			if !tc.want && c != (WorkspaceCapabilities{}) {
				t.Fatal("disabled runtime advertised", c)
			}
		})
	}
	if !ClusterWorkspaceEnabled || MacOSHostWorkspaceEnabled {
		t.Fatal("cluster must ship enabled; macOS host must remain disabled")
	}
}

func TestWorkspaceOffRuntimeCannotBorrowEnabledPeer(t *testing.T) {
	all := WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1, LinuxHost: true, MacOSHost: true, Sandbox: true, Cluster: true}
	negotiated := NegotiateWorkspaceCapabilities(EnabledWorkspaceCapabilities(), all)
	if !negotiated.Supports("host", true) || !negotiated.Supports("sandbox", true) || negotiated.MacOSHost || !negotiated.Supports("cluster", true) {
		t.Fatal("policy not intersected", negotiated)
	}
	for _, peer := range []WorkspaceCapabilities{{ManifestVersion: 2, ShellVersion: 1}, {ManifestVersion: 2, ShellVersion: 1, MacOSHost: true}, {ManifestVersion: 3, ShellVersion: 1, LinuxHost: true}} {
		if c := NegotiateWorkspaceCapabilities(EnabledWorkspaceCapabilities(), peer); c != (WorkspaceCapabilities{}) {
			t.Fatal("off/unknown peer upgraded", c)
		}
	}
	for _, message := range []any{DaemonHello{}, DaemonWelcome{}} {
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(raw, &envelope)
		if _, ok := envelope["workspace_capabilities"]; ok {
			t.Fatal("missing runtime must be omitted")
		}
	}
	if NegotiateWorkspaceCapabilities(WorkspaceCapabilitiesFor("host", "linux"), WorkspaceCapabilitiesFor("sandbox", "linux")) != (WorkspaceCapabilities{}) {
		t.Fatal("cross-runtime negotiation")
	}
}
