package daemon

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceCapabilitiesMissingConfigurationRefuses(t *testing.T) {
	for _, service := range []*LocalWorkspaceService{nil, {}, {HostExecutable: "/missing-helper"}} {
		if got := service.Capabilities(context.Background()); got != (protocol.WorkspaceCapabilities{}) {
			t.Fatal("incomplete runtime advertised", got)
		}
	}
}

func TestWorkspaceRuntimeCapabilitiesAreIndependent(t *testing.T) {
	client := NewWSClient(Config{})
	service := NewIntegratedWorkspaceService(client, func() *Manager { return &Manager{} })
	ok := func(context.Context) error { return nil }
	bad := func(context.Context) error { return context.Canceled }
	for _, tc := range []struct {
		platform              string
		host, image           func(context.Context) error
		wantHost, wantSandbox bool
	}{
		{"linux", ok, ok, true, true}, {"linux", ok, bad, true, false}, {"linux", bad, ok, false, true},
		{"darwin", ok, ok, false, true}, {"darwin", ok, bad, false, false}, {"windows", ok, ok, false, false},
	} {
		got := service.capabilitiesForPlatform(t.Context(), tc.platform, tc.host, tc.image)
		if got.Supports("host", true) != tc.wantHost || got.Supports("sandbox", true) != tc.wantSandbox || got.Cluster || got.MacOSHost {
			t.Fatalf("%s policy/probes: %+v", tc.platform, got)
		}
	}
}
