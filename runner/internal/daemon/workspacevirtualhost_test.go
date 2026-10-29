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
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "selected", Path: "selected", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: "digest", Root: workspaceRootIdentity(info)}
	virtual, err := NewVirtualHostExplorer(namespace, manifest, binding, map[string]HostWorkspaceSource{"one": {Path: source, Root: workspaceRootIdentity(sourceInfo)}}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = virtual.Close() }()
	req := protocol.WorkspaceExplorerRequest{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Operation: "list"}
	root := virtual.Explore(context.Background(), req)
	if !root.OK || len(root.Entries) != 1 || root.Entries[0].Name != "selected" || root.Entries[0].MemberID != "one" || !root.Entries[0].Repository {
		t.Fatalf("virtual root leaked namespace files: %+v", root)
	}
	req.Path = "selected"
	member := virtual.Explore(context.Background(), req)
	if !member.OK || len(member.Entries) != 1 || member.Entries[0].Path != "selected/visible.txt" {
		t.Fatalf("member tree: %+v", member)
	}
	req.Operation = "search"
	req.Path = ""
	req.Filter = "outside"
	if found := virtual.Explore(context.Background(), req); !found.OK || len(found.Entries) != 0 {
		t.Fatalf("virtual search escaped selection: %+v", found)
	}
	file := virtual.ReadFile(context.Background(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "selected/visible.txt"})
	if !file.OK || file.Path != "selected/visible.txt" {
		t.Fatalf("virtual file: %+v", file)
	}
	if file = virtual.ReadFile(context.Background(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: filepath.Base(sibling) + "/outside.txt"}); file.OK {
		t.Fatal("unselected sibling opened")
	}
	req.Operation = "list"
	req.Filter = ""
	req.Path = ""
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

func TestWorkspaceExplorerVirtualHostStructureProjectsOnlyPickedRepositories(t *testing.T) {
	namespace := t.TempDir()
	if err := os.Mkdir(filepath.Join(namespace, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeExplorerFile(t, namespace, "dev/unselected.txt")
	first, second := t.TempDir(), t.TempDir()
	writeExplorerFile(t, first, "visible.txt")
	writeExplorerFile(t, second, "visible.txt")
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Layout: protocol.WorkspaceLayoutStructure, OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{
		{ID: "one", Name: "app", Path: "repositories/app", SourceIdentity: "source-one", CommonDirectoryIdentity: "common-one", Destination: "one"},
		{ID: "two", Name: "app", Path: "dev/ll/app", SourceIdentity: "source-two", CommonDirectoryIdentity: "common-two", Destination: "two"},
	}}
	info, err := os.Stat(namespace)
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: "digest", Root: workspaceRootIdentity(info)}
	sources := map[string]HostWorkspaceSource{}
	for id, folder := range map[string]string{"one": first, "two": second} {
		info, err := os.Stat(folder)
		if err != nil {
			t.Fatal(err)
		}
		sources[id] = HostWorkspaceSource{Path: folder, Root: workspaceRootIdentity(info)}
	}
	virtual, err := NewVirtualHostExplorer(namespace, manifest, binding, sources, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = virtual.Close() }()
	req := protocol.WorkspaceExplorerRequest{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Operation: "list"}
	root := virtual.Explore(t.Context(), req)
	if !root.OK || len(root.Entries) != 2 || root.Entries[0].Path != "dev" || root.Entries[1].Path != "repositories" {
		t.Fatalf("root: %+v", root)
	}
	for _, tc := range []struct{ path, want, id string }{{"dev", "dev/ll", ""}, {"dev/ll", "dev/ll/app", "two"}, {"repositories", "repositories/app", "one"}} {
		req.Path = tc.path
		res := virtual.Explore(t.Context(), req)
		if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != tc.want || res.Entries[0].MemberID != tc.id {
			t.Fatalf("intermediate projection: %+v", res)
		}
	}
	file := virtual.ReadFile(t.Context(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "dev/unselected.txt"})
	if file.OK {
		t.Fatal("unselected intermediate-directory contents exposed")
	}
	file = virtual.ReadFile(t.Context(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "dev/ll/app/visible.txt"})
	if !file.OK {
		t.Fatalf("selected structured file unavailable: %+v", file)
	}
}
