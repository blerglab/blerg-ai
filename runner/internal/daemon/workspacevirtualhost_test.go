package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceExplorerVirtualHostConfinesSelectedSources(t *testing.T) {
	namespace := t.TempDir()
	writeExplorerFile(t, namespace, "not-public.txt")
	source := t.TempDir()
	writeExplorerFile(t, source, "visible.txt")
	sibling := t.TempDir()
	writeExplorerFile(t, sibling, "outside.txt")
	info, err := os.Stat(namespace)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "selected", Path: "members/one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: "digest", Root: workspaceRootIdentity(info)}
	virtual, err := NewVirtualHostExplorer(namespace, manifest, binding, map[string]HostWorkspaceSource{"one": {Path: source, Root: workspaceRootIdentity(sourceInfo)}}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = virtual.Close() }()
	req := protocol.WorkspaceExplorerRequest{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Operation: "list"}
	root := virtual.Explore(context.Background(), req)
	if !root.OK || len(root.Entries) != 1 || root.Entries[0].Name != "members" {
		t.Fatalf("virtual root leaked namespace files: %+v", root)
	}
	req.Path = "members/one"
	member := virtual.Explore(context.Background(), req)
	if !member.OK || len(member.Entries) != 1 || member.Entries[0].Path != "members/one/visible.txt" {
		t.Fatalf("member tree: %+v", member)
	}
	req.Operation = "search"
	req.Path = ""
	req.Filter = "outside"
	if found := virtual.Explore(context.Background(), req); !found.OK || len(found.Entries) != 0 {
		t.Fatalf("virtual search escaped selection: %+v", found)
	}
	file := virtual.ReadFile(context.Background(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "members/one/visible.txt"})
	if !file.OK || file.Path != "members/one/visible.txt" {
		t.Fatalf("virtual file: %+v", file)
	}
	if file = virtual.ReadFile(context.Background(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: filepath.Base(sibling) + "/outside.txt"}); file.OK {
		t.Fatal("unselected sibling opened")
	}
	req.Operation = "list"
	req.Filter = ""
	req.Path = "members"
	moved := source + "-moved"
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(moved) }()
	res := virtual.Explore(context.Background(), req)
	if !res.OK || len(res.UnavailableMembers) != 1 || res.UnavailableMembers[0] != "one" {
		t.Fatal("missing selected member not reported unavailable")
	}
}
