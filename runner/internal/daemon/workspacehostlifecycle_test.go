package daemon

import (
	"context"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestHostLifecycleHelper(t *testing.T) {
	args := os.Args
	if len(args) > 1 && args[len(args)-1] == "host-lifecycle-helper" {
		if shellruntime.ServeHelper(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) > 2 && args[len(args)-2] == "host-lifecycle-helper" && args[len(args)-1] == "--probe" {
		if shellruntime.Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}
func hostLifecycleTestService(t *testing.T, authority WorkspaceRuntimeAuthority) *LocalWorkspaceService {
	t.Helper()
	service := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	return &LocalWorkspaceService{Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) { return nil, "", nil }, HostExecutable: os.Getenv("BLERG_HOST_HELPER_TEST_BINARY"), Authority: authority, Supervisor: newWorkspaceSupervisors(service), HostCommand: func(identity shellruntime.Identity) shellruntime.Command {
		return shellruntime.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestHostLifecycleHelper$", "--", "host-lifecycle-helper"}, Identity: identity}
	}, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}, CommitFinal: acknowledgeWorkspaceFinal}
}
