//go:build darwin

package daemon

import (
	"bytes"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestWorkspaceDarwinHostCapabilitiesRefuse(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	if err := workspacePlatformProbe(); err == nil {
		t.Fatal("macOS advertised unsupported host containment")
	}
	var out bytes.Buffer
	if err := shellruntime.Probe(&out); err == nil || out.Len() != 0 {
		t.Fatalf("macOS advertised unsupported host shell: output=%q err=%v", out.String(), err)
	}
	client := NewWSClient(Config{})
	var manager *Manager
	service := NewIntegratedWorkspaceService(client, func() *Manager { return manager })
	manager = newManagerWithSender(newRecordingSender(1), ManagerConfig{ReposRoot: t.TempDir(), Workspaces: service})
	if got := service.Capabilities(t.Context()); got.LinuxHost || got.MacOSHost || got.Cluster {
		t.Fatalf("macOS advertised unsupported workspace/shell capabilities: %+v", got)
	}
}
