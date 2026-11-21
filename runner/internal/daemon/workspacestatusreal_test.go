package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Opt-in acceptance against an existing checkout; never changes its files or config.
// Named outside "TestWorkspace": the macOS gate selects that prefix and refuses skips.
func TestOptInStatusRealRepository(t *testing.T) {
	root := os.Getenv("BLERG_STATUS_TEST_REPOSITORY")
	if root == "" {
		t.Skip("set BLERG_STATUS_TEST_REPOSITORY to an existing checkout")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	start := legacyExplorerStart("real-status")
	start.Manifest.Runtime = "host"
	remote := processExplorer(t, root, start)
	req := protocol.WorkspaceExplorerRequest{
		Type: "workspace_explorer", RequestID: "real-status", SessionID: start.SessionID,
		WorkspaceRuntimeRef: start.Runtime, Operation: "status", MemberID: "repo",
	}
	started := time.Now()
	res := remote.Explore(t.Context(), req)
	t.Logf("real helper status: elapsed=%v ok=%v reason=%q", time.Since(started), res.OK, res.StatusReason)
	if !res.OK || res.Status == nil {
		t.Fatalf("real repository status: %+v", res)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: start.Runtime, ManifestDigest: start.Manifest.Digest, Root: workspaceRootIdentity(info)}
	host, err := NewLocalExplorer(root, start.Manifest, binding, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	host.statusDiagnostic = func(stage string, err error, stderr string) {
		t.Logf("host diagnostic: stage=%q err=%v stderr=%q", stage, err, stderr)
	}
	started = time.Now()
	res = host.Explore(t.Context(), req)
	t.Logf("host status: elapsed=%v ok=%v reason=%q", time.Since(started), res.OK, res.StatusReason)
	if !res.OK || res.Status == nil {
		t.Fatalf("host repository status: %+v", res)
	}

}
