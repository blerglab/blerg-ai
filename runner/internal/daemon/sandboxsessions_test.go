package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The escape probes, for an ordinary sandboxed session (real Docker, BLERG_SANDBOX_CHILD_TEST=1):
// nothing it does in its container reaches the checkout or its .git, nothing it plants runs on the
// host, and its commits and its leftover work come back on wip/<id>.
func TestSandboxSessionEscapeProbesInDocker(t *testing.T) {
	sandboxEscapeProbesInDocker(t, "session-escape-")
}

// An ordinary sandboxed session (a launch-sheet, board or cron one: no parent) works in a clone of
// its own on wip/<id>, exactly like a child: the repository's working tree and .git are never
// mounted, only its objects, read-only. Its turn end pauses the container around the fetch; its
// commits land on wip/<id> in the repository, whose checkout never moves.
func TestSandboxSessionRunsInItsOwnClone(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	stubCloneProbe(t, true, true)
	repo := filepath.Join(host.cfg.ReposRoot, "proj")
	initChildRepo(t, repo)

	const id = "sandbox-session-1"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("spawn errored: %v", errs)
	}
	clone := filepath.Join(sandboxCloneRoot(), id)
	calls := dockerCalls(t, logPath)
	run := runCalls(calls)
	if !strings.Contains(run, "src="+clone+",dst="+sandboxWorkdir) {
		t.Fatalf("the session's clone is not what is mounted at /workspace:\n%s", run)
	}
	if strings.Contains(run, "src="+repo+",") || strings.Contains(run, "src="+repo+"/.git,") {
		t.Fatalf("the repository or its .git was mounted:\n%s", run)
	}
	objects := filepath.Join(repo, ".git", "objects")
	if !strings.Contains(run, "src="+objects+",dst="+objects+",readonly") {
		t.Fatalf("the repository's objects are not mounted read-only at their own path:\n%s", run)
	}
	container := dockerContainerName(id)
	if !strings.Contains(calls, "pause "+container) || !strings.Contains(calls, "unpause "+container) {
		t.Fatalf("the turn end did not pause the container around the fetch:\n%s", calls)
	}
	if got := childRepoGit(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "wip/"+id {
		t.Fatalf("clone branch %q", got)
	}
	if got := childRepoGit(t, clone, "for-each-ref", "--format=%(refname)", "refs/heads/"); got != "refs/heads/wip/"+id {
		t.Fatalf("the clone holds other branches: %q", got)
	}

	// The session commits in its clone, as its engine would inside the container.
	childCommit(t, clone, "work.txt")
	host.Kill(id)
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/"+id); got != "child work.txt" {
		t.Fatalf("the session's commit did not reach wip/%s: %q", id, got)
	}
	if got := childRepoGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the checkout moved to %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "work.txt")); err == nil {
		t.Fatal("the session wrote into the checkout's working tree")
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("a clean clone was left behind: %v", err)
	}
}

// A sandboxed session in an empty folder that is not a repository yet (a new folder, a scratch
// folder) gets one made, with no commit, and works in a clone of it; with no commit at the end
// nothing is left but the empty repository, and with one it lands on wip/<id>.
func TestSandboxSessionInANewFolder(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, _, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	stubCloneProbe(t, true, true)
	proj := filepath.Join(host.cfg.ReposRoot, "proj") // empty: sandboxHost makes it

	for _, commit := range []bool{false, true} {
		id := "sandbox-new-folder-0"
		if commit {
			id = "sandbox-new-folder-1"
		}
		host.Spawn(protocol.SpawnSession{
			Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent",
			Sandbox: true, NewRepo: true, InitialPrompt: "hello",
		})
		sender.waitForKindCount(t, "turn_done", map[bool]int{false: 1, true: 2}[commit])
		if errs := sender.sessionErrors(); len(errs) > 0 {
			t.Fatalf("spawn errored: %v", errs)
		}
		clone := filepath.Join(sandboxCloneRoot(), id)
		if got, err := childRepoGitErr(clone, "symbolic-ref", "HEAD"); err != nil || got != "refs/heads/wip/"+id {
			t.Fatalf("the clone's HEAD is %q (%v)", got, err)
		}
		if commit {
			childCommit(t, clone, "first.txt")
		}
		host.Kill(id)
		_, err := childRepoGitErr(proj, "rev-parse", "--verify", "refs/heads/wip/"+id)
		if commit && err != nil {
			t.Fatal("the session's first commit did not reach the new repository")
		}
		if !commit && err == nil {
			t.Fatal("a branch was left for a session that committed nothing")
		}
		if _, err := os.Stat(clone); !os.IsNotExist(err) {
			t.Fatalf("a clean clone was left behind: %v", err)
		}
	}
	if entries, _ := os.ReadDir(proj); len(entries) != 1 || entries[0].Name() != ".git" {
		t.Fatalf("the new folder's working tree was written to: %v", entries)
	}
}

// A sandboxed terminal session works in its own clone too: the container is started on the clone,
// never on the checkout, and a clone whose sandbox failed to start is removed.
func TestSandboxTerminalWorksInItsOwnClone(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("PATH", fakeClaude(t)+":"+os.Getenv("PATH"))
	repo := filepath.Join(root, "app")
	initChildRepo(t, repo)
	var mounted string
	old := startTerminalSandbox
	startTerminalSandbox = func(_, hostProjectPath string, _, _ []string, _ SandboxOptions) error {
		mounted = hostProjectPath
		return errors.New("no docker in this test")
	}
	t.Cleanup(func() { startTerminalSandbox = old })
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.sandboxImagePresent = func() bool { return true }
	raw, _ := json.Marshal(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-term", Repo: "app", Sandbox: true})
	mgr.handleSpawnSession(raw)
	if want := filepath.Join(sandboxCloneRoot(), "s-term"); mounted != want {
		t.Fatalf("the terminal sandbox was started on %q, not its clone %q", mounted, want)
	}
	if _, err := os.Stat(mounted); !os.IsNotExist(err) {
		t.Fatal("the clone of a terminal whose sandbox failed was left behind")
	}
	if got := childRepoGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/wip/"); got != "" {
		t.Fatalf("a branch was left behind: %q", got)
	}
}

// At reattach a running terminal sandbox is kept only when the daemon's own record names the
// session's recorded clone and docker, when it can say, agrees. One an older daemon started (the
// person's folder), one with no record, and one whose container mounts anything else are stopped
// and told why; a failed docker inspect alone does not keep a legacy container running.
func TestLegacyTerminalSandboxIsStopped(t *testing.T) {
	dockerDir, _ := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(t.TempDir(), "docker.log"))
	t.Setenv("FAKE_DOCKER_EXEC_NOOP", "1")
	stubCloneProbe(t, false, false)
	cloned := sandboxTestClone(t, "cloned")
	unread := sandboxTestClone(t, "unread")
	other := sandboxTestClone(t, "other")
	sender := newRecordingSender(16)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	for id, path := range map[string]string{"legacy": "/home/someone/repos/app", "legacy-unread": "/home/someone/repos/app", "cloned": cloned, "unread": unread, "other": other} {
		if err := mgr.writeRecord(SessionRecord{Version: recordSchemaVersion, SessionID: id, ProjectPath: path, Sandbox: true}); err != nil {
			t.Fatal(err)
		}
	}
	sources := map[string]string{
		dockerContainerName("legacy"): "/home/someone/repos/app",
		dockerContainerName("cloned"): cloned,
		dockerContainerName("other"):  "/home/someone/repos/app",
		// "legacy-unread", "unread" and "norecord": docker inspect fails.
	}
	old := containerWorkspaceSource
	containerWorkspaceSource = func(name string) (string, bool) { return sources[name], sources[name] != "" }
	t.Cleanup(func() { containerWorkspaceSource = old })
	kept := mgr.stopLegacySandboxes([]string{"legacy", "legacy-unread", "cloned", "unread", "other", "norecord"})
	if strings.Join(kept, ",") != "cloned,unread" {
		t.Fatalf("kept %v, want cloned,unread", kept)
	}
	msgs := strings.Join(sender.messages(), "\n")
	for _, id := range []string{"legacy", "legacy-unread", "other", "norecord"} {
		if !strings.Contains(msgs, `"`+id+`"`) {
			t.Fatalf("%s was not told why it stopped: %s", id, msgs)
		}
		if _, err := readSessionRecord(mgr.reposRoot(), id); err == nil {
			t.Fatalf("%s's record was kept", id)
		}
	}
	if !strings.Contains(msgs, "older daemon that mounted your checkout") {
		t.Fatalf("no reason given: %s", msgs)
	}
}

// A recovery record an older daemon wrote for a sandboxed terminal names the person's folder: it is
// never recovered onto (no container is started), the record goes so it is not retried at every
// reconnect, and the session says why. A record whose clone lost its own record is refused too.
func TestLegacySandboxRecordIsNotRecovered(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	var started []string
	old := startTerminalSandbox
	startTerminalSandbox = func(id, path string, _, _ []string, _ SandboxOptions) error {
		started = append(started, id+"="+path)
		return errors.New("not in this test")
	}
	t.Cleanup(func() { startTerminalSandbox = old })
	sender := newRecordingSender(16)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	broken := sandboxTestClone(t, "rec-broken")
	_, brokenRecord, _ := sandboxClonePaths("rec-broken")
	if err := os.Remove(brokenRecord); err != nil {
		t.Fatal(err)
	}
	for id, path := range map[string]string{"rec-legacy": t.TempDir(), "rec-broken": broken} {
		rec := SessionRecord{Version: recordSchemaVersion, SessionID: id, ProjectPath: path, Sandbox: true}
		if err := mgr.writeRecord(rec); err != nil {
			t.Fatal(err)
		}
		mgr.recoverSession(rec)
		if _, err := readSessionRecord(mgr.reposRoot(), id); err == nil {
			t.Fatalf("%s: the record was kept, so it would be retried at every reconnect", id)
		}
	}
	if len(started) != 0 {
		t.Fatalf("a sandbox was started from a record with no clone of its own: %v", started)
	}
	msgs := strings.Join(sender.messages(), "\n")
	if !strings.Contains(msgs, "names a folder of yours, not a clone of its own") || !strings.Contains(msgs, "the record of its clone is missing") {
		t.Fatalf("the sessions were not told why: %s", msgs)
	}
}

// A tmux probe that never answers (the session's own tmux, made to hang) is bounded: the reattach
// listing and the orphan sweep skip that container instead of blocking, and nothing is removed.
func TestHungTmuxProbeIsSkipped(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	oldTimeout := sandboxProbeTimeout
	sandboxProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { sandboxProbeTimeout = oldTimeout })
	start := time.Now()
	if has, known := tmuxProbe("blerg-sandbox-hung"); has || known {
		t.Fatalf("a hung probe reported has=%v known=%v", has, known)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the probe took %s", took)
	}

	// The passes that use it: the hung container is neither listed nor swept.
	origOut, origRun, origProbe := dockerOutput, dockerRun, dockerProbe
	var removed []string
	dockerOutput = func(args ...string) ([]byte, error) {
		return []byte(sandboxContainerPrefix + "hung\n"), nil
	}
	dockerRun = func(args ...string) error {
		if args[0] == "rm" {
			removed = append(removed, args[len(args)-1])
		}
		return nil
	}
	dockerProbe = func(time.Duration, ...string) (bool, error) { return true, errors.New("timed out") }
	t.Cleanup(func() { dockerOutput, dockerRun, dockerProbe = origOut, origRun, origProbe })
	if got := listSandboxedTmuxSessions(); len(got) != 0 {
		t.Fatalf("a container whose probe hung was listed: %v", got)
	}
	sweepOrphanSandboxContainers(nil)
	if len(removed) != 0 {
		t.Fatalf("a container whose probe hung was swept: %v", removed)
	}
}

// The stop-time step that runs in the container: work left uncommitted on wip/<id> is committed
// (the session's own hooks skipped), and the clone is then clean; a HEAD off wip/<id> commits
// nothing and is reported not clean.
func TestSettleScriptCommitsLeftoverWork(t *testing.T) {
	_, clone := cloneFixture(t, "settle-1")
	if err := os.WriteFile(filepath.Join(clone, "draft.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marks := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(clone, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marks+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(id string) int {
		script := strings.Replace(sandboxSettleScript(id), "cd /workspace", "cd '"+clone+"'", 1)
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return exit.ExitCode()
			}
			t.Fatal(err)
		}
		return 0
	}
	if code := run("settle-1"); code != 0 {
		t.Fatalf("settle exit %d, want 0 (clean after the commit)", code)
	}
	if got := childRepoGit(t, clone, "log", "-1", "--format=%s%n%an", "wip/settle-1"); got != sandboxSnapshotMessage+"\nBlerg" {
		t.Fatalf("leftover work commit: %q", got)
	}
	if _, err := os.Stat(marks); err == nil {
		t.Fatal("the session's pre-commit hook ran for the stop-time commit")
	}

	_, other := cloneFixture(t, "settle-2")
	childRepoGit(t, other, "checkout", "-q", "-b", "elsewhere")
	if err := os.WriteFile(filepath.Join(other, "draft.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clone = other
	if code := run("settle-2"); code != 1 {
		t.Fatalf("settle exit %d for a HEAD off wip/<id>, want 1", code)
	}
	if got := childRepoGit(t, other, "status", "--porcelain"); got == "" {
		t.Fatal("work on another branch was committed")
	}
}

// After a daemon restart nothing hosts a sandboxed agent session again; its clone's committed
// work is still brought back to wip/<id>, and the clone itself is kept when the container cannot
// be asked whether anything else is in it.
func TestFinishSandboxWithoutAContainerKeepsTheClone(t *testing.T) {
	repo, clone := cloneFixture(t, "finish-1")
	dockerDir, _ := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(t.TempDir(), "docker.log"))
	stubCloneProbe(t, false, false)
	childCommit(t, clone, "work.txt")
	finishSandbox("finish-1")
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/finish-1"); got != "child work.txt" {
		t.Fatalf("committed work was not brought back: %q", got)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatal("a clone nobody could ask about was removed")
	}
	finishSandbox("finish-1") // twice is safe
}

// A sandboxed session's origin (the commit a filing names) is read from its clone as plain files:
// host Git never runs there, so nothing the session configured in the clone can.
func TestSandboxOriginNeverRunsHostGit(t *testing.T) {
	_, clone := cloneFixture(t, "origin-1")
	childCommit(t, clone, "work.txt")
	want := childRepoGit(t, clone, "rev-parse", "HEAD")
	// A git on PATH that records any call: the read must make none.
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "git-calls")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho \"$@\" >> '"+calls+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	info, err := os.Stat(clone)
	if err != nil {
		t.Fatal(err)
	}
	sender := &originSender{ch: make(chan protocol.SessionOriginResult, 1)}
	binding := workspaceFileBinding{root: clone, info: info, live: func() bool { return true }}
	queueSessionOrigin(context.Background(), sender, protocol.ReadSessionOrigin{RequestID: "r1", SessionID: "origin-1"}, binding)
	select {
	case res := <-sender.ch:
		if !res.OK || res.Commit != want {
			t.Fatalf("origin %+v, want %s", res, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no origin result")
	}
	if raw, err := os.ReadFile(calls); err == nil {
		t.Fatalf("host git ran in the sandbox clone: %s", raw)
	}
}

type originSender struct {
	ch chan protocol.SessionOriginResult
}

func (s *originSender) Send(v any) error {
	if r, ok := v.(protocol.SessionOriginResult); ok {
		s.ch <- r
	}
	return nil
}

// readCloneHead follows HEAD through loose and packed refs, reports a branch with no commit as
// unborn, and refuses links and malformed content.
func TestReadCloneHead(t *testing.T) {
	_, clone := cloneFixture(t, "head-1")
	want := childRepoGit(t, clone, "rev-parse", "HEAD")
	if got, ok := readCloneHead(clone); !ok || got != want {
		t.Fatalf("loose ref: %q %v", got, ok)
	}
	childRepoGit(t, clone, "pack-refs", "--all")
	if got, ok := readCloneHead(clone); !ok || got != want {
		t.Fatalf("packed ref: %q %v", got, ok)
	}
	childRepoGit(t, clone, "symbolic-ref", "HEAD", "refs/heads/unborn")
	if got, ok := readCloneHead(clone); !ok || got != "" {
		t.Fatalf("unborn: %q %v", got, ok)
	}
	for name, content := range map[string]string{"escape": "ref: refs/heads/../../../etc/passwd\n", "garbage": "not a ref\n"} {
		if err := os.WriteFile(filepath.Join(clone, ".git", "HEAD"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := readCloneHead(clone); ok {
			t.Fatalf("%s HEAD accepted", name)
		}
	}
	head := filepath.Join(clone, ".git", "HEAD")
	_ = os.Remove(head)
	outside := filepath.Join(t.TempDir(), "HEAD")
	if err := os.WriteFile(outside, []byte(want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, head); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCloneHead(clone); ok {
		t.Fatal("a symlinked HEAD was followed")
	}
}

// sandboxTestClone makes sessionID a clone of a fresh one-commit repository, as a spawn would, so a
// test can start a sandbox: the mount check accepts nothing else.
func sandboxTestClone(t *testing.T, sessionID string) string {
	t.Helper()
	if state := os.Getenv(DaemonStateDirEnv); state == "" || !strings.HasPrefix(state, os.TempDir()) {
		t.Setenv(DaemonStateDirEnv, t.TempDir())
	}
	repo := filepath.Join(t.TempDir(), "proj")
	initChildRepo(t, repo)
	clone, err := createSandboxClone(repo, sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	return clone
}
