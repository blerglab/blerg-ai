package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func explorerFixture(t *testing.T) (*LocalExplorer, string, protocol.WorkspaceExplorerRequest) {
	t.Helper()
	root := t.TempDir()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.WorkspaceManifest{LaunchGeneration: "launch", Runtime: "host", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "member"}}}
	e, err := newLocalExplorer(root, info, manifest, 1, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	e.statusDiagnostic = func(stage string, err error, stderr string) {
		t.Logf("workspace explorer unavailable: stage=%q err=%v stderr=%q", stage, err, stderr)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, root, protocol.WorkspaceExplorerRequest{SessionID: "session", RequestID: "request", Operation: "list", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}}
}
func writeExplorerFile(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceExplorerRootAndGenerations(t *testing.T) {
	e, root, req := explorerFixture(t)
	writeExplorerFile(t, root, "visible.txt")
	if res := e.Explore(context.Background(), req); !res.OK || len(res.Entries) != 1 {
		t.Fatalf("list: %+v", res)
	}
	for _, bad := range []protocol.WorkspaceExplorerRequest{
		{Operation: "list", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "old", Generation: 1}},
		{Operation: "list", WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 2}},
	} {
		if res := e.Explore(context.Background(), bad); res.OK {
			t.Fatal("stale binding accepted")
		}
	}
	moved := root + "-old"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeExplorerFile(t, root, "replacement.txt")
	if res := e.Explore(context.Background(), req); res.OK || len(res.Entries) != 0 {
		t.Fatal("replaced root delivered data")
	}
}
func TestWorkspaceExplorerProtectedChildrenAndLinks(t *testing.T) {
	e, root, req := explorerFixture(t)
	writeExplorerFile(t, root, "visible.txt")
	for _, name := range []string{".env", "credentials.json", "token.key"} {
		writeExplorerFile(t, root, name)
	}
	for _, name := range []string{".git", ".ssh", ".env.local", "secrets"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		writeExplorerFile(t, root, filepath.Join(name, "leak.txt"))
	}
	if err := os.Symlink("visible.txt", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "visible.txt"), filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	res := e.Explore(context.Background(), req)
	if !res.OK || len(res.Entries) != 0 {
		t.Fatalf("protected files or hard links disclosed: %+v", res)
	}
	req.Operation = "search"
	req.Filter = "leak"
	res = e.Explore(context.Background(), req)
	if !res.OK || len(res.Entries) != 0 {
		t.Fatalf("search: %+v", res)
	}
	req.Operation = "list"
	req.Path = ".env.local"
	if res = e.Explore(context.Background(), req); res.OK {
		t.Fatal("protected directory enumerated")
	}
}
func TestWorkspaceExplorerCursorBindingsAndChanges(t *testing.T) {
	e, root, req := explorerFixture(t)
	for i := 0; i < 505; i++ {
		writeExplorerFile(t, root, fmt.Sprintf("file-%04d.txt", i))
	}
	first := e.Explore(context.Background(), req)
	if !first.OK || len(first.Entries) != 500 || first.Cursor == "" || !first.Truncated {
		t.Fatalf("first page: ok=%v entries=%d cursor=%v", first.OK, len(first.Entries), first.Cursor != "")
	}
	req.Cursor = first.Cursor
	second := e.Explore(context.Background(), req)
	if !second.OK || len(second.Entries) != 5 || second.Truncated {
		t.Fatalf("second page: %+v", second)
	}
	req.Filter = "file"
	if res := e.Explore(context.Background(), req); res.ErrorCode != "stale" {
		t.Fatal("cursor filter replay accepted")
	}
	req.Filter = ""
	if err := os.WriteFile(filepath.Join(root, "file-0004.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if res := e.Explore(context.Background(), req); res.ErrorCode != "stale" {
		t.Fatal("changed child metadata accepted cursor")
	}
	req.Filter = ""
	writeExplorerFile(t, root, "new.txt")
	if res := e.Explore(context.Background(), req); res.ErrorCode != "stale" {
		t.Fatal("changed directory accepted cursor")
	}
	req.Cursor = "forged"
	if res := e.Explore(context.Background(), req); res.ErrorCode != "stale" {
		t.Fatal("forged cursor accepted")
	}
}
func TestWorkspaceExplorerSearchCancelAndBounds(t *testing.T) {
	e, root, req := explorerFixture(t)
	for i := 0; i < 205; i++ {
		writeExplorerFile(t, root, fmt.Sprintf("hit-%04d", i))
	}
	req.Operation = "search"
	req.Filter = "hit"
	res := e.Explore(context.Background(), req)
	if !res.OK || len(res.Entries) != 200 || !res.Truncated {
		t.Fatalf("hits: %+v", res)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res = e.Explore(ctx, req); res.OK || res.ErrorCode != "cancelled" {
		t.Fatal("cancelled search delivered data")
	}
	dir := root
	for i := 0; i < 34; i++ {
		dir = filepath.Join(dir, "d")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeExplorerFile(t, dir, "too-deep.txt")
	req.Filter = "too-deep"
	res = e.Explore(context.Background(), req)
	if !res.OK || len(res.Entries) != 0 || !res.Truncated {
		t.Fatal("search depth not bounded")
	}
}
func TestWorkspaceExplorerDiscoveryDoesNotAdopt(t *testing.T) {
	e, root, req := explorerFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "new-repo", ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	res := e.Explore(context.Background(), req)
	if !res.OK || len(res.Entries) != 1 || !res.Entries[0].Repository || res.Entries[0].MemberID != "" {
		t.Fatalf("discovery: %+v", res)
	}
	if len(e.manifest.Members) != 1 || e.manifest.Members[0].ID != "one" {
		t.Fatal("discovery adopted repository")
	}
	e.manifest.Runtime = "sandbox"
	res = e.Explore(context.Background(), req)
	if !res.OK || res.Entries[0].Repository {
		t.Fatal("sandbox scanned host-only discovery")
	}
}
func TestWorkspaceStatusHostileBinaryRecords(t *testing.T) {
	raw := "# branch.head feature/<script>\x00? normal.txt\x00? line\nwith\ttab.txt\x00? .env\x00? .env.local/leak\x00? normal.txt\x00? bad\xff.txt\x00"
	status, err := parseWorkspaceStatus(raw)
	if err != nil || status.Untracked != 2 || status.Changed != 0 || len(status.Paths) != 2 {
		t.Fatalf("parse: %+v %v", status, err)
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), "<script>") || strings.Contains(string(encoded), "line\n") {
		t.Fatal("binary names were not escaped as text")
	}
	for _, bad := range []string{"? missing-terminator", "2 malformed\x00", "1 too-short\x00"} {
		if _, err := parseWorkspaceStatus(bad); err == nil {
			t.Fatal("malformed status implied clean")
		}
	}
	var large strings.Builder
	for i := 0; i < 10002; i++ {
		fmt.Fprintf(&large, "? file-%d\x00", i)
	}
	status, err = parseWorkspaceStatus(large.String())
	if err != nil || len(status.Paths) != 10000 || !status.Partial {
		t.Fatal("status path budget not enforced")
	}
}
func TestWorkspaceExplorerConcurrentRename(t *testing.T) {
	e, root, req := explorerFixture(t)
	outside := t.TempDir()
	writeExplorerFile(t, outside, "outside.txt")
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Rename(filepath.Join(root, "child"), filepath.Join(root, "saved"))
			_ = os.Symlink(outside, filepath.Join(root, "child"))
			_ = os.Remove(filepath.Join(root, "child"))
			_ = os.Rename(filepath.Join(root, "saved"), filepath.Join(root, "child"))
		}
	}()
	for i := 0; i < 40; i++ {
		req.Operation = "search"
		req.Filter = "outside"
		res := e.Explore(context.Background(), req)
		if len(res.Entries) > 0 {
			close(stop)
			wg.Wait()
			t.Fatal("symlink race escaped root")
		}
	}
	close(stop)
	wg.Wait()
}
func TestWorkspaceExplorerGitStatusSafety(t *testing.T) {

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	e, root, req := explorerFixture(t)
	repo := filepath.Join(root, "member")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	writeExplorerFile(t, repo, "new.txt")
	writeExplorerFile(t, repo, ".env")
	marker := filepath.Join(root, "hook-ran")
	config := "\n[core]\n fsmonitor = touch " + marker + "\n[include]\n path = /nonexistent/explorer-test-config\n"
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(config)
	_ = f.Close()
	e.pinMembers() // bind after the fake runtime has prepared Git
	req.Operation = "status"
	req.Path = "member"
	req.MemberID = "one"
	res := e.Explore(context.Background(), req)
	if !res.OK || res.Status == nil || res.Status.Untracked != 1 || res.Status.Paths[0] != "new.txt" {
		t.Fatalf("status: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Git executed fsmonitor")
	}
	outside := t.TempDir()
	writeExplorerFile(t, outside, "index")
	if err := os.Symlink(filepath.Join(outside, "index"), filepath.Join(repo, ".git", "index")); err != nil {
		t.Fatal(err)
	}
	if res = e.Explore(context.Background(), req); res.OK {
		t.Fatal("symlinked index accepted")
	}
	if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, "old-git")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "init", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("replacement Git: %v %s", err, out)
	}
	if res = e.Explore(context.Background(), req); res.OK {
		t.Fatal("replaced launch Git metadata accepted")
	}

}

func TestWorkspaceExplorerIntermediateSymlink(t *testing.T) {
	e, root, req := explorerFixture(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeExplorerFile(t, outside, "nested/outside.txt")
	if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	req.Path = "alias/nested"
	if result := e.Explore(context.Background(), req); result.OK || len(result.Entries) > 0 {
		t.Fatal("intermediate directory symlink traversed")
	}
	if result := readWorkspaceFile(root, "alias/nested/outside.txt", workspaceFileLimits{expectedRoot: e.info}); result.OK {
		t.Fatal("file read followed intermediate symlink")
	}
}

func TestWorkspaceExplorerHostWorktreePins(t *testing.T) {

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	e, root, req := explorerFixture(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v %s", err, out)
		}
	}
	git("init", source)
	git("-C", source, "-c", "user.name=Example", "-c", "user.email=agent@example.com", "commit", "--allow-empty", "-m", "fixture")
	git("-C", source, "worktree", "add", "-b", "fixture-branch", filepath.Join(root, "member"))
	e.pinMembers() // fake preparation completes before publishing its binding
	if len(e.worktrees) != 1 {
		t.Fatal("registered worktree was not pinned")
	}
	writeExplorerFile(t, filepath.Join(root, "member"), "new.txt")
	req.Operation = "status"
	req.Path = "member"
	req.MemberID = "one"
	result := e.Explore(context.Background(), req)
	if !result.OK || result.Status == nil || result.Status.Branch != "fixture-branch" || result.Status.Untracked != 1 {
		t.Fatalf("registered worktree status: %+v", result)
	}
	// A planted unregistered pointer must never borrow the launch-time exception.
	if err := os.WriteFile(filepath.Join(root, "member", ".git"), []byte("gitdir: "+source+"/.git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result = e.Explore(context.Background(), req); result.OK {
		t.Fatal("switched host Git pointer accepted")
	}
}

func TestWorkspaceExplorerStatusNeverRunsCleanFilters(t *testing.T) {

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	e, root, req := explorerFixture(t)
	repo := filepath.Join(root, "member")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v %s", err, out)
		}
	}
	git("init", repo)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=hostile\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", repo, "add", "tracked.txt", ".gitattributes")
	git("-C", repo, "-c", "user.name=Example", "-c", "user.email=agent@example.com", "commit", "-m", "fixture")
	marker := filepath.Join(root, "filter-ran")
	file, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[filter \"hostile\"]\nclean = touch " + marker + "; cat\n")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e.pinMembers()
	req.Operation = "status"
	req.Path = "member"
	req.MemberID = "one"
	result := e.Explore(context.Background(), req)
	if !result.OK || result.Status == nil || result.Status.Changed != 1 {
		t.Fatalf("safe tracked status: %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("status executed a repository clean filter")
	}
}

func TestWorkspaceExplorerMemberDescriptorSurvivesNamespaceSwap(t *testing.T) {
	e, root, req := explorerFixture(t)
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "file.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	e.pinMembers()
	saved := filepath.Join(root, "saved")
	if err := os.Rename(member, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "file.txt"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	// The open primitive itself must remain on the pinned member even during an
	// ABA replacement, rather than depending solely on pathname checks around I/O.
	file, err := e.openFile("member/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(raw) != "original" {
		t.Fatal("member open followed replacement namespace")
	}
	req.Path = "member"
	if result := e.Explore(context.Background(), req); result.OK {
		t.Fatal("replaced member delivered a listing")
	}
	if result := e.ReadFile(context.Background(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, Path: "./member/file.txt"}); result.OK {
		t.Fatal("normalized file path bypassed member fence")
	}
	if err := os.RemoveAll(member); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, member); err != nil {
		t.Fatal(err)
	}
	result := e.ReadFile(context.Background(), protocol.ReadWorkspaceFile{WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, Path: "./member/file.txt"})
	if !result.OK || result.Path != "member/file.txt" {
		t.Fatalf("restored pinned member: %+v", result)
	}
}

func TestWorkspaceExplorerLongDirectoryCursorStaysBounded(t *testing.T) {
	e, _, req := explorerFixture(t)
	relative := workspaceLongDirectoryFixture(t, e)
	req.Path = relative
	started := time.Now()
	first := e.Explore(context.Background(), req)
	if !first.OK || len(first.Entries) == 0 || len(first.Entries) > 500 || first.Cursor == "" || len(first.Cursor) > 8192 {
		t.Fatalf("long-path page not bounded: ok=%v error=%q entries=%d cursor=%d path=%d elapsed=%v", first.OK, first.ErrorCode, len(first.Entries), len(first.Cursor), len(relative), time.Since(started))
	}
	payload, err := json.Marshal(map[string]any{"result": first, "members": e.manifest.Members, "runtime": e.manifest.Runtime})
	if err != nil || len(payload)+1 > protocol.WorkspaceExplorerReplyBytes {
		t.Fatal("escaped response envelope exceeded reply budget")
	}
	req.Cursor = first.Cursor
	second := e.Explore(context.Background(), req)
	if !second.OK || len(second.Entries) == 0 || second.Entries[0].Name <= first.Entries[len(first.Entries)-1].Name {
		t.Fatalf("bounded long-path cursor did not resume: ok=%v error=%q entries=%d elapsed=%v", second.OK, second.ErrorCode, len(second.Entries), time.Since(started))
	}
}

func workspaceLongDirectoryFixture(t *testing.T, e *LocalExplorer) string {
	t.Helper()
	relative := ""
	for i := 0; i < 15; i++ {
		relative = path.Join(relative, strings.Repeat("<", 200))
		if err := e.root.Mkdir(relative, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 501; i++ {
		file, err := e.root.OpenFile(path.Join(relative, fmt.Sprintf("file-%04d", i)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("fixture")); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		_ = file.Close()
	}
	return relative
}

func TestWorkspaceStatusFileCodes(t *testing.T) {
	raw := "1 .M N... 100644 100644 100644 abc abc src/changed.txt\x00" +
		"1 A. N... 100644 100644 100644 abc abc added.txt\x00" +
		"1 D. N... 100644 100644 100644 abc abc removed.txt\x00" +
		"u UU N... 100644 100644 100644 100644 abc abc abc conflict.txt\x00" +
		"? new-directory/\x00? .env\x00"
	status, err := parseWorkspaceStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"M", "A", "D", "C", "U"}
	if len(status.Files) != len(want) || len(status.Files) != len(status.Paths) {
		t.Fatalf("missing file codes: %+v", status)
	}
	for i, code := range want {
		if status.Files[i].Code != code || status.Files[i].Path != status.Paths[i] {
			t.Fatalf("file %d: %+v", i, status.Files[i])
		}
	}
	if !status.Files[4].Directory || status.Files[4].Path != "new-directory" {
		t.Fatalf("untracked directory lost: %+v", status.Files[4])
	}
	if _, err := parseWorkspaceStatus("1 XX N... 100644 100644 100644 abc abc bad.txt\x00"); err == nil {
		t.Fatal("invalid status code accepted")
	}
	var large strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&large, "? %04d%s.txt\x00", i, strings.Repeat("a", 800))
	}
	bounded, err := parseWorkspaceStatus(large.String())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(bounded)
	if err != nil || !bounded.Partial || len(encoded) >= protocol.WorkspaceExplorerReplyBytes || len(bounded.Files) != len(bounded.Paths) {
		t.Fatalf("unbounded decorations: bytes=%d partial=%v err=%v", len(encoded), bounded.Partial, err)
	}
}

func TestWorkspaceStatusDetachedHead(t *testing.T) {
	for _, head := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		status, err := parseWorkspaceStatus("# branch.oid " + head + "\x00# branch.head (detached)\x00")
		if err != nil {
			t.Fatal(err)
		}
		if status.Head != head || status.Branch != "(detached)" {
			t.Fatalf("detached commit lost: %+v", status)
		}
	}
	if status, err := parseWorkspaceStatus("# branch.oid (initial)\x00# branch.head main\x00"); err != nil || status.Head != "" || status.Branch != "main" {
		t.Fatalf("unborn branch refused: %+v %v", status, err)
	}
	for _, head := range []string{"", "short", strings.Repeat("g", 40), strings.Repeat("a", 65)} {
		if _, err := parseWorkspaceStatus("# branch.oid " + head + "\x00"); err == nil {
			t.Fatalf("invalid object ID accepted: %q", head)
		}
	}
}
