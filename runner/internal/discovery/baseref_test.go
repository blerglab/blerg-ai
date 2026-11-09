//go:build linux || darwin

package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func resolveBaseReq(launchGeneration string, folder protocol.SelectionRef, branch string) protocol.DiscoverWorkspace {
	req := request("resolve_base")
	req.ParentLaunchGeneration = launchGeneration
	req.Folder = &folder
	req.Base = branch
	return req
}

// bindSingleMember runs the same "resolve" + "bind" round trip
// workspace_selection.go's prepareWorkspaceSelection does for an explicit, single-member host
// selection, including MemberPaths' own collapse of a single member's public Path to "."
// (design §13) — so a resolve_base test here exercises the exact same manifest shape
// cluster_host_workspace.go sends, not a hand-built one. Returns the manifest's
// LaunchGeneration, for resolveBaseReq's ParentLaunchGeneration, and the member's real,
// on-disk SelectionRef (as opposed to the manifest's own collapsed one).
func bindSingleMember(t *testing.T, s *Service, realRef protocol.SelectionRef) (launchGeneration string) {
	t.Helper()
	res := resolve(t, s, realRef)
	if !res.OK {
		t.Fatalf("resolve refused: %s", res.ErrorCode)
	}
	v := res.Resolved
	paths, _ := workspaceselection.MemberPaths(*v)
	source := v.Members[0]
	m := protocol.WorkspaceManifest{Version: 2, Layout: v.Layout, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch-" + source.ID, Root: v.Root,
		Members: []protocol.WorkspaceMember{{ID: source.ID, Name: source.Name, Path: paths[0], SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity}}}
	m.Digest = workspaceselection.Digest(m)
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	bindReq := request("bind")
	bindReq.SelectionIdentity = v.SelectionIdentity
	bindReq.Manifest = &m
	bres := s.Handle(t.Context(), bindReq)
	if !bres.OK {
		t.Fatalf("bind refused: %s", bres.ErrorCode)
	}
	return m.LaunchGeneration
}

// newResolveBaseFixture is the general (non-root) case: the daemon's configured root holds
// one repository, "app", in a subdirectory — the member's real on-disk path and its
// manifest's own collapsed Path happen to differ ("app" vs "."), exactly as they would for
// Main's own desktop (a root holding several repositories, one of them selected).
func newResolveBaseFixture(t *testing.T) (root, dir string, s *Service, entry protocol.DiscoveryEntry, launchGeneration string) {
	t.Helper()
	root = fixtureRoot(t)
	dir = fixtureRepo(t, root, "app", true)
	s = New(func() string { return root })
	page := list(t, s, ".")
	entry = find(t, page, "app")
	launchGeneration = bindSingleMember(t, s, ref(entry))
	return
}

// TestResolveBaseResolvesLocalCommit is design §10's own shape: resolve_base answers with
// Main's own local commit for the requested branch, by reading a ref — nothing about
// reachability or history is decided here (that moved server-side, cluster_host_workspace.go).
func TestResolveBaseResolvesLocalCommit(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "commit", "--allow-empty", "-qm", "local work")
	localSHA := fixtureGit(t, dir, "rev-parse", "HEAD")

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if !res.OK {
		t.Fatalf("resolve_base refused: %s", res.ErrorCode)
	}
	if res.BaseSHA != localSHA {
		t.Fatalf("got BaseSHA %q, want the local commit %q", res.BaseSHA, localSHA)
	}
}

// A branch that doesn't exist locally at all is refused, not guessed at.
func TestResolveBaseUnknownBranchRefuses(t *testing.T) {
	_, _, s, entry, launchGeneration := newResolveBaseFixture(t)
	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "no/such/branch"))
	if res.OK {
		t.Fatal("expected a refusal for a branch that does not exist locally")
	}
}

// A hostile branch name (shaped like a git flag, or escaping refs/heads) never reaches the
// filesystem beneath root.
func TestResolveBaseRefusesHostileBranchName(t *testing.T) {
	_, _, s, entry, launchGeneration := newResolveBaseFixture(t)
	for _, hostile := range []string{"--upload-pack=evil", "../../etc/passwd", "a\x00b", "a\nb"} {
		res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), hostile))
		if res.OK {
			t.Fatalf("hostile branch %q was accepted", hostile)
		}
	}
}

// TestResolveBaseNeverOpensObjects is the round-3 regression: an alternates file plus a
// missing object. resolve_base must still return the ref's own SHA, because it never opens
// anything under objects/ — existence and reachability are the server's job (the provider's
// compare API), never this daemon's.
func TestResolveBaseNeverOpensObjects(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "commit", "--allow-empty", "-qm", "local work")
	localSHA := fixtureGit(t, dir, "rev-parse", "HEAD")

	// A dangling alternates file: if resolveBase ever opened an object, this would make git
	// itself try (and fail) to satisfy it from a nonexistent alternate.
	infoDir := filepath.Join(dir, ".git", "objects", "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "alternates"), []byte(filepath.Join(t.TempDir(), "nonexistent")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Remove the actual commit object: the ref still names it, but nothing can resolve it.
	objPath := filepath.Join(dir, ".git", "objects", localSHA[:2], localSHA[2:])
	if err := os.Remove(objPath); err != nil {
		t.Fatalf("remove fixture object: %v", err)
	}

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if !res.OK {
		t.Fatalf("resolve_base refused: %s", res.ErrorCode)
	}
	if res.BaseSHA != localSHA {
		t.Fatalf("got BaseSHA %q, want the ref's own commit %q (dangling, never opened)", res.BaseSHA, localSHA)
	}
}

// A loose ref file that is actually a symlink is refused outright — openBeneath's
// RESOLVE_NO_SYMLINKS containment, exercised here through the full resolve_base path.
func TestResolveBaseRefusesSymlinkedLooseRef(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	refPath := filepath.Join(dir, ".git", "refs", "heads", "alpha", "0.6.0")
	if err := os.Remove(refPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", refPath); err != nil {
		t.Fatal(err)
	}

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if res.OK {
		t.Fatal("a symlinked loose ref must be refused")
	}
}

// A symlinked intermediate directory under refs/heads (a different escape shape than a
// symlinked leaf ref file) is refused the same way.
func TestResolveBaseRefusesSymlinkedRefDirectory(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	aliasDir := filepath.Join(dir, ".git", "refs", "heads", "escape")
	if err := os.Symlink(filepath.Join(dir, ".git", "refs", "heads", "alpha"), aliasDir); err != nil {
		t.Fatal(err)
	}

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "escape/0.6.0"))
	if res.OK {
		t.Fatal("a ref path through a symlinked directory must be refused")
	}
}

// With no loose ref file at all, resolve_base falls back to packed-refs.
func TestResolveBasePackedRefsOnly(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "commit", "--allow-empty", "-qm", "local work")
	localSHA := fixtureGit(t, dir, "rev-parse", "HEAD")
	fixtureGit(t, dir, "pack-refs", "--all")
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "alpha", "0.6.0")); !os.IsNotExist(err) {
		t.Fatal("fixture: loose ref should have been packed away")
	}

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if !res.OK {
		t.Fatalf("resolve_base refused: %s", res.ErrorCode)
	}
	if res.BaseSHA != localSHA {
		t.Fatalf("got BaseSHA %q from packed-refs, want %q", res.BaseSHA, localSHA)
	}
}

// A loose ref overrides a stale packed-refs entry for the same name — matching git's own
// precedence.
func TestResolveBaseLooseOverridesPacked(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "pack-refs", "--all")
	fixtureGit(t, dir, "commit", "--allow-empty", "-qm", "moved after packing")
	newSHA := fixtureGit(t, dir, "rev-parse", "HEAD")
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "alpha", "0.6.0")); err != nil {
		t.Fatalf("fixture: expected a fresh loose ref after the new commit: %v", err)
	}

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if !res.OK {
		t.Fatalf("resolve_base refused: %s", res.ErrorCode)
	}
	if res.BaseSHA != newSHA {
		t.Fatalf("got BaseSHA %q, want the loose ref's own (newer) commit %q, not the stale packed one", res.BaseSHA, newSHA)
	}
}

// An oversized packed-refs file is refused rather than read.
func TestResolveBaseRefusesOversizedPackedRefs(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	sha := fixtureGit(t, dir, "rev-parse", "HEAD")
	fixtureGit(t, dir, "pack-refs", "--all")
	target := filepath.Join(dir, ".git", "packed-refs")
	var b strings.Builder
	b.WriteString("# pack-refs with: peeled fully-peeled sorted\n")
	b.WriteString(sha + " refs/heads/alpha/0.6.0\n")
	padding := strings.Repeat("#", 1<<20)
	for b.Len() <= maxPackedRefsBytes {
		b.WriteString(padding)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(target, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if res.OK {
		t.Fatal("an oversized packed-refs file must be refused, not read")
	}
}

// TestResolveBaseNeverOpensConfigOrObjectsDirectory is the round-4 regression: deny all
// access to .git/config and .git/objects. If resolve_base (or anything it calls) ever
// tried to open either — as safeDirectory/inspectRepository's full repository inspection
// used to, before this round moved resolve_base onto resolveRepositoryIdentity's
// identity-only path — this would fail with a permission error instead of succeeding.
func TestResolveBaseNeverOpensConfigOrObjectsDirectory(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "commit", "--allow-empty", "-qm", "local work")
	localSHA := fixtureGit(t, dir, "rev-parse", "HEAD")

	configPath := filepath.Join(dir, ".git", "config")
	objectsPath := filepath.Join(dir, ".git", "objects")
	if err := os.Chmod(configPath, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(objectsPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(configPath, 0o600)
		_ = os.Chmod(objectsPath, 0o700)
	})

	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0"))
	if !res.OK {
		t.Fatalf("resolve_base refused: %s (it must never need config or objects/ access)", res.ErrorCode)
	}
	if res.BaseSHA != localSHA {
		t.Fatalf("got BaseSHA %q, want %q", res.BaseSHA, localSHA)
	}
}

// A leading '/' or an empty path segment in --base is refused before it ever reaches a
// path.Join, matching the same shape check the server already applies before this value
// is sent here (server/workspace_child.go's validBaseBranch).
func TestResolveBaseRefusesLeadingSlashAndEmptySegment(t *testing.T) {
	_, _, s, entry, launchGeneration := newResolveBaseFixture(t)
	for _, hostile := range []string{"/etc/passwd", "/alpha/0.6.0", "alpha//0.6.0", "alpha/0.6.0/", "/"} {
		res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, ref(entry), hostile))
		if res.OK {
			t.Fatalf("hostile branch %q was accepted", hostile)
		}
	}
}

// TestResolveBaseSingleMemberCollapsedPathAgainstRoot is the live e2e regression (design
// §13): the daemon's configured root holds SEVERAL repositories (Main's own desktop layout),
// and only one, "blerg", is selected as a single-member host workspace. Because
// workspace_selection.go's MemberPaths collapses a single member's public manifest Path to
// "." for display, the manifest sent over the wire names "." — which is the daemon's *root*,
// not the repository. Before this round, resolveBase opened req.Folder.Path directly, found
// no .git at the root, and refused with the same ambiguous "invalid_selection" every other
// check in this function shared. It must now recover the real on-disk path ("blerg") from
// its own bound selection instead.
func TestResolveBaseSingleMemberCollapsedPathAgainstRoot(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "blerg", true)
	fixtureRepo(t, root, "blerg-release", true)
	s := New(func() string { return root })

	page := list(t, s, ".")
	entry := find(t, page, "blerg")
	launchGeneration := bindSingleMember(t, s, ref(entry))

	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "commit", "--allow-empty", "-qm", "local work")
	localSHA := fixtureGit(t, dir, "rev-parse", "HEAD")

	// The manifest's own (collapsed, display) Path — "." — exactly as
	// cluster_host_workspace.go's plan struct carries it from parentManifest.Members.
	collapsed := protocol.SelectionRef{Path: ".", SourceIdentity: entry.SourceIdentity}
	res := s.Handle(t.Context(), resolveBaseReq(launchGeneration, collapsed, "alpha/0.6.0"))
	if !res.OK {
		t.Fatalf("resolve_base refused: %s (cause=%s)", res.ErrorCode, res.Cause)
	}
	if res.BaseSHA != localSHA {
		t.Fatalf("got BaseSHA %q, want %q", res.BaseSHA, localSHA)
	}
}

// TestResolveBaseRequiresParentLaunchGeneration and
// TestResolveBaseRequiresKnownParentLaunchGeneration are the two new failure sites §13 added
// around the bound-selection lookup; each carries its own cause (checked below), never the
// older ones' "invalid_selection" alone.
func TestResolveBaseRequiresParentLaunchGeneration(t *testing.T) {
	_, _, s, entry, _ := newResolveBaseFixture(t)
	res := s.Handle(t.Context(), resolveBaseReq("", ref(entry), "alpha/0.6.0"))
	if res.OK {
		t.Fatal("resolve_base must refuse without a ParentLaunchGeneration")
	}
	if res.Cause != "resolve_base:no_parent_launch" {
		t.Fatalf("got cause %q, want %q", res.Cause, "resolve_base:no_parent_launch")
	}
}
func TestResolveBaseRequiresKnownParentLaunchGeneration(t *testing.T) {
	_, _, s, entry, _ := newResolveBaseFixture(t)
	res := s.Handle(t.Context(), resolveBaseReq("no-such-launch", ref(entry), "alpha/0.6.0"))
	if res.OK {
		t.Fatal("resolve_base must refuse an unknown ParentLaunchGeneration")
	}
	if res.Cause != "resolve_base:unknown_parent_launch" {
		t.Fatalf("got cause %q, want %q", res.Cause, "resolve_base:unknown_parent_launch")
	}
}

// TestResolveBaseCausesAreDistinct is §13's own instruction: every refusal site in this
// function (and the helpers it calls) must log a distinct, fixed cause — never the same
// ambiguous "invalid_selection" string every one of them used to share.
func TestResolveBaseCausesAreDistinct(t *testing.T) {
	_, dir, s, entry, launchGeneration := newResolveBaseFixture(t)
	fixtureGit(t, dir, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, dir, "pack-refs", "--all")

	cases := []struct {
		name  string
		req   protocol.DiscoverWorkspace
		cause string
	}{
		{"no folder", func() protocol.DiscoverWorkspace {
			r := resolveBaseReq(launchGeneration, ref(entry), "alpha/0.6.0")
			r.Folder = nil
			return r
		}(), "resolve_base:no_folder_selection"},
		{"hostile branch", resolveBaseReq(launchGeneration, ref(entry), "--upload-pack=evil"), "resolve_base:hostile_branch_shape"},
		{"unknown member", resolveBaseReq(launchGeneration, protocol.SelectionRef{Path: entry.Path, SourceIdentity: "not-the-real-one"}, "alpha/0.6.0"), "resolve_base:unknown_member"},
		{"unknown branch, present in neither loose nor packed refs", resolveBaseReq(launchGeneration, ref(entry), "no/such/branch"), "resolve_base:ref_not_found"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		res := s.Handle(t.Context(), c.req)
		if res.OK {
			t.Fatalf("%s: expected a refusal", c.name)
		}
		if res.Cause != c.cause {
			t.Fatalf("%s: got cause %q, want %q", c.name, res.Cause, c.cause)
		}
		if seen[res.Cause] {
			t.Fatalf("%s: cause %q was already used by another case — not distinct", c.name, res.Cause)
		}
		seen[res.Cause] = true
	}
}
