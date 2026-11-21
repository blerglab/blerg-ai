//go:build linux

package daemon

import (
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceStatusRootRepositoryPaths(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, dirty := range []bool{false, true} {
			name := "checkout"
			if linked {
				name = "worktree"
			}
			if dirty {
				name += "-dirty"
			} else {
				name += "-clean"
			}
			t.Run(name, func(t *testing.T) {
				source := t.TempDir()
				initChildRepo(t, source)
				folder := source
				branch := "main"
				var child *LocalWorkspace
				if linked {
					_, parent, _ := hostChildSelectionFixture(t)
					child = hostChildAllocationFixture(t, parent)
					folder = child.state.HostChildren[0].Path
					branch = "wip/child"
				}
				if dirty {
					writeExplorerFile(t, folder, "initial.txt")
					writeExplorerFile(t, folder, "new.txt")
				}
				info, err := os.Stat(folder)
				if err != nil {
					t.Fatal(err)
				}
				manifest := protocol.WorkspaceManifest{LaunchGeneration: "launch", Runtime: "host", Members: []protocol.WorkspaceMember{{ID: "one", Name: "app", Path: "."}}}
				if child != nil {
					manifest = child.state.Launch
				}
				explorer, err := newLocalExplorer(folder, info, manifest, 1, func() bool { return true })
				if err != nil {
					t.Fatal(err)
				}
				defer explorer.Close()
				if child != nil {
					if err := child.bindHostChildExplorer(t.Context(), explorer); err != nil {
						t.Fatal(err)
					}
				}
				explorer.statusDiagnostic = func(stage string, err error, _ string) { t.Logf("root status: %s: %v", stage, err) }
				// A member at '.' is requested with the canonical empty public root path.
				req := protocol.WorkspaceExplorerRequest{SessionID: "session", RequestID: "request", Operation: "status", Path: "", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: 1}}
				result := explorer.Explore(t.Context(), req)
				if !result.OK || result.Status == nil || result.Status.Partial || result.Status.Branch != branch || result.Status.MemberID != manifest.Members[0].ID || result.Status.Path != "" {
					t.Fatalf("root status: %+v", result)
				}
				changes := 0
				if dirty {
					changes = 1
				}
				if result.Status.Changed != changes || result.Status.Untracked != changes {
					t.Fatalf("root changes: %+v", result.Status)
				}
			})
		}
	}
}
