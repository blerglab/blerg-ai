package daemon

import (
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceDarwinHostAdmissionBeforeSideEffects(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(DaemonStateDirEnv, stateDir)
	// Even with no manager/selection configuration, platform refusal must win:
	// attempting admission would dereference the nil manager before authority.
	var manager *Manager
	for _, kind := range []string{"agent", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			msg := protocol.SpawnSession{SessionID: "darwin-refusal", Kind: kind, Workspace: &protocol.WorkspaceManifest{Runtime: "host"}}
			cwd, reader, err := manager.prepareHostWorkspaceForPlatform(t.Context(), msg, "darwin")
			if err == nil || err.Error() != "workspace sessions are not supported on macOS hosts yet" || cwd != "" || reader != nil {
				t.Fatalf("Darwin host admission: cwd=%q reader=%v err=%v", cwd, reader, err)
			}
			entries, err := os.ReadDir(stateDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("Darwin refusal wrote private state: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestWorkspaceHostAdmissionRequiresItsOwnCapability(t *testing.T) {
	var manager *Manager
	for _, caps := range []protocol.WorkspaceCapabilities{{}, {ManifestVersion: 2, ShellVersion: 1}, protocol.WorkspaceCapabilitiesFor("sandbox", "linux")} {
		_, _, err := manager.prepareHostWorkspaceForPlatform(t.Context(), protocol.SpawnSession{WorkspaceCapabilities: caps}, "linux")
		if err == nil || err.Error() != "host workspace runtime is unnegotiated" {
			t.Fatal("host admission borrowed another runtime", err)
		}
	}
}
