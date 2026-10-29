//go:build linux || darwin

package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := hostgit.Output(t.Context(), hostgit.Options{Dir: dir, Config: []string{"user.name=Fixture", "user.email=fixture@example.test"}, MemoryLimit: true, LimitKB: 256 << 10}, args...)
	if err != nil {
		t.Fatalf("fixture git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}
func fixtureRepo(t *testing.T, root, name string, commit bool) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "init", "--initial-branch=main")
	if commit {
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, dir, "add", "README")
		fixtureGit(t, dir, "commit", "-m", "fixture")
	}
	return dir
}
func request(operation string) protocol.DiscoverWorkspace {
	return protocol.DiscoverWorkspace{RequestID: "fixture-request", Operation: operation, Runtime: "host"}
}
func list(t *testing.T, s *Service, rel string) protocol.DiscoveryPage {
	t.Helper()
	req := request("list")
	req.Path = rel
	res := s.Handle(t.Context(), req)
	if !res.OK || res.Page == nil {
		t.Fatalf("list %q: %s", rel, res.ErrorCode)
	}
	return *res.Page
}
func ref(entry protocol.DiscoveryEntry) protocol.SelectionRef {
	return protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity}
}
func find(t *testing.T, page protocol.DiscoveryPage, path string) protocol.DiscoveryEntry {
	t.Helper()
	for _, entry := range page.Entries {
		if entry.Path == path {
			return entry
		}
	}
	t.Fatalf("entry %q absent", path)
	return protocol.DiscoveryEntry{}
}
func resolve(t *testing.T, s *Service, refs ...protocol.SelectionRef) protocol.WorkspaceDiscoveryResult {
	t.Helper()
	req := request("resolve")
	req.Members = refs
	return s.Handle(t.Context(), req)
}
func TestDiscoveryDistinguishesDirectoriesAndRepositoriesAndBindsSources(t *testing.T) {
	root := fixtureRoot(t)
	fixtureRepo(t, root, "one", true)
	if err := os.Mkdir(filepath.Join(root, "ordinary"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	page := list(t, s, ".")
	one := find(t, page, "one")
	ordinary := find(t, page, "ordinary")
	if one.Kind != "repository" || ordinary.Kind != "directory" {
		t.Fatal("ordinary directories were labeled repositories")
	}
	if result := resolve(t, s, ref(ordinary)); result.OK {
		t.Fatal("ordinary directory became a repository member")
	}
	result := resolve(t, s, ref(one))
	if !result.OK {
		t.Fatal(result.ErrorCode)
	}
	selected := result.Resolved
	source := selected.Members[0]
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "person-a", Runtime: "host", LaunchGeneration: "launch-one", Root: selected.Root, Members: []protocol.WorkspaceMember{{ID: source.ID, Name: source.Name, Path: ".", SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity}}}
	m.Digest = workspaceselection.Digest(m)
	bind := request("bind")
	bind.SelectionIdentity = selected.SelectionIdentity
	bind.Manifest = &m
	if reply := s.Handle(t.Context(), bind); !reply.OK {
		t.Fatal("binding refused", reply.ErrorCode)
	}
	pins, err := s.PinBound(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	defer pins.Close()
	if pins.Sources[0].CanonicalPath != filepath.Join(root, "one") {
		t.Fatal("wrong source pin")
	}
	raw, err := json.Marshal(pins)
	if err != nil || string(raw) != "{}" {
		t.Fatal("private source pins are serializable")
	}
	forged := m
	forged.Members = append([]protocol.WorkspaceMember(nil), m.Members...)
	forged.Members[0].Name = "other"
	if _, err := s.PinBound(t.Context(), forged); err == nil {
		t.Fatal("forged manifest borrowed a bound digest")
	}
	if err := os.Rename(filepath.Join(root, "one"), filepath.Join(root, "old-one")); err != nil {
		t.Fatal(err)
	}
	fixtureRepo(t, root, "one", false)
	if reply := resolve(t, s, ref(one)); reply.OK {
		t.Fatal("replaced source accepted a stale identity")
	}
	if _, err := s.PinBound(t.Context(), m); err == nil {
		t.Fatal("bound source silently changed after replacement")
	}
}
func TestDiscoveryMoreThanTwentyRequiresExplicitSubset(t *testing.T) {
	root := fixtureRoot(t)
	for i := 0; i < 21; i++ {
		fixtureRepo(t, root, fmt.Sprintf("repo-%02d", i), false)
	}
	s := New(func() string { return root })
	page := list(t, s, ".")
	if len(page.Entries) != 21 {
		t.Fatal("discovery silently took a first-20 subset")
	}
	req := request("expand")
	folder := ref(page.Root)
	req.Folder = &folder
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "too_many_members" || reply.Resolved != nil {
		t.Fatal("folder silently truncated membership", reply.ErrorCode)
	}
	refs := make([]protocol.SelectionRef, 0, 20)
	for _, entry := range page.Entries[:20] {
		refs = append(refs, ref(entry))
	}
	if reply := resolve(t, s, refs...); !reply.OK || len(reply.Resolved.Members) != 20 {
		t.Fatal("explicit 20-member subset refused", reply.ErrorCode)
	}
	subset := request("resolve")
	subset.Folder = &folder
	subset.Members = refs
	if reply := s.Handle(t.Context(), subset); !reply.OK || len(reply.Resolved.Members) != 20 || reply.Resolved.Folder == nil {
		t.Fatal("folder subset did not preserve its physical anchor", reply.ErrorCode)
	} else {
		m := manifestForSelection(reply.Resolved, "subset")
		if m.Kind != "folder" || m.Members[0].Path != "repo-00" {
			t.Fatal("folder subset acquired synthetic member paths")
		}
	}
	refs = append(refs, ref(page.Entries[20]))
	if reply := resolve(t, s, refs...); reply.OK {
		t.Fatal("21 explicit members accepted")
	}
}
func TestDiscoveryPaginationTamperingAndDirectoryChanges(t *testing.T) {
	root := fixtureRoot(t)
	for i := 0; i < 510; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("folder-%03d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := New(func() string { return root })
	first := list(t, s, ".")
	if len(first.Entries) != 500 || first.NextCursor == "" || !first.Truncated {
		t.Fatal("listing is not a bounded page")
	}
	req := request("list")
	req.Path = "."
	req.Cursor = first.NextCursor
	second := s.Handle(t.Context(), req)
	if !second.OK || len(second.Page.Entries) != 10 || second.Page.NextCursor != "" {
		t.Fatal("second page lost entries", second.ErrorCode)
	}
	data, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	req.Cursor = base64.RawURLEncoding.EncodeToString(data)
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "stale" {
		t.Fatal("tampered cursor accepted")
	}
	req.Cursor = first.NextCursor
	req.Path = first.Entries[0].Path
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "stale" {
		t.Fatal("cursor moved to another directory")
	}
	if err := os.Mkdir(filepath.Join(root, "new-folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	req.Path = "."
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "stale" {
		t.Fatal("changed-directory cursor survived")
	}
}
func TestDiscoveryRejectsOverlapDuplicatesAndLinkedWorktrees(t *testing.T) {
	root := fixtureRoot(t)
	parent := fixtureRepo(t, root, "parent", true)
	fixtureRepo(t, root, "parent/nested", false)
	fixtureGit(t, parent, "worktree", "add", "--detach", filepath.Join(root, "linked"), "HEAD")
	s := New(func() string { return root })
	page := list(t, s, ".")
	nestedPage := list(t, s, "parent")
	parentRef, linkedRef, nestedRef := ref(find(t, page, "parent")), ref(find(t, page, "linked")), ref(find(t, nestedPage, "parent/nested"))
	if find(t, page, "linked").Kind != "repository" {
		t.Fatal("a .git file was treated as a submodule")
	}
	for _, layout := range []protocol.WorkspaceLayout{protocol.WorkspaceLayoutNames, protocol.WorkspaceLayoutStructure} {
		for _, refs := range [][]protocol.SelectionRef{{parentRef, parentRef}, {parentRef, nestedRef}, {parentRef, linkedRef}} {
			req := request("resolve")
			req.Members = refs
			req.Layout = layout
			if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "overlapping_repositories" {
				t.Fatal("overlap/common-dir identity accepted", reply.ErrorCode)
			}
		}
	}
	if reply := resolve(t, s, nestedRef); !reply.OK {
		t.Fatal("explicit independent nested repository refused", reply.ErrorCode)
	}
}
func TestDiscoveryUsesGitlinksIncludingUninitializedSubmodules(t *testing.T) {
	root := fixtureRoot(t)
	parent := fixtureRepo(t, root, "parent", true)
	if err := os.Mkdir(filepath.Join(parent, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	head := fixtureGit(t, parent, "rev-parse", "HEAD")
	fixtureGit(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
	for _, version := range []string{"2", "3", "4"} {
		fixtureGit(t, parent, "update-index", "--index-version="+version)
		s := New(func() string { return root })
		page := list(t, s, "parent")
		for _, entry := range page.Entries {
			if entry.Path == "parent/sub" {
				t.Fatal("uninitialized gitlink listed as a folder/member")
			}
		}
		req := request("list")
		req.Path = "parent/sub"
		if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "submodule" {
			t.Fatal("direct path bypassed submodule metadata", version, reply.ErrorCode)
		}
	}
}
func TestDiscoveryRefusesUnexpandedSplitIndex(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "repo", true)
	fixtureGit(t, dir, "update-index", "--split-index")
	s := New(func() string { return root })
	req := request("list")
	req.Path = "repo"
	if reply := s.Handle(t.Context(), req); reply.OK {
		t.Fatal("split index silently hid gitlinks")
	}
}
func TestDiscoveryEmptyFolderIsHostOnlyAndMembershipRefreshIsExplicit(t *testing.T) {
	root := fixtureRoot(t)
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	folder := ref(find(t, list(t, s, "."), "folder"))
	req := request("expand")
	req.Folder = &folder
	host := s.Handle(t.Context(), req)
	if !host.OK || len(host.Resolved.Members) != 0 {
		t.Fatal("empty host folder refused", host.ErrorCode)
	}
	req.Runtime = "sandbox"
	if reply := s.Handle(t.Context(), req); reply.OK {
		t.Fatal("empty sandbox folder accepted")
	}
	fixtureRepo(t, root, "folder/added", false)
	req = request("resolve")
	req.Folder = host.Resolved.Folder
	req.MembershipSnapshot = host.Resolved.MembershipSnapshot
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "stale" {
		t.Fatal("folder silently adopted a new member", reply.ErrorCode)
	}
}
func TestDiscoveryTraversalSymlinksGitdirEscapeAndMetadataHardlinks(t *testing.T) {
	root := fixtureRoot(t)
	outside := fixtureRoot(t)
	fixtureRepo(t, outside, "private-repo", true)
	repo := fixtureRepo(t, root, "repo", true)
	if err := os.Symlink(filepath.Join(outside, "private-repo"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "escape"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "escape", ".git"), []byte("gitdir: "+filepath.Join(outside, "private-repo", ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	page := list(t, s, ".")
	repoRef := ref(find(t, page, "repo"))
	for _, entry := range page.Entries {
		if entry.Path == "link" || entry.Path == "escape" {
			t.Fatal("unsafe source disclosed as a picker row")
		}
	}
	for _, rel := range []string{"../private-repo", "/etc", "repo/../repo", "repo//nested", "repo/.git", "link", "escape"} {
		req := request("list")
		req.Path = rel
		if reply := s.Handle(t.Context(), req); reply.OK {
			t.Fatal("unsafe picker traversal accepted", rel)
		}
	}
	if err := os.Link(filepath.Join(repo, ".git", "HEAD"), filepath.Join(root, "hard-link-head")); err != nil {
		t.Fatal(err)
	}
	if reply := resolve(t, s, repoRef); reply.OK {
		t.Fatal("hard-linked Git metadata accepted")
	}
}
func TestDiscoveryReplyBudgetAndCancellation(t *testing.T) {
	root := fixtureRoot(t)
	parts := make([]string, 28)
	for i := range parts {
		parts[i] = strings.Repeat("d", 98) + fmt.Sprintf("%02d", i)
	}
	rel := strings.Join(parts, "/")
	base := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if err := os.Mkdir(filepath.Join(base, strings.Repeat("n", 190)+fmt.Sprintf("%03d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := New(func() string { return root })
	page := list(t, s, rel)
	if len(page.Entries) >= 500 || page.NextCursor == "" {
		t.Fatal("long names were not bounded by reply bytes")
	}
	raw, _ := json.Marshal(page)
	if len(raw) > maxReply {
		t.Fatal("reply exceeded one MiB")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if reply := s.Handle(ctx, request("list")); reply.OK {
		t.Fatal("canceled operation delivered a listing")
	}
}

func manifestForSelection(v *protocol.DiscoveryResolved, generation string) protocol.WorkspaceManifest {
	m := protocol.WorkspaceManifest{Version: 2, Layout: v.Layout, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: generation, Root: v.Root, Members: []protocol.WorkspaceMember{}}
	if v.Folder != nil || len(v.Members) != 1 {
		m.Kind = "folder"
	}
	paths, _ := workspaceselection.MemberPaths(*v)
	for i, source := range v.Members {
		m.Members = append(m.Members, protocol.WorkspaceMember{ID: source.ID, Name: source.Name, Path: paths[i], SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity})
	}
	m.Digest = workspaceselection.Digest(m)
	return m
}
func TestDiscoveryChildrenKeepPathsAndDoNotEvictActiveParent(t *testing.T) {
	root := fixtureRoot(t)
	fixtureRepo(t, root, "api", false)
	fixtureRepo(t, root, "web", false)
	s := NewOwned(func() string { return root }, "desktop-a")
	page := list(t, s, ".")
	selected := resolve(t, s, ref(find(t, page, "api")), ref(find(t, page, "web")))
	if !selected.OK {
		t.Fatal(selected.ErrorCode)
	}
	parent := manifestForSelection(selected.Resolved, "parent")
	bind := request("bind")
	bind.SelectionIdentity = selected.Resolved.SelectionIdentity
	bind.Manifest = &parent
	if res := s.Handle(t.Context(), bind); !res.OK {
		t.Fatal(res.ErrorCode)
	}
	for i := 0; i < 150; i++ {
		child := parent
		child.LaunchGeneration = fmt.Sprintf("child-%d", i)
		child.Members = append([]protocol.WorkspaceMember(nil), parent.Members[1:]...)
		child.Digest = workspaceselection.Digest(child)
		req := request("bind_child")
		req.ParentLaunchGeneration = parent.LaunchGeneration
		req.Manifest = &child
		if res := s.Handle(t.Context(), req); !res.OK {
			t.Fatalf("child %d: %s", i, res.ErrorCode)
		}
		pins, err := s.PinBound(t.Context(), child)
		if err != nil {
			t.Fatal(err)
		}
		if len(pins.Sources) != 1 || pins.Sources[0].Member.Path != parent.Members[1].Path {
			t.Fatal("child paths changed")
		}
		pins.Close()
		s.Release(child)
	}
	if _, ok := s.bound[parent.LaunchGeneration]; !ok {
		t.Fatal("ended child history evicted active parent")
	}
	forged := parent
	forged.LaunchGeneration = "outside"
	forged.Members = append([]protocol.WorkspaceMember(nil), parent.Members...)
	forged.Members[0].SourceIdentity = "outside"
	forged.Digest = workspaceselection.Digest(forged)
	req := request("bind_child")
	req.ParentLaunchGeneration = parent.LaunchGeneration
	req.Manifest = &forged
	if s.Handle(t.Context(), req).OK {
		t.Fatal("forged child source accepted")
	}
}
func TestDiscoveryIdentitiesAreDesktopQualifiedAndConfigNeverExecutes(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "repo", true)
	sentinel := filepath.Join(root, "unexpected-execution")
	config := fmt.Sprintf("[core]\n repositoryformatversion = 0\n fsmonitor = sh -c 'touch %s'\n[include]\n path = /outside/private-config\n[remote \"origin\"]\n url = https://user:secret@example.invalid/private/repo.git\n", sentinel)
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewOwned(func() string { return root }, "desktop-a")
	b := NewOwned(func() string { return root }, "desktop-b")
	one := find(t, list(t, a, "."), "repo")
	two := find(t, list(t, b, "."), "repo")
	if one.SourceIdentity == two.SourceIdentity {
		t.Fatal("source identity crossed desktop namespace")
	}
	if !resolve(t, a, ref(one)).OK {
		t.Fatal("safe local metadata was unusable")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("discovery executed repository-controlled configuration")
	}
	raw, _ := json.Marshal(list(t, a, "."))
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private-config") {
		t.Fatal("repository config escaped discovery")
	}
}
func TestDiscoverySupportsSHA256GitMetadata(t *testing.T) {
	root := fixtureRoot(t)
	dir := filepath.Join(root, "repo")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "init", "--object-format=sha256", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "add", "README")
	fixtureGit(t, dir, "commit", "-m", "fixture")
	s := New(func() string { return root })
	entry := find(t, list(t, s, "."), "repo")
	if entry.Kind != "repository" || !resolve(t, s, ref(entry)).OK {
		t.Fatal("SHA256 metadata refused")
	}
}

func TestDiscoveryRestoresPrivateHandoffWithoutAdoptingNewRepositories(t *testing.T) {
	root := fixtureRoot(t)
	fixtureRepo(t, root, "group/api", false)
	s := NewOwned(func() string { return root }, "desktop")
	folder := find(t, list(t, s, "."), "group")
	expand := request("expand")
	expand.Folder = &protocol.SelectionRef{Path: folder.Path, SourceIdentity: folder.SourceIdentity}
	selected := s.Handle(t.Context(), expand)
	if !selected.OK {
		t.Fatal(selected.ErrorCode)
	}
	manifest := manifestForSelection(selected.Resolved, "launch")
	bind := request("bind")
	bind.SelectionIdentity = selected.Resolved.SelectionIdentity
	bind.Manifest = &manifest
	if result := s.Handle(t.Context(), bind); !result.OK {
		t.Fatal(result.ErrorCode)
	}
	private, err := s.SnapshotBound(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixtureRepo(t, root, "group/new", false)
	restored := NewOwned(func() string { return root }, "desktop")
	if err := restored.RestoreBound(t.Context(), manifest, private); err != nil {
		t.Fatal(err)
	}
	child := manifest
	child.LaunchGeneration = "child"
	child.Digest = workspaceselection.Digest(child)
	req := request("bind_child")
	req.ParentLaunchGeneration = manifest.LaunchGeneration
	req.Manifest = &child
	if result := restored.Handle(t.Context(), req); !result.OK {
		t.Fatal(result.ErrorCode)
	}
	pins, err := restored.PinBound(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	defer pins.Close()
	if len(pins.Sources) != 1 || pins.Sources[0].Member.Name != "api" {
		t.Fatal("recovery adopted a new repository")
	}
	foreign := NewOwned(func() string { return root }, "other-desktop")
	if err := foreign.RestoreBound(t.Context(), manifest, private); err == nil {
		t.Fatal("private handoff crossed desktop identity")
	}
	if err := os.Rename(filepath.Join(root, "group", "api"), filepath.Join(root, "group", "old-api")); err != nil {
		t.Fatal(err)
	}
	fixtureRepo(t, root, "group/api", false)
	if err := NewOwned(func() string { return root }, "desktop").RestoreBound(t.Context(), manifest, private); err == nil {
		t.Fatal("recovery accepted replacement source")
	}
}

func TestDiscoveryReviewSafetyBoundaries(t *testing.T) {
	if _, err := cleanPath(strings.Repeat("a/", 31) + "a"); err != nil {
		t.Fatal("32 components refused")
	}
	if _, err := cleanPath(strings.Repeat("a/", 32) + "a"); !errors.Is(err, errBudget) {
		t.Fatal("33 components not bounded")
	}
	for _, name := range []string{".git", ".ssh", ".env", "node_modules", "secrets", "credentials"} {
		if _, err := cleanPath(name + "/file"); !errors.Is(err, errInvalid) {
			t.Fatalf("protected query accepted: %s", name)
		}
	}
	op := &operation{ctx: t.Context()}
	for range maxEntries {
		if err := op.visit(); err != nil {
			t.Fatal("visited cap refused early")
		}
	}
	if err := op.visit(); !errors.Is(err, errBudget) {
		t.Fatal("visited cap not enforced")
	}
	root := fixtureRoot(t)
	s := NewOwned(func() string { return root }, "desktop")
	valid := s.cursorFor(cursor{Expires: time.Now().Add(time.Hour).Unix()})
	if _, err := s.readCursor(valid); err != nil {
		t.Fatal(err)
	}
	expired := s.cursorFor(cursor{Expires: time.Now().Unix() - 1})
	if _, err := s.readCursor(expired); !errors.Is(err, errStale) {
		t.Fatal("expired cursor accepted")
	}
	fixtureGit(t, root, "init")
	for _, name := range []string{".git", ".ssh", ".env", "node_modules", "secrets", "credentials"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if page := list(t, s, "."); len(page.Entries) != 0 {
		t.Fatal("protected names listed")
	}
	handle, err := s.openRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.file.Close() }()
	for _, limit := range []int{maxGitFileBytes, maxIndexBytes} {
		file := filepath.Join(root, "metadata")
		if err := os.WriteFile(file, make([]byte, limit), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSmall(handle.file, "metadata", limit); err != nil {
			t.Fatal("metadata at cap refused")
		}
		if err := os.WriteFile(file, make([]byte, limit+1), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSmall(handle.file, "metadata", limit); err == nil {
			t.Fatal("oversized metadata read")
		}
	}
	for _, name := range []string{".git", ".ssh", ".env", "node_modules", "secrets", "credentials"} {
		req := request("list")
		req.Path = name
		reply := s.Handle(t.Context(), req)
		if reply.OK || reply.ErrorCode != "invalid_selection" || reply.Page != nil || reply.Resolved != nil {
			t.Fatal("protected direct query disclosed a source")
		}
	}
	for _, metadata := range []struct {
		name  string
		limit int
	}{{"config", maxGitFileBytes}, {"index", maxIndexBytes}} {
		fresh := fixtureRoot(t)
		repo := fixtureRepo(t, fresh, "repo", false)
		if err := os.WriteFile(filepath.Join(repo, ".git", metadata.name), make([]byte, metadata.limit+1), 0600); err != nil {
			t.Fatal(err)
		}
		service := NewOwned(func() string { return fresh }, "desktop")
		req := request("list")
		req.Path = "repo"
		reply := service.Handle(t.Context(), req)
		if reply.OK || reply.Page != nil || reply.Resolved != nil || reply.ErrorCode != "unavailable" {
			t.Fatal("oversized Git metadata did not fail closed")
		}
	}

	outer := fixtureRoot(t)
	if err := os.Mkdir(filepath.Join(root, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(outer, "link")); err != nil {
		t.Fatal(err)
	}
	escaped := NewOwned(func() string { return filepath.Join(outer, "link", "inside") }, "desktop")
	req := request("list")
	req.Path = "."
	res := escaped.Handle(t.Context(), req)
	if res.OK || res.ErrorCode != "unavailable" {
		t.Fatal("symlinked configured-root ancestor accepted")
	}
}

func TestDiscoveryLayoutIdentityAndBinding(t *testing.T) {
	root := fixtureRoot(t)
	fixtureRepo(t, root, "left/app", false)
	fixtureRepo(t, root, "right/app", false)
	s := NewOwned(func() string { return root }, "desktop")
	refs := []protocol.SelectionRef{ref(find(t, list(t, s, "left"), "left/app")), ref(find(t, list(t, s, "right"), "right/app"))}
	var identities []string
	for _, layout := range []protocol.WorkspaceLayout{"", protocol.WorkspaceLayoutNames, protocol.WorkspaceLayoutStructure} {
		req := request("resolve")
		req.Members = refs
		req.Layout = layout
		selected := s.Handle(t.Context(), req)
		if !selected.OK {
			t.Fatal(selected)
		}
		for _, identity := range identities {
			if identity == selected.Resolved.SelectionIdentity {
				t.Fatal("layout missing from selection identity")
			}
		}
		identities = append(identities, selected.Resolved.SelectionIdentity)
		m := manifestForSelection(selected.Resolved, fmt.Sprintf("launch-%d", len(identities)))
		bind := request("bind")
		bind.SelectionIdentity = selected.Resolved.SelectionIdentity
		bind.Manifest = &m
		if reply := s.Handle(t.Context(), bind); !reply.OK {
			t.Fatal(reply)
		}
		m.Layout = protocol.WorkspaceLayout("invalid")
		m.Digest = workspaceselection.Digest(m)
		if reply := s.Handle(t.Context(), bind); reply.OK {
			t.Fatal("invalid manifest layout accepted")
		}
		m.Layout = protocol.WorkspaceLayoutNames
		if layout == m.Layout {
			m.Layout = protocol.WorkspaceLayoutStructure
		}
		m.Digest = workspaceselection.Digest(m)
		if reply := s.Handle(t.Context(), bind); reply.OK {
			t.Fatal("manifest changed resolved layout")
		}
	}
	req := request("resolve")
	req.Members = refs
	req.Layout = "invalid"
	if res := s.Handle(t.Context(), req); res.OK {
		t.Fatal("unknown layout accepted")
	}
}
