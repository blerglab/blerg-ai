package daemon

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func commitFixture(t *testing.T) (string, workspaceFileBinding) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "init", "-q", "--initial-branch=main")
	return dir, originBinding(t, dir)
}

func commitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "add", "--", name)
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", message)
	return originGit(t, dir, "rev-parse", "HEAD")
}

func summary(t *testing.T, b workspaceFileBinding, sha string) protocol.CommitResult {
	t.Helper()
	return readWorkspaceCommit(t.Context(), "session-1", b, sha, false)
}

func TestCommitLookupNormalRootAndMergeCommits(t *testing.T) {
	dir, b := commitFixture(t)
	root := commitFile(t, dir, "README.md", "one\ntwo\n", "Root commit")
	second := commitFile(t, dir, "src/app.go", "package app\n", "Add the app\n\nIt does a thing.\nOn two lines.")
	res := summary(t, b, second[:7])
	if !res.OK || res.Commit == nil {
		t.Fatalf("normal commit: %+v", res)
	}
	c := res.Commit
	if c.SHA != second || c.Short != second[:7] || c.Subject != "Add the app" || c.Body != "It does a thing.\nOn two lines." || c.Author != "Fixture" {
		t.Fatalf("normal commit fields: %+v", c)
	}
	if _, err := time.Parse(time.RFC3339, c.AuthorDate); err != nil {
		t.Fatalf("author date %q", c.AuthorDate)
	}
	if len(c.Parents) != 1 || c.Parents[0] != root {
		t.Fatalf("parents %v", c.Parents)
	}
	if len(c.Files) != 1 || c.Files[0] != (protocol.CommitFile{Path: "src/app.go", Adds: 1}) || c.Adds != 1 || c.Dels != 0 {
		t.Fatalf("files %+v", c.Files)
	}

	res = summary(t, b, root)
	if !res.OK || len(res.Commit.Parents) != 0 || len(res.Commit.Files) != 1 || res.Commit.Files[0].Path != "README.md" || res.Commit.Adds != 2 {
		t.Fatalf("root commit: %+v", res.Commit)
	}

	originGit(t, dir, "checkout", "-qb", "side")
	commitFile(t, dir, "side.txt", "s\n", "Side work")
	originGit(t, dir, "checkout", "-q", "main")
	commitFile(t, dir, "README.md", "one\n", "Trim the readme")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "merge", "-q", "--no-edit", "side")
	merge := originGit(t, dir, "rev-parse", "HEAD")
	res = summary(t, b, merge)
	if !res.OK || len(res.Commit.Parents) != 2 || !strings.HasPrefix(res.Commit.Subject, "Merge branch") {
		t.Fatalf("merge commit: %+v", res.Commit)
	}
	// Against its first parent: what the merge brought in.
	if len(res.Commit.Files) != 1 || res.Commit.Files[0].Path != "side.txt" {
		t.Fatalf("merge files: %+v", res.Commit.Files)
	}

	originGit(t, dir, "mv", "side.txt", "moved.txt")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Rename")
	res = summary(t, b, originGit(t, dir, "rev-parse", "HEAD"))
	if !res.OK || len(res.Commit.Files) != 1 || res.Commit.Files[0] != (protocol.CommitFile{Path: "moved.txt", OldPath: "side.txt"}) {
		t.Fatalf("rename: %+v", res.Commit.Files)
	}

	patch := readWorkspaceCommit(t.Context(), "session-1", b, second, true)
	raw, err := base64.StdEncoding.DecodeString(patch.PatchB64)
	if !patch.OK || err != nil {
		t.Fatalf("patch: %+v", patch)
	}
	text := string(raw)
	for _, want := range []string{"commit " + second + "\n", "Author: Fixture <fixture@example.test>\n", "    Add the app\n", "diff --git a/src/app.go b/src/app.go\n", "+package app\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("patch lacks %q:\n%s", want, text)
		}
	}
}

func TestCommitLookupRefusesAnythingButHex(t *testing.T) {
	dir, b := commitFixture(t)
	commitFile(t, dir, "a.txt", "a\n", "A")
	marker := filepath.Join(t.TempDir(), "written")
	for _, in := range []string{
		"", "abc12", "HEAD", "HEAD~1", "main", "ABCDEF1234", "abcdef1..abcdef2", "abcdef1^{tree}",
		"--output=" + marker, "--", "-n", "--all", "-p", "abcdef1 --", " abcdef1", "abcdef1\n",
		"../../etc", strings.Repeat("a", 65), "abcdefg",
	} {
		// An invalid value is refused before the workspace is even looked at.
		if res := readWorkspaceCommit(t.Context(), "s", workspaceFileBinding{}, in, false); res.OK || res.ErrorCode != "invalid" {
			t.Fatalf("%q: %+v", in, res)
		}
		if res := readWorkspaceCommit(t.Context(), "s", b, in, true); res.OK || res.ErrorCode != "invalid" {
			t.Fatalf("%q (patch): %+v", in, res)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("an option-looking sha reached git")
	}
	if res := summary(t, b, "deadbeefdeadbeef"); res.OK || res.ErrorCode != "not_found" {
		t.Fatalf("unknown sha: %+v", res)
	}
	// A tree or blob id is not a commit.
	blob := originGit(t, dir, "rev-parse", "HEAD:a.txt")
	if res := summary(t, b, blob); res.ErrorCode != "not_found" {
		t.Fatalf("blob: %+v", res)
	}
}

func TestCommitLookupUnavailableWorkspaces(t *testing.T) {
	_, b := commitFixture(t)
	if res := summary(t, b, "abcdef1"); res.ErrorCode != "not_found" {
		t.Fatalf("empty repository: %+v", res)
	}
	b.live = func() bool { return false }
	if res := summary(t, b, "abcdef1"); res.ErrorCode != "unavailable" {
		t.Fatalf("ended launch: %+v", res)
	}
	plain := t.TempDir()
	if res := summary(t, originBinding(t, plain), "abcdef1"); res.ErrorCode != "unavailable" {
		t.Fatalf("not a repository: %+v", res)
	}
	// A workspace replaced after the launch bound it is not read.
	dir, b := commitFixture(t)
	sha := commitFile(t, dir, "a.txt", "a\n", "A")
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "init", "-q")
	if res := summary(t, b, sha); res.ErrorCode != "unavailable" {
		t.Fatalf("replaced workspace: %+v", res)
	}
}

func TestCommitLookupBoundsOutput(t *testing.T) {
	dir, b := commitFixture(t)
	var big strings.Builder
	for big.Len() <= protocol.CommitPatchMaxBytes {
		big.WriteString("a line of a large generated file, repeated until the diff passes its cap\n")
	}
	for i := 0; i < protocol.CommitFilesMax+20; i++ {
		name := filepath.Join(dir, "many", "f"+strconv.Itoa(i)+".txt")
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "add", ".")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Huge\n\n"+strings.Repeat("body text ", 1000))
	sha := originGit(t, dir, "rev-parse", "HEAD")
	res := summary(t, b, sha)
	if !res.OK {
		t.Fatalf("huge summary: %+v", res)
	}
	c := res.Commit
	if len(c.Files) != protocol.CommitFilesMax || !c.FilesTruncated || !c.BodyTruncated || len(c.Body) > protocol.CommitBodyMaxBytes {
		t.Fatalf("huge commit not bounded: files=%d truncated=%t body=%d", len(c.Files), c.FilesTruncated, len(c.Body))
	}
	if c.Adds != protocol.CommitFilesMax+20+strings.Count(big.String(), "\n") {
		t.Fatalf("totals count every file: %d", c.Adds)
	}
	if res := readWorkspaceCommit(t.Context(), "s", b, sha, true); res.OK || res.ErrorCode != "too_large" || res.PatchB64 != "" {
		t.Fatalf("huge patch: ok=%t code=%s len=%d", res.OK, res.ErrorCode, len(res.PatchB64))
	}
}

// The repository's own configuration cannot run a command: no external diff, no text conversion,
// no pager, no signature check, no fsmonitor, no hook.
func TestCommitLookupIgnoresRepositoryCommands(t *testing.T) {
	dir, b := commitFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"diff.external", script}, {"diff.evil.textconv", script}, {"diff.evil.command", script}, {"core.pager", script},
		{"pager.show", script}, {"log.showSignature", "true"}, {"gpg.program", script}, {"core.fsmonitor", script},
		{"core.hooksPath", filepath.Dir(script)}, {"diff.noprefix", "true"}, {"core.sshCommand", script},
	} {
		originGit(t, dir, "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt diff=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "add", ".")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "commit", "-qm", "A")
	sha := originGit(t, dir, "-c", "core.fsmonitor=false", "rev-parse", "HEAD")
	_ = os.Remove(marker)
	res := summary(t, b, sha)
	patch := readWorkspaceCommit(t.Context(), "s", b, sha, true)
	if !res.OK || !patch.OK {
		t.Fatalf("reads failed: %+v %+v", res.ErrorCode, patch.ErrorCode)
	}
	raw, _ := base64.StdEncoding.DecodeString(patch.PatchB64)
	if !strings.Contains(string(raw), "diff --git a/a.txt b/a.txt") || !strings.Contains(string(raw), "+a\n") {
		t.Fatalf("patch not plain:\n%s", raw)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a command from the repository's configuration ran")
	}
}

// A sandboxed session's clone is never read by host git: its commits are read from the
// repository they are brought back to.
func TestCommitLookupSandboxReadsTheRepositoryNotTheClone(t *testing.T) {
	id := "commit-lookup-sandbox"
	repo, clone := cloneFixture(t, id)
	base := childRepoGit(t, repo, "rev-parse", "HEAD")
	childCommit(t, clone, "only-in-clone.txt")
	inClone := childRepoGit(t, clone, "rev-parse", "HEAD")
	info, err := os.Stat(clone)
	if err != nil {
		t.Fatal(err)
	}
	b := workspaceFileBinding{root: clone, info: info, live: func() bool { return true }}
	if res := readWorkspaceCommit(t.Context(), id, b, inClone, false); res.ErrorCode != "not_found" {
		t.Fatalf("a commit only in the clone was read: %+v", res)
	}
	res := readWorkspaceCommit(t.Context(), id, b, base, false)
	if !res.OK || res.Commit.SHA != base {
		t.Fatalf("repository commit: %+v", res)
	}
	// Brought back (as a turn's end does), the clone's commit reads.
	childRepoGit(t, repo, "fetch", "-q", filepath.Join(clone, ".git"), "+refs/heads/wip/"+id+":refs/heads/wip/"+id)
	if res := readWorkspaceCommit(t.Context(), id, b, inClone, false); !res.OK {
		t.Fatalf("fetched commit: %+v", res)
	}
}

type commitSender struct {
	mu  sync.Mutex
	got []protocol.CommitResult
	ch  chan struct{}
}

func (s *commitSender) Send(v any) error {
	s.mu.Lock()
	s.got = append(s.got, v.(protocol.CommitResult))
	s.mu.Unlock()
	s.ch <- struct{}{}
	return nil
}

func TestCommitLookupQueueAnswersBusyPerSession(t *testing.T) {
	dir, b := commitFixture(t)
	sha := commitFile(t, dir, "a.txt", "a\n", "A")
	release := make([]func(), 0, commitReadsPerSession)
	for i := 0; i < commitReadsPerSession; i++ {
		r, ok := acquireCommitRead("busy-session")
		if !ok {
			t.Fatal("slot refused")
		}
		release = append(release, r)
	}
	s := &commitSender{ch: make(chan struct{}, 4)}
	queueCommitRead(context.Background(), s, protocol.ReadCommit{RequestID: "r1", SessionID: "busy-session", SHA: sha}, b)
	queueCommitRead(context.Background(), s, protocol.ReadCommit{RequestID: "r2", SessionID: "other-session", SHA: sha}, b)
	for range 2 {
		select {
		case <-s.ch:
		case <-time.After(10 * time.Second):
			t.Fatal("no reply")
		}
	}
	for _, r := range release {
		r()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := map[string]protocol.CommitResult{}
	for _, r := range s.got {
		byID[r.RequestID] = r
	}
	if byID["r1"].ErrorCode != "busy" || !byID["r2"].OK || byID["r2"].Type != "commit_result" || byID["r2"].SessionID != "other-session" {
		t.Fatalf("replies: %+v", s.got)
	}
}

func markerScript(t *testing.T) (marker, script string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "ran")
	script = filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return marker, script
}

func patchText(t *testing.T, res protocol.CommitResult) string {
	t.Helper()
	if !res.OK {
		t.Fatalf("patch read failed: %s", res.ErrorCode)
	}
	raw, err := base64.StdEncoding.DecodeString(res.PatchB64)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Security round 1, C1: diff.submodule=diff in the superproject would make git run a child diff in
// the submodule, with the submodule's own diff.external and none of our flags.
func TestCommitLookupNeverRunsASubmodulesDiffCommand(t *testing.T) {
	marker, script := markerScript(t)
	dir, b := commitFixture(t)
	commitFile(t, dir, "README.md", "top\n", "Top")
	subSrc := filepath.Join(t.TempDir(), "subsrc")
	if err := os.Mkdir(subSrc, 0o700); err != nil {
		t.Fatal(err)
	}
	originGit(t, subSrc, "init", "-q")
	commitFile(t, subSrc, "s.txt", "one\n", "Sub one")
	originGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Add sub")
	sub := filepath.Join(dir, "sub")
	commitFile(t, sub, "s.txt", "two\n", "Sub two")
	originGit(t, dir, "add", "sub")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Move sub")
	moved := originGit(t, dir, "rev-parse", "HEAD")
	originGit(t, dir, "config", "diff.submodule", "diff")
	originGit(t, sub, "config", "diff.external", script)
	originGit(t, sub, "config", "diff.evil.textconv", script)
	if err := os.WriteFile(filepath.Join(sub, ".gitattributes"), []byte("* diff=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text := patchText(t, readWorkspaceCommit(t.Context(), "s", b, moved, true))
	if res := summary(t, b, moved); !res.OK {
		t.Fatalf("summary: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a submodule's diff command ran")
	}
	if !strings.Contains(text, "Subproject commit") {
		t.Fatalf("submodule not shown short:\n%s", text)
	}
}

// I2: files named by configuration are not read (an endless /dev/zero would take the host's memory).
func TestCommitLookupReadsNoFileNamedByConfiguration(t *testing.T) {
	dir, b := commitFixture(t)
	sha := commitFile(t, dir, "a.txt", "a\n", "A")
	for _, kv := range [][2]string{{"diff.orderFile", "/dev/zero"}, {"mailmap.file", "/dev/zero"}, {"log.mailmap", "true"}, {"core.attributesFile", "/dev/zero"}} {
		originGit(t, dir, "config", kv[0], kv[1])
	}
	start := time.Now()
	if res := summary(t, b, sha); !res.OK {
		t.Fatalf("summary: %+v", res)
	}
	if text := patchText(t, readWorkspaceCommit(t.Context(), "s", b, sha, true)); !strings.Contains(text, "+a\n") {
		t.Fatalf("patch:\n%s", text)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("reads took %s: a configured file was read", time.Since(start))
	}
}

// I2, defense in depth: whatever still reads without end fails fast under the address-space limit.
func TestCommitLookupGitRunsUnderAMemoryLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the limit is Linux only")
	}
	dir, b := commitFixture(t)
	sha := commitFile(t, dir, "a.txt", "a\n", "A")
	pinned, ok := pinGitDir(dir, b.info)
	if !ok {
		t.Fatal("pin")
	}
	defer pinned.close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	// A later -c outranks ours: the order file is /dev/zero, read to its (missing) end.
	err := pinned.git(ctx, io.Discard, "-c", "diff.orderFile=/dev/zero", "show", "--numstat", sha)
	if err == nil || ctx.Err() != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("unbounded read not stopped by the limit: err=%v after %s", err, time.Since(start))
	}
}

// I3: a partial clone's lazy fetch of a missing object would run an ext:: transport its
// configuration allows.
func TestCommitLookupNoLazyFetchTransport(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fetched")
	dir, b := commitFixture(t)
	sha := commitFile(t, dir, "f.txt", "hello\n", "One")
	blob := originGit(t, dir, "rev-parse", "HEAD:f.txt")
	if err := os.Remove(filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"},
		{"remote.origin.url", "ext::sh -c touch% " + marker}, {"remote.origin.promisor", "true"},
		{"protocol.ext.allow", "always"}, {"protocol.allow", "always"},
	} {
		originGit(t, dir, "config", kv[0], kv[1])
	}
	for _, withoutVar := range []bool{false, true} {
		// true: GIT_NO_LAZY_FETCH left out, as on a git that does not know it; the protocol
		// block alone must refuse the transport.
		restore := hostgit.SetWithoutLazyFetchVarForTest(withoutVar)
		_ = readWorkspaceCommit(t.Context(), "s", b, sha, true)
		_ = summary(t, b, sha)
		restore()
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("a lazy fetch ran a transport (GIT_NO_LAZY_FETCH left out: %t)", withoutVar)
		}
	}
}

// M2: a .git that points outside the workspace is refused; a registered worktree is read.
func TestCommitLookupRefusesAGitDirElsewhere(t *testing.T) {
	other, _ := commitFixture(t)
	foreign := commitFile(t, other, "secret.txt", "s\n", "Elsewhere")

	gitfile := filepath.Join(t.TempDir(), "gitfile")
	if err := os.Mkdir(gitfile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitfile, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := summary(t, originBinding(t, gitfile), foreign); res.OK {
		t.Fatal("a .git file naming another repository was read")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".git"), filepath.Join(link, ".git")); err != nil {
		t.Fatal(err)
	}
	if res := summary(t, originBinding(t, link), foreign); res.OK {
		t.Fatal("a symbolic link .git was read")
	}
	borrow, bb := commitFixture(t)
	commitFile(t, borrow, "b.txt", "b\n", "B")
	if err := os.WriteFile(filepath.Join(borrow, ".git", "objects", "info", "alternates"), []byte(filepath.Join(other, ".git", "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := summary(t, bb, foreign); res.OK {
		t.Fatal("objects borrowed through alternates were read")
	}

	// N2: an objects directory that is a link to another repository's store.
	linked, lb := commitFixture(t)
	commitFile(t, linked, "l.txt", "l\n", "L")
	objects := filepath.Join(linked, ".git", "objects")
	if err := os.RemoveAll(objects); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".git", "objects"), objects); err != nil {
		t.Fatal(err)
	}
	if res := summary(t, lb, foreign); res.OK {
		t.Fatal("objects read through a linked objects directory")
	}

	wt := filepath.Join(t.TempDir(), "wt")
	originGit(t, other, "worktree", "add", "-q", wt)
	if res := summary(t, originBinding(t, wt), foreign); !res.OK {
		t.Fatalf("a registered worktree: %+v", res)
	}
}

// M3: a workspace under the sandbox clone root that is not this session's clone is never read.
func TestCommitLookupRefusesAnUnrecognizedSandboxClone(t *testing.T) {
	id := "commit-lookup-recognized"
	repo, clone := cloneFixture(t, id)
	base := childRepoGit(t, repo, "rev-parse", "HEAD")
	info, err := os.Stat(clone)
	if err != nil {
		t.Fatal(err)
	}
	// A self-contained clone (no alternates), so only the clone-root check can refuse it.
	childRepoGit(t, clone, "repack", "-a", "-d", "-q")
	if err := os.Remove(filepath.Join(clone, ".git", "objects", "info", "alternates")); err != nil {
		t.Fatal(err)
	}
	b := workspaceFileBinding{root: clone, info: info, live: func() bool { return true }}
	if res := readWorkspaceCommit(t.Context(), "another-session", b, base, false); res.OK {
		t.Fatal("a clone of another session was read with host git")
	}
	if res := readWorkspaceCommit(t.Context(), id, b, base, false); !res.OK {
		t.Fatalf("own clone: %+v", res)
	}
}

// N3: a grafts file does not rewrite the parents shown.
func TestCommitLookupIgnoresGrafts(t *testing.T) {
	dir, b := commitFixture(t)
	first := commitFile(t, dir, "a.txt", "a\n", "A")
	second := commitFile(t, dir, "b.txt", "b\n", "B")
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "grafts"), []byte(second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := summary(t, b, second)
	if !res.OK || len(res.Commit.Parents) != 1 || res.Commit.Parents[0] != first {
		t.Fatalf("grafted parents shown: %+v", res.Commit)
	}
}

// A child's wip branch is merged when its tip is an ancestor of HEAD; only a session id names it.
func TestChildMergedRead(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	dir, b := commitFixture(t)
	commitFile(t, dir, "README.md", "one\n", "Root")
	child := "0f0e0d0c-0b0a-4908-8706-050403020100"
	if res := readChildMerged(t.Context(), "s", b, child); res.OK || res.ErrorCode != "not_found" {
		t.Fatalf("no branch: %+v", res)
	}
	if _, err := createSandboxClone(dir, child, filepath.Dir(dir)); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "branch", "wip/"+child)
	if res := readChildMerged(t.Context(), "s", b, child); !res.OK || res.Merged {
		t.Fatalf("untouched recent base: %+v", res)
	}
	originGit(t, dir, "checkout", "-q", "wip/"+child)
	commitFile(t, dir, "work.txt", "w\n", "Child work")
	originGit(t, dir, "checkout", "-q", "main")
	res := readChildMerged(t.Context(), "s", b, child)
	if !res.OK || res.Merged {
		t.Fatalf("unmerged: %+v", res)
	}

	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "merge", "-q", "--no-edit", "--no-ff", "wip/"+child)
	if res := readChildMerged(t.Context(), "s", b, child); !res.OK || !res.Merged {
		t.Fatalf("merged: %+v", res)
	}
	originGit(t, dir, "checkout", "-q", "wip/"+child)
	if res := readChildMerged(t.Context(), "s", b, child); !res.OK || res.Merged {
		t.Fatalf("checked out child: %+v", res)
	}
	originGit(t, dir, "checkout", "-q", "main")
	originGit(t, dir, "branch", "wip/"+child+".incoming")
	if res := readChildMerged(t.Context(), "s", b, child); !res.OK || res.Merged {
		t.Fatalf("incoming work: %+v", res)
	}
	originGit(t, dir, "branch", "-D", "wip/"+child+".incoming")
	otherWorktree := filepath.Join(t.TempDir(), "child-worktree")
	originGit(t, dir, "worktree", "add", "-q", otherWorktree, "wip/"+child)
	if res := readChildMerged(t.Context(), "s", b, child); !res.OK || res.Merged {
		t.Fatalf("child checked out elsewhere: %+v", res)
	}
	originGit(t, dir, "worktree", "remove", otherWorktree)

	parent := "af0e0d0c-0b0a-4908-8706-050403020100"
	parentDir, err := createSandboxClone(dir, parent, filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "branch", "wip/"+parent)
	originGit(t, dir, "checkout", "-q", "main~1")
	if res := readChildMerged(t.Context(), parent, originBinding(t, parentDir), child); !res.OK || !res.Merged {
		t.Fatalf("sandboxed parent branch: %+v", res)
	}
	for _, bad := range []string{"HEAD", "main", "../x", "0F0E0D0C-0B0A-4908-8706-050403020100", child + "^", "--all"} {
		if res := readChildMerged(t.Context(), "s", b, bad); res.OK || res.ErrorCode != "invalid" {
			t.Fatalf("%q: %+v", bad, res)
		}
	}
}
