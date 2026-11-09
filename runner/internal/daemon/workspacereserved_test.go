package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func reservedGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	raw, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}

func TestReservedExcludeIsIdempotentAndOnlyRemovesItsOwn(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initChildRepo(t, repo)
	childCommit(t, repo, "file.txt")
	git := filepath.Join(repo, ".git")
	exclude := filepath.Join(git, "info", "exclude")
	if err := os.WriteFile(exclude, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := addReservedExclude(git, repo); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(exclude)
	if strings.Count(string(raw), ".workspace/") != 1 || !strings.HasPrefix(string(raw), "keep-me\n") {
		t.Fatalf("exclude %q", raw)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".workspace", "two"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".workspace", "two", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := reservedGit(t, repo, "status", "--porcelain"); out != "" {
		t.Fatalf("Git sees the folder: %s", out)
	}
	if err := removeReservedExclude(git, repo); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(exclude); string(raw) != "keep-me\n" {
		t.Fatalf("exclude %q", raw)
	}

	// An entry the person wrote is theirs, and it outlives the last repository.
	if err := os.WriteFile(exclude, []byte(".workspace/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addReservedExclude(git, repo); err != nil {
		t.Fatal(err)
	}
	if err := removeReservedExclude(git, repo); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(exclude); string(raw) != ".workspace/\n" {
		t.Fatalf("the person's entry changed: %q", raw)
	}
}

func TestReservedExcludeReachesALinkedWorktree(t *testing.T) {
	base := t.TempDir()
	repo, linked := filepath.Join(base, "repo"), filepath.Join(base, "linked")
	initChildRepo(t, repo)
	childCommit(t, repo, "file.txt")
	reservedGit(t, repo, "worktree", "add", "-q", "-b", "linked", linked)
	common := reservedGit(t, linked, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err := addReservedExclude(common, linked); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(linked, ".workspace", "two"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".workspace", "two", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := reservedGit(t, linked, "status", "--porcelain"); out != "" {
		t.Fatalf("the linked worktree sees the folder: %s", out)
	}
	if want := reservedGit(t, linked, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"); want != filepath.Join(common, "info", "exclude") {
		t.Fatalf("Git reads %s", want)
	}
}

func TestReservedExcludeRefusesASymlink(t *testing.T) {
	git := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(git, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(git, "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	if err := addReservedExclude(git, git); err == nil {
		t.Fatal("a linked exclude was written through")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "x\n" {
		t.Fatalf("outside file changed: %q", raw)
	}
}

// reservedHost turns the host fixture into a one-repository workspace in a real
// repository, and the addition into a real second repository.
func reservedHost(t *testing.T) *membershipFixture {
	t.Helper()
	f := membershipSetup(t)
	first, second := f.w.state.Root, f.pins.dir
	for _, dir := range []string{first, second} {
		if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		initChildRepo(t, dir)
		childCommit(t, dir, "README.md")
	}
	root := f.w.state.Launch
	root.Members = []protocol.WorkspaceMember{root.Members[0]}
	root.Members[0].Path = "."
	root.Digest = workspaceselection.Digest(root)
	common, err := workspaceDirectoryIdentity(filepath.Join(first, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	f.w.state.Launch = root
	f.w.state.Sources[0].Member, f.w.state.Sources[0].CommonIdentity = root.Members[0], common
	f.w.state.Members[0].Member = root.Members[0]
	b := *f.w.state.Binding
	b.ManifestDigest = root.Digest
	f.w.state.Binding = &b
	if err := f.w.save(); err != nil {
		t.Fatal(err)
	}
	next := f.msg.Proposed
	next.Members = append([]protocol.WorkspaceMember{root.Members[0]}, next.Members[1])
	next.Members[1].Path = ".workspace/three"
	next.Digest = workspaceselection.Digest(next)
	f.msg.Proposed, f.msg.OldDigest = next, root.Digest
	f.server.proposed = next
	return f
}

func (f *membershipFixture) reservedRemoval() protocol.WorkspaceMembershipApply {
	f.t.Helper()
	next := f.w.state.Launch
	next.Revision++
	next.Members = next.Members[:1]
	next.Digest = workspaceselection.Digest(next)
	f.server.proposed, f.server.phase = next, "authorized"
	return protocol.WorkspaceMembershipApply{SessionID: "session", OperationID: "operation-2", Kind: "remove", MemberID: "three", Proposed: next, OldDigest: f.w.state.Launch.Digest}
}

func TestReservedHostAddAndRemoveLeaveTheRepositoryClean(t *testing.T) {
	f := reservedHost(t)
	first := f.w.state.Root
	before := reservedGit(t, first, "rev-parse", "HEAD")
	f.service.ApplyMembership(t.Context(), f.msg)
	state := f.saved()
	if state.Membership != nil || len(state.Sources) != 2 || state.Sources[1].Path != f.pins.dir {
		t.Fatalf("state %+v reports %+v", state.Membership, f.server.reports)
	}
	if got := reservedGit(t, filepath.Join(first, ".workspace", "three"), "rev-parse", "--show-toplevel"); got != f.pins.dir {
		t.Fatalf("the path reaches %s", got)
	}
	if out := reservedGit(t, first, "status", "--porcelain"); out != "" {
		t.Fatalf("Git sees the folder: %s", out)
	}
	if reservedGit(t, first, "rev-parse", "HEAD") != before {
		t.Fatal("something was committed")
	}
	if !hostStateVirtual(state) {
		t.Fatal("a reserved member must read through its own source")
	}

	f.service.ApplyMembership(t.Context(), f.reservedRemoval())
	state = f.saved()
	if state.Membership != nil || len(state.Members) != 1 || len(state.ReservedRetired) != 0 {
		t.Fatalf("state %+v retired %+v reports %+v", state.Membership, state.ReservedRetired, f.server.reports)
	}
	if _, err := os.Lstat(filepath.Join(first, ".workspace")); !os.IsNotExist(err) {
		t.Fatalf("the folder stayed: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(first, ".git", "info", "exclude")); strings.Contains(string(raw), ".workspace") {
		t.Fatalf("exclude kept the entry: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(f.pins.dir, "README.md")); err != nil {
		t.Fatal("removal touched the checkout", err)
	}
}

func TestReservedHostLinkedWorktreeUsesTheCommonExclude(t *testing.T) {
	f := reservedHost(t)
	first := f.w.state.Root
	linked := filepath.Join(t.TempDir(), "linked")
	reservedGit(t, first, "worktree", "add", "-q", "-b", "linked", linked)
	identity, err := workspaceDirectoryIdentity(linked)
	if err != nil {
		t.Fatal(err)
	}
	f.w.state.Root, f.w.state.RootIdentity = linked, identity
	f.w.state.Sources[0].Path, f.w.state.Sources[0].RootIdentity = linked, identity
	if err := f.w.save(); err != nil {
		t.Fatal(err)
	}
	f.service.ApplyMembership(t.Context(), f.msg)
	if out := reservedGit(t, linked, "status", "--porcelain"); out != "" {
		t.Fatalf("the worktree sees the folder: %s; reports %+v", out, f.server.reports)
	}
	if _, err := os.Lstat(filepath.Join(linked, ".git", "info")); err == nil {
		t.Fatal("wrote through a .git file")
	}
}

func TestReservedHostRefusesWhatIsNotOurs(t *testing.T) {
	outside := t.TempDir()
	for name, prepare := range map[string]func(root string) error{
		"a file at the folder":    func(root string) error { return os.WriteFile(filepath.Join(root, ".workspace"), nil, 0o600) },
		"a link at the folder":    func(root string) error { return os.Symlink(outside, filepath.Join(root, ".workspace")) },
		"the person's own folder": func(root string) error { return os.MkdirAll(filepath.Join(root, ".workspace", "theirs"), 0o700) },
		"a folder at the name":    func(root string) error { return os.MkdirAll(filepath.Join(root, ".workspace", "three"), 0o700) },
		"a link to elsewhere": func(root string) error {
			if err := os.Mkdir(filepath.Join(root, ".workspace"), 0o700); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(root, ".workspace", "three"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := reservedHost(t)
			if err := prepare(f.w.state.Root); err != nil {
				t.Fatal(err)
			}
			f.service.ApplyMembership(t.Context(), f.msg)
			if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseCollision {
				t.Fatalf("reports %+v", f.server.reports)
			}
			if state := f.saved(); state.Membership != nil || len(state.Members) != 1 {
				t.Fatalf("state %+v", state.Membership)
			}
			if f.writers.thaws != 1 {
				t.Fatalf("thaws %d", f.writers.thaws)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatal("wrote through a link")
			}
		})
	}
}

func TestReservedHostRefusedCommitLeavesNothing(t *testing.T) {
	f := reservedHost(t)
	f.server.commitErr = &WorkspaceRefusedError{}
	f.service.ApplyMembership(t.Context(), f.msg)
	first := f.w.state.Root
	if _, err := os.Lstat(filepath.Join(first, ".workspace")); !os.IsNotExist(err) {
		t.Fatalf("the folder stayed: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(first, ".git", "info", "exclude")); strings.Contains(string(raw), ".workspace") {
		t.Fatalf("exclude kept the entry: %q", raw)
	}
}

func TestReservedHostCrashAtEachPhase(t *testing.T) {
	for _, c := range []struct {
		stage     string
		committed bool
	}{
		{"membership-fencing", false},
		{"membership-fenced", false},
		{"membership-prepared", false},
		{"membership-after-commit", true},
		{"membership-state-saved", true},
		{"membership-live-installed", true},
		{"membership-journal-closed", true},
	} {
		t.Run(c.stage, func(t *testing.T) {
			f := reservedHost(t)
			first := f.w.state.Root
			type crash struct{}
			workspaceCrashHook = func(stage string) {
				if stage == c.stage {
					panic(crash{})
				}
			}
			defer func() { workspaceCrashHook = nil }()
			func() {
				defer func() {
					if v := recover(); v != nil {
						if _, ok := v.(crash); !ok {
							panic(v)
						}
					}
				}()
				f.service.ApplyMembership(t.Context(), f.msg)
			}()
			workspaceCrashHook = nil
			if _, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record); err != nil {
				t.Fatal(err)
			}
			_, err := os.Lstat(filepath.Join(first, ".workspace", "three"))
			if c.committed && err != nil {
				t.Fatalf("a committed member lost its path: %v", err)
			}
			if !c.committed && !os.IsNotExist(err) {
				t.Fatalf("an unpublished change left its path: %v", err)
			}
			if out := reservedGit(t, first, "status", "--porcelain"); out != "" {
				t.Fatalf("Git sees the folder: %s", out)
			}
		})
	}
}

func TestReservedHostCrashDuringRemovalStillClears(t *testing.T) {
	f := reservedHost(t)
	first := f.w.state.Root
	f.service.ApplyMembership(t.Context(), f.msg)
	removal := f.reservedRemoval()
	type crash struct{}
	workspaceCrashHook = func(stage string) {
		if stage == "membership-state-saved" {
			panic(crash{})
		}
	}
	defer func() { workspaceCrashHook = nil }()
	func() {
		defer func() {
			if _, ok := recover().(crash); !ok {
				t.Error("no crash")
			}
		}()
		f.service.ApplyMembership(t.Context(), removal)
	}()
	workspaceCrashHook = nil
	state, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ReservedRetired) != 0 || len(state.Members) != 1 {
		t.Fatalf("state %+v", state.ReservedRetired)
	}
	if _, err := os.Lstat(filepath.Join(first, ".workspace")); !os.IsNotExist(err) {
		t.Fatalf("the folder stayed: %v", err)
	}
}

func reservedExplorer(t *testing.T, f *membershipFixture) *VirtualHostExplorer {
	t.Helper()
	state := f.saved()
	sources := map[string]HostWorkspaceSource{}
	for _, source := range state.Sources {
		sources[source.Member.ID] = HostWorkspaceSource{Path: source.Path, Root: source.RootIdentity}
	}
	e, err := NewVirtualHostExplorer(state.Root, state.Launch, *state.Binding, sources, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestReservedHostReaderShowsAMemberNotAFolder(t *testing.T) {
	f := reservedHost(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	if err := os.WriteFile(filepath.Join(f.w.state.Root, ".dotfile"), []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := reservedExplorer(t, f)
	state := f.saved()
	request := func(operation, name, filter string) protocol.WorkspaceExplorerResult {
		t.Helper()
		result := e.Explore(t.Context(), protocol.WorkspaceExplorerRequest{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: state.Launch.LaunchGeneration, Generation: state.Binding.Generation}, Operation: operation, Path: name, Filter: filter})
		if !result.OK {
			t.Fatalf("%s %s: %s", operation, name, result.ErrorCode)
		}
		return result
	}
	list := request("list", "", "")
	var names []string
	for _, row := range list.Entries {
		names = append(names, row.Name+"="+row.Path)
		if row.Name == "three" && (!row.Repository || row.MemberID != "three") {
			t.Fatalf("row %+v", row)
		}
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "three=.workspace/three") || !strings.Contains(joined, "README.md=README.md") || strings.Contains(joined, ".workspace=") {
		t.Fatalf("list %s", joined)
	}
	if rows := request("list", ".workspace/three", "").Entries; len(rows) == 0 || rows[0].Path != ".workspace/three/README.md" {
		t.Fatalf("member list %+v", rows)
	}
	// Revealing a file walks its parents, so the folder still answers when asked.
	request("list", ".workspace", "")
	found := map[string]bool{}
	for _, row := range request("search", "", "README").Entries {
		found[row.Path] = true
	}
	if !found["README.md"] || !found[".workspace/three/README.md"] {
		t.Fatalf("search %v", found)
	}
	if status := request("status", ".workspace/three", ""); status.Status == nil {
		t.Fatalf("status %+v", status)
	}
	read := e.ReadFile(t.Context(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: state.Launch.LaunchGeneration, Generation: state.Binding.Generation}, Path: ".dotfile"})
	if !read.OK {
		t.Fatalf("a dotted root file was misrouted: %+v", read)
	}
	read = e.ReadFile(t.Context(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: state.Launch.LaunchGeneration, Generation: state.Binding.Generation}, Path: ".workspace/three/README.md"})
	if !read.OK {
		t.Fatalf("member read: %+v", read)
	}
}

// reservedSandbox is a real allocation with one repository at its root, gaining
// a real second repository in the reserved folder.
func reservedSandbox(t *testing.T) *sandboxMembership {
	t.Helper()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	repo := filepath.Join(t.TempDir(), "one")
	initChildRepo(t, repo)
	childCommit(t, repo, "source-one.txt")
	member := protocol.WorkspaceMember{ID: "one", Name: "one", Path: ".", SourceIdentity: "source-one", CommonDirectoryIdentity: "common-one", Destination: "destination-one"}
	launch := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "generation", Root: protocol.WorkspaceRoot{Inode: 1}, Digest: "trusted-digest", Members: []protocol.WorkspaceMember{member}}
	root, err := workspaceDirectoryIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	common, err := workspaceDirectoryIdentity(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "checkout")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := PrepareLocalWorkspace(t.Context(), "p2-session", launch, []WorkspaceSource{{Member: member, Path: repo, RootIdentity: root, CommonIdentity: common}}, storage)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &testWorkspaceRuntime{w: w}
	if _, err := w.BindRuntime(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	f := sandboxMembershipFrom(t, w, runtime)
	f.msg.Proposed.Kind = "folder"
	f.msg.Proposed.Members[1].Path = ".workspace/three"
	f.msg.Proposed.Digest = workspaceselection.Digest(f.msg.Proposed)
	f.server.proposed = f.msg.Proposed
	f.clone = filepath.Join(w.state.Root, ".workspace", "three")
	return f
}

func TestReservedSandboxAddDeliversAndStaysOutOfTheFirstRepository(t *testing.T) {
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	root := f.w.state.Root
	state := f.saved()
	if state.Membership != nil || len(state.Members) != 2 || state.Members[1].Clone != f.clone {
		t.Fatalf("state %+v reports %+v", state.Membership, f.server.reports)
	}
	if out := reservedGit(t, root, "status", "--porcelain"); out != "" {
		t.Fatalf("Git sees the folder: %s", out)
	}
	childCommit(t, f.clone, "from-the-agent.txt")
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if head, ok := readCloneRef(f.source, "refs/heads/"+f.w.branch()); !ok || head != f.w.state.Members[1].Result.Head {
		t.Fatalf("delivery %q %v %+v", head, ok, f.w.state.Members[1].Result)
	}
	if err := f.w.validateAllocation(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The clone's info/exclude is the agent's file. Settlement must hold without it.
func TestReservedSandboxSettleTrustsTheJournalNotTheClone(t *testing.T) {
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	root := f.w.state.Root
	if err := os.Remove(filepath.Join(root, ".git", "info", "exclude")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "work.txt"), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".workspace", "stray"), []byte("agent work"), 0o600); err != nil {
		t.Fatal(err)
	}
	member := f.w.state.Members[0]
	clean, err := settleWorkspaceRoot(t.Context(), &member, reservedPaths(f.w.state), f.w.state.Session, f.w.branch())
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Fatal("the other member's clone counted as unsaved work")
	}
	tree := reservedGit(t, root, "ls-tree", "-r", "--name-only", "refs/heads/"+f.w.branch())
	if strings.Contains(tree, "three") || !strings.Contains(tree, "work.txt") {
		t.Fatalf("snapshot %s", tree)
	}
	// Work beside the members is still work: only the exact paths are excluded.
	if !strings.Contains(tree, ".workspace/stray") {
		t.Fatalf("a stray file was hidden from settlement: %s", tree)
	}
}

func TestReservedSandboxRemoveClearsAndCanComeBack(t *testing.T) {
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	root := f.w.state.Root
	added := f.msg
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	removal := sandboxRemoval(f, "three")
	f.service.ApplyMembership(t.Context(), removal)
	state := f.saved()
	if state.Membership != nil || len(state.Members) != 1 || len(state.ReservedRetired) != 0 {
		t.Fatalf("state %+v retired %+v reports %+v", state.Membership, state.ReservedRetired, f.server.reports)
	}
	if _, err := os.Lstat(filepath.Join(root, ".workspace")); !os.IsNotExist(err) {
		t.Fatalf("the folder stayed: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude")); strings.Contains(string(raw), ".workspace") {
		t.Fatalf("exclude kept the entry: %q", raw)
	}
	back := removal.Proposed
	back.Revision++
	back.Kind = "folder"
	back.Members = append(append([]protocol.WorkspaceMember(nil), back.Members...), added.Proposed.Members[1])
	back.Digest = workspaceselection.Digest(back)
	f.server.proposed, f.server.phase = back, "authorized"
	added.OperationID, added.Proposed, added.OldDigest = "operation-3", back, state.Launch.Digest
	f.service.ApplyMembership(t.Context(), added)
	if state = f.saved(); len(state.Members) != 2 {
		t.Fatalf("re-add %+v", f.server.reports)
	}
}

func TestReservedSandboxRefusesAgentFolders(t *testing.T) {
	for name, prepare := range map[string]func(root string) error{
		"a link at the folder": func(root string) error { return os.Symlink(t.TempDir(), filepath.Join(root, ".workspace")) },
		"a folder at the name": func(root string) error { return os.MkdirAll(filepath.Join(root, ".workspace", "three"), 0o700) },
		"another folder":       func(root string) error { return os.MkdirAll(filepath.Join(root, ".workspace", "theirs"), 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			f := reservedSandbox(t)
			if err := prepare(f.w.state.Root); err != nil {
				t.Fatal(err)
			}
			f.service.ApplyMembership(t.Context(), f.msg)
			if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseCollision || f.runtime.thaw != 1 {
				t.Fatalf("reports %+v thaws %d", f.server.reports, f.runtime.thaw)
			}
		})
	}
}

func TestReservedSandboxCrashAtEachPhase(t *testing.T) {
	for _, c := range []struct {
		stage     string
		committed bool
	}{
		{"membership-fencing", false},
		{"membership-cloned", false},
		{"membership-prepared", false},
		{"membership-after-commit", true},
		{"membership-state-saved", true},
		{"membership-journal-closed", true},
	} {
		t.Run(c.stage, func(t *testing.T) {
			f := reservedSandbox(t)
			type crash struct{}
			workspaceCrashHook = func(stage string) {
				if stage == c.stage {
					panic(crash{})
				}
			}
			defer func() { workspaceCrashHook = nil }()
			func() {
				defer func() {
					if v := recover(); v != nil {
						if _, ok := v.(crash); !ok {
							panic(v)
						}
					}
				}()
				f.service.ApplyMembership(t.Context(), f.msg)
			}()
			workspaceCrashHook = nil
			state, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record)
			if err != nil {
				t.Fatal(err)
			}
			// A prepared clone with no member stays excluded until it is cleared.
			if _, statErr := os.Lstat(f.clone); !c.committed && statErr == nil {
				found := false
				for _, path := range reservedPaths(state) {
					found = found || path == ".workspace/three"
				}
				if !found {
					t.Fatal("an orphan clone is not excluded from settlement")
				}
			}
		})
	}
}

// A worker gets the same layout in its own worktrees: its reader sees real paths.
func TestReservedHostWorkerNestsItsWorktrees(t *testing.T) {
	f := reservedHost(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	parent := f.saved()
	if parent.Membership != nil || len(parent.Sources) != 2 {
		t.Fatalf("add: %+v", f.server.reports)
	}
	child := parent.Launch
	child.LaunchGeneration = "child-launch"
	if err := protocol.ValidateWorkspaceChild(parent.Launch, child); err != nil {
		t.Fatal(err)
	}
	parent.Launch = child
	w := hostChildAllocationFixture(t, parent)
	nested := filepath.Join(w.state.Root, ".workspace", "three")
	if got := reservedGit(t, nested, "rev-parse", "--show-toplevel"); got != nested {
		t.Fatalf("nested worktree at %s", got)
	}
	if got := reservedGit(t, nested, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != parent.Sources[1].CommonPath {
		t.Fatalf("worktree of %s", got)
	}
	if out := reservedGit(t, w.state.Root, "status", "--porcelain"); out != "" {
		t.Fatalf("the worker's first repository sees the folder: %s", out)
	}
	if hostStateVirtual(w.state) {
		t.Fatal("a worker's worktrees are physical")
	}
	if len(w.state.HostChildren) != 2 || w.state.HostChildren[1].Path != nested {
		t.Fatalf("allocations %+v", w.state.HostChildren)
	}
}

func TestReservedHostWorkerWithOnlyTheAddedRepository(t *testing.T) {
	f := reservedHost(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	parent := f.saved()
	child := parent.Launch
	child.LaunchGeneration = "child-launch"
	child.Members = child.Members[1:]
	if err := protocol.ValidateWorkspaceChild(parent.Launch, child); err != nil {
		t.Fatal(err)
	}
	parent.Launch = child
	w := hostChildAllocationFixture(t, parent)
	nested := filepath.Join(w.state.Root, ".workspace", "three")
	if got := reservedGit(t, nested, "rev-parse", "--show-toplevel"); got != nested {
		t.Fatalf("worktree at %s", got)
	}
}

// A sandbox worker is prepared from the whole selection, not added to one.
func TestReservedSandboxPreparesBothAtLaunch(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	base := t.TempDir()
	launch := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "generation", Root: protocol.WorkspaceRoot{Inode: 1}, Digest: "trusted-digest"}
	var sources []WorkspaceSource
	for _, c := range []struct{ id, path string }{{"one", "."}, {"three", ".workspace/three"}} {
		repo := filepath.Join(base, c.id)
		initChildRepo(t, repo)
		childCommit(t, repo, c.id+".txt")
		member := protocol.WorkspaceMember{ID: c.id, Name: c.id, Path: c.path, SourceIdentity: "source-" + c.id, CommonDirectoryIdentity: "common-" + c.id, Destination: "destination-" + c.id}
		root, err := workspaceDirectoryIdentity(repo)
		if err != nil {
			t.Fatal(err)
		}
		common, err := workspaceDirectoryIdentity(filepath.Join(repo, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		launch.Members = append(launch.Members, member)
		sources = append(sources, WorkspaceSource{Member: member, Path: repo, RootIdentity: root, CommonIdentity: common})
	}
	storage := &testWorkspaceStorage{root: filepath.Join(base, "checkout")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := PrepareLocalWorkspace(t.Context(), "p2-session", launch, sources, storage)
	if err != nil {
		t.Fatal(err)
	}
	root := w.state.Root
	if out := reservedGit(t, root, "status", "--porcelain"); out != "" {
		t.Fatalf("Git sees the folder: %s", out)
	}
	if got := reservedGit(t, filepath.Join(root, ".workspace", "three"), "rev-parse", "--show-toplevel"); got != filepath.Join(root, ".workspace", "three") {
		t.Fatalf("clone at %s", got)
	}
}

// Both worktrees share one info/exclude. Each counts as an owner of the entry, so
// the first to leave cannot expose the other's folder to Git.
func TestReservedExcludeOutlivesOneOfTwoWorktrees(t *testing.T) {
	base := t.TempDir()
	repo, linked := filepath.Join(base, "repo"), filepath.Join(base, "linked")
	initChildRepo(t, repo)
	childCommit(t, repo, "file.txt")
	reservedGit(t, repo, "worktree", "add", "-q", "-b", "linked", linked)
	git := filepath.Join(repo, ".git")
	for _, root := range []string{repo, linked, linked} {
		if err := addReservedExclude(git, root); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{repo, linked} {
		if err := os.MkdirAll(filepath.Join(root, ".workspace", "two"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".workspace", "two", "x"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := releaseReservedFolder(linked, git); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(linked, ".workspace")); err != nil {
		t.Fatal(err)
	}
	if err := releaseReservedFolder(linked, git); err != nil {
		t.Fatal(err)
	}
	if out := reservedGit(t, repo, "status", "--porcelain"); out != "" {
		t.Fatalf("the other worktree lost its exclusion: %s", out)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".workspace")); err != nil {
		t.Fatal(err)
	}
	if err := releaseReservedFolder(repo, git); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(git, "info", "exclude")); strings.Contains(string(raw), "Blerg") || strings.Contains(string(raw), ".workspace") {
		t.Fatalf("the last owner left the entry: %q", raw)
	}
}

// A delivered, clean clone can still hold what the sandbox never delivered.
func reservedDelivered(t *testing.T) *sandboxMembership {
	t.Helper()
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.clone, ".git", "info", "exclude"), []byte("cache/\n.env.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.clone, "cache", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env.local", "cache/deep/blob"} {
		if err := os.WriteFile(filepath.Join(f.clone, name), []byte("local"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestReservedSandboxRemoveRefusesIgnoredFilesAndNamesThem(t *testing.T) {
	f := reservedDelivered(t)
	f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "three"))
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseLocal {
		t.Fatalf("reports %+v", f.server.reports)
	}
	local := f.server.reports[0].Local
	if local == nil || local.Count != 2 || local.Digest == "" || strings.Join(local.Names, " ") != ".env.local cache/" {
		t.Fatalf("local %+v", local)
	}
	state := f.saved()
	if state.Membership != nil || len(state.Members) != 2 || f.runtime.frozen {
		t.Fatalf("membership %+v members %d frozen %v", state.Membership, len(state.Members), f.runtime.frozen)
	}
	if raw, err := os.ReadFile(filepath.Join(f.clone, "cache", "deep", "blob")); err != nil || string(raw) != "local" {
		t.Fatalf("the ignored file is gone: %q %v", raw, err)
	}
}

func TestReservedSandboxDiscardsExactlyTheConfirmedFiles(t *testing.T) {
	f := reservedDelivered(t)
	f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "three"))
	digest := f.server.reports[0].Local.Digest

	// The agent writes after the person saw the list: the approval no longer fits.
	if err := os.WriteFile(filepath.Join(f.clone, "cache", "deep", "newer"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := sandboxRemoval(f, "three")
	stale.DiscardLocal = digest
	f.service.ApplyMembership(t.Context(), stale)
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseLocal || f.server.reports[0].Local.Digest == digest {
		t.Fatalf("a stale confirmation was honored: %+v", f.server.reports)
	}
	if _, err := os.Stat(filepath.Join(f.clone, "cache", "deep", "newer")); err != nil {
		t.Fatal(err)
	}

	current := f.server.reports[0].Local.Digest
	confirmed := sandboxRemoval(f, "three")
	confirmed.DiscardLocal = current
	f.service.ApplyMembership(t.Context(), confirmed)
	state := f.saved()
	if state.Membership != nil || len(state.Members) != 1 || len(state.ReservedRetired) != 0 {
		t.Fatalf("state %+v retired %+v reports %+v", state.Membership, state.ReservedRetired, f.server.reports)
	}
	if _, err := os.Lstat(f.clone); !os.IsNotExist(err) {
		t.Fatalf("the confirmed clone stayed: %v", err)
	}
}

// Git status hides a file the index marks unchanged, and a populated submodule
// under --ignore-submodules. Neither is proof.
func TestReservedSandboxRemoveRefusesWhatStatusHides(t *testing.T) {
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.clone, "source-three.txt"), []byte("hidden edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	reservedGit(t, f.clone, "update-index", "--assume-unchanged", "source-three.txt")
	f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "three"))
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseWork {
		t.Fatalf("reports %+v", f.server.reports)
	}
	if raw, _ := os.ReadFile(filepath.Join(f.clone, "source-three.txt")); string(raw) != "hidden edit" {
		t.Fatalf("the edit is gone: %q", raw)
	}
}

// A clone that outlives its removal stays reachable by the agent, so deleting it
// later proves everything again rather than trusting the earlier check.
func TestReservedSandboxRetiredCloneIsProvedAgainBeforeDeletion(t *testing.T) {
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	type crash struct{}
	workspaceCrashHook = func(stage string) {
		if stage == "membership-state-saved" {
			panic(crash{})
		}
	}
	defer func() { workspaceCrashHook = nil }()
	func() {
		defer func() {
			if _, ok := recover().(crash); !ok {
				t.Error("no crash")
			}
		}()
		f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "three"))
	}()
	workspaceCrashHook = nil
	state, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record)
	if err != nil || len(state.ReservedRetired) != 1 {
		t.Fatalf("retired %+v %v", state.ReservedRetired, err)
	}
	f.w.state = state
	if err := os.WriteFile(filepath.Join(f.clone, ".git", "info", "exclude"), []byte("late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.clone, "late"), []byte("written after the check"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.w.retireReserved(t.Context()); err == nil {
		t.Fatal("a clone with new files was retired")
	}
	if _, err := os.Stat(filepath.Join(f.clone, "late")); err != nil {
		t.Fatal("deleted without proof: ", err)
	}
	if retired := f.saved().ReservedRetired; len(retired) != 1 {
		t.Fatalf("the record was dropped: %+v", retired)
	}
	if err := os.Remove(filepath.Join(f.clone, "late")); err != nil {
		t.Fatal(err)
	}
	if err := f.w.retireReserved(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f.clone); !os.IsNotExist(err) {
		t.Fatalf("the proved clone stayed: %v", err)
	}
}
