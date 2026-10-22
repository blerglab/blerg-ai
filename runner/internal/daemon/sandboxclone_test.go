package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// childRepoGit runs git in dir for a fixture: no operator configuration or credentials.
func childRepoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := childRepoGitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func childRepoGitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Example Developer", "GIT_AUTHOR_EMAIL=developer@example.com",
		"GIT_COMMITTER_NAME=Example Developer", "GIT_COMMITTER_EMAIL=developer@example.com")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// initChildRepo makes dir a repository with one commit on main.
func initChildRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "initial.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, dir, "add", ".")
	childRepoGit(t, dir, "commit", "-qm", "initial")
}

func runCalls(calls string) string {
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "run ") {
			return line
		}
	}
	return ""
}

func stubCloneProbe(t *testing.T, clean, known bool) {
	t.Helper()
	old := sandboxCloneSettle
	sandboxCloneSettle = func(string, string) (bool, bool) { return clean, known }
	t.Cleanup(func() { sandboxCloneSettle = old })
}

// A sandboxed child is mounted from its own clone on wip/<id>; the repository's working tree and
// .git are never mounted, only its objects, read-only. Its end fetches nothing back when it made
// no commit, and removes the clone the probe found clean.
func TestSandboxedChildRunsInItsOwnClone(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	stubCloneProbe(t, true, true)
	repo := filepath.Join(host.cfg.ReposRoot, "proj")
	initChildRepo(t, repo)

	const id = "child-clone-1"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("child spawn errored: %v", errs)
	}
	clone := filepath.Join(sandboxCloneRoot(), id)
	run := runCalls(dockerCalls(t, logPath))
	if !strings.Contains(run, "src="+clone+",dst="+sandboxWorkdir) {
		t.Fatalf("the child's clone is not what is mounted at /workspace:\n%s", run)
	}
	if strings.Contains(run, "src="+repo+",") || strings.Contains(run, "src="+repo+"/.git,") {
		t.Fatalf("the repository or its .git was mounted:\n%s", run)
	}
	objects := filepath.Join(repo, ".git", "objects")
	if !strings.Contains(run, "src="+objects+",dst="+objects+",readonly") {
		t.Fatalf("the repository's objects are not mounted read-only at their own path:\n%s", run)
	}
	if got := childRepoGit(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "wip/"+id {
		t.Fatalf("clone branch %q", got)
	}
	if got := childRepoGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("parent checkout moved to %q", got)
	}

	host.Kill(id)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("a clean clone was left behind: %v", err)
	}
	if out, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
		t.Fatalf("a branch without commits was left in the repository: %s", out)
	}
}

// No clone, no start: a folder with content that is not a Git repository is refused, and no
// container is made with the folder mounted.
func TestSandboxedChildWithoutACloneIsRefused(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	proj := filepath.Join(host.cfg.ReposRoot, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "child-clone-2", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	reason := sender.waitForSessionError(t)
	if !strings.Contains(reason, "its own clone") || !strings.Contains(reason, "not a Git repository") {
		t.Fatalf("refusal %q", reason)
	}
	if raw, _ := os.ReadFile(logPath); strings.Contains(string(raw), "run -d") {
		t.Fatalf("a container started without a clone:\n%s", raw)
	}
	if _, err := os.Lstat(filepath.Join(proj, ".git")); err == nil {
		t.Fatal("a folder with content was made a repository")
	}
}

func cloneFixture(t *testing.T, id string) (repo, clone string) {
	t.Helper()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo = filepath.Join(t.TempDir(), "proj")
	initChildRepo(t, repo)
	clone, err := createSandboxClone(repo, id, "")
	if err != nil {
		t.Fatal(err)
	}
	return repo, clone
}

func childCommit(t *testing.T, clone, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(clone, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, clone, "add", name)
	childRepoGit(t, clone, "commit", "-qm", "child "+name)
}

// The end of a child never deletes work: its commits are fetched into the repository's wip/<id>,
// and a clone the probe did not find clean (or could not ask) is kept.
func TestReleaseChildCloneKeepsWork(t *testing.T) {
	repo, clone := cloneFixture(t, "child-commits")
	childCommit(t, clone, "work.txt")
	releaseSandboxClone(clone, "child-commits", true, true)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("a clean clone should be removed once its branch is fetched")
	}
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/child-commits"); got != "child work.txt" {
		t.Fatalf("the child's commit did not reach the repository: %q", got)
	}

	_, dirty := cloneFixture(t, "child-dirty")
	releaseSandboxClone(dirty, "child-dirty", false, true)
	if _, err := os.Stat(dirty); err != nil {
		t.Fatal("a clone with work outside its branch was removed")
	}
	_, unknown := cloneFixture(t, "child-unknown")
	releaseSandboxClone(unknown, "child-unknown", false, false)
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("a clone nobody could ask about was removed")
	}

	// A path that is not a daemon-made child clone is never touched.
	releaseSandboxClone(repo, "child-dirty", true, true)
	discardSandboxClone(repo, "child-dirty")
	if _, err := os.Stat(filepath.Join(repo, "initial.txt")); err != nil {
		t.Fatal("the repository checkout was touched")
	}
}

// writeHostileClone plants, in a child's clone, every configured command and hook the reviewer
// found would run on the host: each runs evil.sh, which records its name in marks. A child can
// write all of this; the host must run none of it.
func writeHostileClone(t *testing.T, clone, marks string) {
	t.Helper()
	evil := filepath.Join(clone, "evil.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\necho \"$1\" >> '"+marks+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(clone, "hostile-hooks")
	for _, dir := range []string{hooks, filepath.Join(clone, ".git", "hooks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, h := range []string{"post-checkout", "post-merge", "pre-commit", "reference-transaction", "pre-auto-gc", "post-rewrite", "pre-push", "fsmonitor-watchman"} {
			if err := os.WriteFile(filepath.Join(dir, h), []byte("#!/bin/sh\n'"+evil+"' hook-"+h+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	for k, v := range map[string]string{
		"core.fsmonitor":             evil + " fsmonitor",
		"core.sshCommand":            evil + " ssh",
		"core.hooksPath":             hooks,
		"core.alternateRefsCommand":  evil + " altrefs",
		"uploadpack.packObjectsHook": evil + " packobjects",
		"filter.evil.clean":          evil + " clean",
		"filter.evil.smudge":         evil + " smudge",
		"filter.evil.process":        evil + " process",
		"core.pager":                 evil + " pager",
	} {
		childRepoGit(t, clone, "config", k, v)
	}
	if err := os.WriteFile(filepath.Join(clone, ".gitattributes"), []byte("* filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The host's fetch of wip/<id>, and the whole release, run none of the child's configured
// commands or hooks.
func TestHostFetchIgnoresTheChildsConfigAndHooks(t *testing.T) {
	repo, clone := cloneFixture(t, "child-hostile")
	childCommit(t, clone, "work.txt")
	marks := filepath.Join(t.TempDir(), "marks")
	writeHostileClone(t, clone, marks)
	before := childRepoGit(t, repo, "rev-parse", "refs/heads/main")

	if commits, err := syncSandboxBranch(clone, "child-hostile", ""); err != nil || !commits {
		t.Fatalf("sync: commits=%v err=%v", commits, err)
	}
	releaseSandboxClone(clone, "child-hostile", false, true)
	if raw, err := os.ReadFile(marks); err == nil {
		t.Fatalf("the child's configuration ran on the host: %s", raw)
	}
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/child-hostile"); got != "child work.txt" {
		t.Fatalf("the child's commit did not arrive: %q", got)
	}
	if got := childRepoGit(t, repo, "rev-parse", "refs/heads/main"); got != before {
		t.Fatal("the fetch moved a branch other than wip/<id>")
	}

	// What actually blocks these keys is Git's protected-config model plus hostGit's clean
	// environment (no system or global config, HOME=/nonexistent); the hardenedGitArgs -c flags
	// (core.hooksPath/fsmonitor/sshCommand, protocol.ext.allow) are defense in depth and redundant
	// with that on this Git. The one load-bearing flag is protocol.file.allow=always, without which
	// a local fetch is refused outright on Git >= 2.38 (functionality, not security). Prove the
	// clean environment is load-bearing: the same fetch run with the host's real environment and a
	// global config that points core.fsmonitor at the child's script DOES run it.
	marks2 := filepath.Join(t.TempDir(), "marks2")
	writeHostileClone(t, clone, marks2)
	globalCfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalCfg, []byte("[core]\n\tfsmonitor = "+filepath.Join(clone, "evil.sh")+" fsmonitor-global\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "-C", repo, "status", "--porcelain")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+globalCfg, "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=")
	_ = cmd.Run()
	if _, err := os.Stat(marks2); err != nil {
		t.Skip("this Git does not run core.fsmonitor from global config here; the clean-environment guard cannot be demonstrated")
	}
}

// The objects mount comes from the daemon's own record, never from the clone's alternates file,
// which the child can rewrite.
func TestChildCloneMountIgnoresTheClonesAlternates(t *testing.T) {
	repo, clone := cloneFixture(t, "child-alternates")
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(clone, ".git", "objects", "info", "alternates"), []byte(elsewhere+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mount, err := sandboxCloneMount("child-alternates")
	objects := filepath.Join(repo, ".git", "objects")
	if err != nil || mount != "type=bind,src="+objects+",dst="+objects+",readonly" {
		t.Fatalf("mount %q, %v", mount, err)
	}
}

// End to end with a real container (BLERG_SANDBOX_CHILD_TEST=1 and a built sandbox image): the
// reviewer's escape probes, from inside the child's container. None may reach the repository, and
// after the daemon's own release on the host, nothing the child planted has run there.
func TestSandboxChildEscapeProbesInDocker(t *testing.T) {
	sandboxEscapeProbesInDocker(t, "child-escape-")
}

// sandboxEscapeProbesInDocker runs the escape probes against the clone a sandboxed session (an
// ordinary one or a child: the same start path) is given, then the turn-end sync with a real pause
// and the stop with the real in-container commit of leftover work.
func sandboxEscapeProbesInDocker(t *testing.T, idPrefix string) {
	t.Helper()
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	labelTestContainers(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(t.TempDir(), "proj")
	initChildRepo(t, repo)
	// Another linked worktree of the repository, as a person or a sibling would have.
	sibling := filepath.Join(t.TempDir(), "sibling")
	childRepoGit(t, repo, "worktree", "add", "-q", "-b", "sibling", sibling)
	before := childRepoGit(t, repo, "rev-parse", "refs/heads/main")
	marks := filepath.Join(t.TempDir(), "marks")
	if err := os.WriteFile(marks+".keep", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	id := idPrefix + ccUUID()
	clone, err := createSandboxClone(repo, id, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { discardSandboxClone(clone, id) })
	name, err := startSandboxContainer(id, clone, nil, sandboxCredential{}, SandboxOptions{})
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	t.Cleanup(func() { releaseTestSandbox(id) })
	git := filepath.Join(repo, ".git")
	evil := filepath.Join(clone, "evil.sh")
	script := `set -u
fail=0
escaped() { echo "ESCAPED: $1"; fail=1; }
# The repository's .git: hooks, config, its checked-out branch, worktree admin data, objects.
mkdir -p '` + git + `/hooks' 2>/dev/null; printf '#!/bin/sh\necho hook\n' > '` + git + `/hooks/post-checkout' 2>/dev/null && escaped hooks
git config --file '` + git + `/config' core.fsmonitor '` + evil + `' 2>/dev/null && escaped config-fsmonitor
git config --file '` + git + `/config' core.sshCommand '` + evil + `' 2>/dev/null && escaped config-sshCommand
git --git-dir='` + git + `' update-ref refs/heads/main HEAD 2>/dev/null && escaped update-ref-main
git --git-dir='` + git + `' worktree prune --expire=now 2>/dev/null
touch '` + git + `/objects/planted' 2>/dev/null && escaped objects
touch '` + repo + `/planted.txt' 2>/dev/null && escaped working-tree
# Its own clone: commit on wip/<id>, then plant everything that would run on a host git.
test "$(git rev-parse --abbrev-ref HEAD)" = "wip/` + id + `" || escaped wrong-branch
printf 'child\n' > child.txt && git add child.txt && git -c user.name=Child -c user.email=child@example.com commit -qm 'child change' || escaped commit-failed
printf '#!/bin/sh\necho "$1" >> '"'` + marks + `'"'\nexit 0\n' > evil.sh && chmod +x evil.sh
mkdir -p .git/hooks && for h in post-checkout post-merge reference-transaction pre-auto-gc; do printf '#!/bin/sh\n'"'` + evil + `'"' hook-'$h'\n' > .git/hooks/$h; chmod +x .git/hooks/$h; done
git config core.fsmonitor '` + evil + ` fsmonitor'
git config core.sshCommand '` + evil + ` ssh'
git config uploadpack.packObjectsHook '` + evil + ` packobjects'
git config core.alternateRefsCommand '` + evil + ` altrefs'
exit $fail`
	out, err := exec.Command("docker", "exec", "-e", "HOME=/tmp", name, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("a probe escaped the session's clone: %v: %s", err, out)
	}
	// The turn's end: the container is paused, the clone checked, the branch fetched.
	if err := syncSandboxAtTurnEnd(clone, id); err != nil {
		t.Fatalf("turn-end sync: %v", err)
	}
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/"+id); got != "child change" {
		t.Fatalf("the session's commit did not reach the repository at its turn's end: %q", got)
	}
	if st, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", name).Output(); err != nil || strings.TrimSpace(string(st)) != "running" {
		t.Fatalf("the container was left %q after the turn-end sync", st)
	}
	// Work left uncommitted when the session stops.
	if out, err := exec.Command("docker", "exec", name, "sh", "-c", "printf 'draft\n' > draft.txt").CombinedOutput(); err != nil {
		t.Fatalf("draft: %v: %s", err, out)
	}
	finishSandbox(id)

	if raw, err := os.ReadFile(marks); err == nil {
		t.Fatalf("the session's hooks or configuration ran on the host: %s", raw)
	}
	if got := childRepoGit(t, repo, "rev-parse", "refs/heads/main"); got != before {
		t.Fatal("the session moved the repository's main")
	}
	if got := childRepoGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the checkout is on %q", got)
	}
	if _, err := os.Stat(filepath.Join(git, "hooks", "post-checkout")); err == nil {
		t.Fatal("a hook was planted in the repository")
	}
	if cfg, _ := os.ReadFile(filepath.Join(git, "config")); strings.Contains(string(cfg), "fsmonitor") || strings.Contains(string(cfg), "sshCommand") {
		t.Fatalf("the repository's config was changed:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(git, "worktrees", "sibling", "gitdir")); err != nil {
		t.Fatal("the session pruned the repository's other worktree")
	}
	if _, err := os.Stat(filepath.Join(git, "objects", "planted")); err == nil {
		t.Fatal("the session wrote into the repository's objects")
	}
	if _, err := os.Stat(filepath.Join(repo, "planted.txt")); err == nil {
		t.Fatal("the session wrote into the checkout's working tree")
	}
	if got := childRepoGit(t, repo, "log", "-2", "--format=%s", "wip/"+id); got != sandboxSnapshotMessage+"\nchild change" {
		t.Fatalf("wip/%s after the stop holds %q, want the leftover work committed on top", id, got)
	}
	if got := childRepoGit(t, repo, "show", "wip/"+id+":draft.txt"); got != "draft" {
		t.Fatalf("the leftover draft did not come back: %q", got)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("the clone, clean after the stop, was kept: %v", err)
	}
	if st, _ := exec.Command("docker", "inspect", name).CombinedOutput(); !strings.Contains(string(st), "No such") && !strings.Contains(string(st), "[]") {
		t.Fatal("the container outlived the stop")
	}
	// Host git in the repository afterwards runs nothing the session planted either.
	childRepoGit(t, repo, "status", "--porcelain")
	childRepoGit(t, sibling, "checkout", "-q", "wip/"+id)
	if raw, err := os.ReadFile(marks); err == nil {
		t.Fatalf("the session's hooks ran on a later host git command: %s", raw)
	}
}

// Right before every host fetch the clone's alternates are rewritten from the daemon's record: a
// child-rewritten file cannot make the fetch read objects from another store. A clone or a
// repository with http-alternates, a nested chain, or redirected object directories is refused.
func TestHostFetchResetsAHostileAlternatesFile(t *testing.T) {
	repo, clone := cloneFixture(t, "child-alt")
	childCommit(t, clone, "work.txt")
	alternates := filepath.Join(clone, ".git", "objects", "info", "alternates")
	objects := filepath.Join(repo, ".git", "objects")

	// Another store the child would like the host to read from.
	elsewhere := filepath.Join(t.TempDir(), "other")
	initChildRepo(t, elsewhere)
	if err := os.WriteFile(alternates, []byte(filepath.Join(elsewhere, ".git", "objects")+"\n"+objects+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if commits, err := syncSandboxBranch(clone, "child-alt", ""); err != nil || !commits {
		t.Fatalf("sync: commits=%v err=%v", commits, err)
	}
	if raw, _ := os.ReadFile(alternates); string(raw) != objects+"\n" {
		t.Fatalf("alternates not reset from the record: %q", raw)
	}

	// A symlinked alternates is refused by the pre-fetch tree walk (checkSandboxCloneTree), which
	// rejects any link under .git, and its target is never read or written.
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(alternates)
	if err := os.Symlink(target, alternates); err != nil {
		t.Fatal(err)
	}
	if _, err := syncSandboxBranch(clone, "child-alt", ""); err == nil {
		t.Fatal("a symlinked alternates was not refused")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "untouched\n" {
		t.Fatal("the refused fetch followed a symlink")
	}
	_ = os.Remove(alternates)
	if err := os.WriteFile(alternates, []byte(objects+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	refused := func(name string) {
		t.Helper()
		before, _ := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/child-alt")
		childCommit(t, clone, name)
		if _, err := syncSandboxBranch(clone, "child-alt", ""); err == nil {
			t.Fatalf("%s: the fetch was not refused", name)
		}
		if after, _ := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/child-alt"); after != before {
			t.Fatalf("%s: a refused fetch still moved wip/<id>", name)
		}
	}
	httpAlt := filepath.Join(clone, ".git", "objects", "info", "http-alternates")
	if err := os.WriteFile(httpAlt, []byte("https://example.com/objects\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused("http-alternates.txt")
	_ = os.Remove(httpAlt)

	parentAlt := filepath.Join(objects, "info", "alternates")
	if err := os.WriteFile(parentAlt, []byte(filepath.Join(elsewhere, ".git", "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused("nested-chain.txt")
	_ = os.Remove(parentAlt)

	before, _ := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/child-alt")
	childCommit(t, clone, "redirected-info.txt")
	info := filepath.Join(clone, ".git", "objects", "info")
	if err := os.Rename(info, info+".real"); err != nil {
		t.Fatal(err)
	}
	redirect := t.TempDir()
	if err := os.Symlink(redirect, info); err != nil {
		t.Fatal(err)
	}
	if _, err := syncSandboxBranch(clone, "child-alt", ""); err == nil {
		t.Fatal("redirected info: the fetch was not refused")
	}
	if after, _ := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/child-alt"); after != before {
		t.Fatal("redirected info: a refused fetch still moved wip/<id>")
	}
	if entries, _ := os.ReadDir(redirect); len(entries) != 0 {
		t.Fatal("the rewrite wrote through a redirected info directory")
	}
}

// A child cannot make the host fetch import another repository's objects by symlinking parts of
// its clone's .git (objects/pack, a loose-object fanout dir, or the wip/<id> ref) at paths the
// alternates reset never touches. Each is refused before the fetch, and wip/<id> does not move.
func TestHostFetchRefusesSymlinkedGitPaths(t *testing.T) {
	// Another host repository with a secret, as a sibling under the same root.
	secret := filepath.Join(t.TempDir(), "secret")
	initChildRepo(t, secret)
	if err := os.WriteFile(filepath.Join(secret, "TOPSECRET"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, secret, "add", ".")
	childRepoGit(t, secret, "commit", "-qm", "secret")
	secretGit := filepath.Join(secret, ".git")

	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, clone string)
	}{
		{"objects/pack", func(t *testing.T, clone string) {
			p := filepath.Join(clone, ".git", "objects", "pack")
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(secretGit, "objects", "pack"), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"loose fanout dir", func(t *testing.T, clone string) {
			if err := os.Symlink(filepath.Join(secretGit, "objects"), filepath.Join(clone, ".git", "objects", "ab")); err != nil {
				t.Fatal(err)
			}
		}},
		{"wip ref", func(t *testing.T, clone string) {
			ref := filepath.Join(clone, ".git", "refs", "heads", "wip")
			if err := os.RemoveAll(filepath.Dir(filepath.Dir(ref))); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(secretGit, "refs", "heads", "main"), ref); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "child-sym-" + strings.NewReplacer("/", "-", " ", "-").Replace(tc.name)
			repo, clone := cloneFixture(t, id)
			tc.plant(t, clone)
			if _, err := syncSandboxBranch(clone, id, ""); err == nil {
				t.Fatal("the fetch was not refused")
			}
			if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
				t.Fatal("a refused fetch still created wip/<id>")
			}
			if _, err := childRepoGitErr(repo, "cat-file", "-e", "HEAD:TOPSECRET"); err == nil {
				t.Fatal("the other repository's blob reached the parent")
			}
		})
	}
}

// A clone whose object store is over the cap is refused before the fetch, so a bomb pack never
// stalls the host.
func TestHostFetchRefusesAnOversizeClone(t *testing.T) {
	id := "child-big"
	repo, clone := cloneFixture(t, id)
	childCommit(t, clone, "work.txt")
	old := sandboxCloneMaxBytes
	sandboxCloneMaxBytes = 1
	t.Cleanup(func() { sandboxCloneMaxBytes = old })
	if _, err := syncSandboxBranch(clone, id, ""); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("an oversize clone was not refused: %v", err)
	}
	if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
		t.Fatal("a refused fetch still created wip/<id>")
	}
}

// The container is paused for the whole check-reset-fetch sequence and always unpaused after, so
// the clone cannot be swapped underneath it.
func TestHostFetchPausesTheContainerAroundTheSequence(t *testing.T) {
	id := "child-pause"
	_, clone := cloneFixture(t, id)
	childCommit(t, clone, "work.txt")
	var calls []string
	oldRun := dockerRunTimeout
	dockerRunTimeout = func(_ time.Duration, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { dockerRunTimeout = oldRun })
	if _, err := syncSandboxBranch(clone, id, "box-1"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "pause box-1" || calls[1] != "unpause box-1" {
		t.Fatalf("pause/unpause not bracketing the sequence: %v", calls)
	}
}

// The container is unpaused even when the sequence refuses the fetch.
func TestHostFetchUnpausesOnRefusal(t *testing.T) {
	id := "child-pause-refuse"
	_, clone := cloneFixture(t, id)
	if err := os.Symlink(filepath.Join(t.TempDir(), "x"), filepath.Join(clone, ".git", "objects", "pack", "evil")); err != nil {
		t.Fatal(err)
	}
	var unpaused bool
	oldRun := dockerRunTimeout
	dockerRunTimeout = func(_ time.Duration, args ...string) error {
		if strings.HasPrefix(strings.Join(args, " "), "unpause") {
			unpaused = true
		}
		return nil
	}
	t.Cleanup(func() { dockerRunTimeout = oldRun })
	if _, err := syncSandboxBranch(clone, id, "box-2"); err == nil {
		t.Fatal("a symlinked pack entry was not refused")
	}
	if !unpaused {
		t.Fatal("the container was left paused after a refusal")
	}
}

// The fetch reads <clone>/.git only, and only when it is a valid repository: a .git without HEAD
// is refused (no fallback to the working folder), and a normal clone still fetches.
func TestHostFetchNeedsAValidGitDir(t *testing.T) {
	id := "child-gitdir"
	repo, clone := cloneFixture(t, id)
	childCommit(t, clone, "work.txt")
	head := filepath.Join(clone, ".git", "HEAD")
	raw, err := os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if _, err := syncSandboxBranch(clone, id, ""); err == nil || !strings.Contains(err.Error(), "not a valid repository") {
		t.Fatalf("a .git without HEAD was not refused: %v", err)
	}
	if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
		t.Fatal("a refused fetch still created wip/<id>")
	}
	if err := os.WriteFile(head, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if commits, err := syncSandboxBranch(clone, id, ""); err != nil || !commits {
		t.Fatalf("a valid clone did not fetch: commits=%v err=%v", commits, err)
	}
}

// A .git with more entries than the cap is refused before the fetch.
func TestHostFetchRefusesTooManyEntries(t *testing.T) {
	id := "child-entries"
	_, clone := cloneFixture(t, id)
	old := sandboxCloneMaxEntries
	sandboxCloneMaxEntries = 5
	t.Cleanup(func() { sandboxCloneMaxEntries = old })
	if _, err := syncSandboxBranch(clone, id, ""); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("an over-cap .git was not refused: %v", err)
	}
}

// A failed unpause is retried once; a pause that fails refuses the fetch and still tries to unpause.
func TestHostFetchUnpauseRetriesAndPauseFailureRefuses(t *testing.T) {
	id := "child-unpause"
	_, clone := cloneFixture(t, id)
	childCommit(t, clone, "work.txt")
	var calls []string
	failUnpause := 1
	oldRun := dockerRunTimeout
	dockerRunTimeout = func(_ time.Duration, args ...string) error {
		calls = append(calls, args[0])
		if args[0] == "unpause" && failUnpause > 0 {
			failUnpause--
			return os.ErrDeadlineExceeded
		}
		return nil
	}
	t.Cleanup(func() { dockerRunTimeout = oldRun })
	if _, err := syncSandboxBranch(clone, id, "box-3"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "pause,unpause,unpause" {
		t.Fatalf("unpause not retried: %v", calls)
	}

	calls = nil
	dockerRunTimeout = func(_ time.Duration, args ...string) error {
		calls = append(calls, args[0])
		if args[0] == "pause" {
			return os.ErrDeadlineExceeded
		}
		return nil
	}
	if _, err := syncSandboxBranch(clone, id, "box-3"); err == nil || !strings.Contains(err.Error(), "could not be paused") {
		t.Fatalf("a failed pause did not refuse the fetch: %v", err)
	}
	if len(calls) < 2 || calls[1] != "unpause" {
		t.Fatalf("no unpause after a failed pause: %v", calls)
	}
}

// At turn end the fetch fails closed when the child's container is not known.
func TestTurnEndSyncFailsClosedWithoutAContainer(t *testing.T) {
	id := "child-nocontainer"
	repo, clone := cloneFixture(t, id)
	childCommit(t, clone, "work.txt")
	if err := syncSandboxAtTurnEnd(clone, id); err == nil || !strings.Contains(err.Error(), "not known") {
		t.Fatalf("the turn-end sync ran without a container: %v", err)
	}
	if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
		t.Fatal("a refused turn-end sync still created wip/<id>")
	}
}

// Regression for I-R2 (M-R6): the fetch must read <clone>/.git and nothing else. Here the clone's
// .git/HEAD holds garbage (a regular file, so the tree walk accepts it) and the working folder is
// laid out like a repository (HEAD, refs/heads/wip/<id>, objects/info/alternates) whose branch
// points at a commit in a separate temp repository. The sync must fail, and the parent must get
// neither wip/<id> nor that commit's object.
func TestHostFetchNeverReadsTheWorkingFolderAsARepository(t *testing.T) {
	id := "child-decoy"
	repo, clone := cloneFixture(t, id)
	childCommit(t, clone, "work.txt")

	decoy := filepath.Join(t.TempDir(), "decoy")
	initChildRepo(t, decoy)
	if err := os.WriteFile(filepath.Join(decoy, "marker.txt"), []byte("marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, decoy, "add", "marker.txt")
	childRepoGit(t, decoy, "commit", "-qm", "marker")
	marker := childRepoGit(t, decoy, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(clone, ".git", "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"HEAD":                    "ref: refs/heads/wip/" + id + "\n",
		"refs/heads/wip/" + id:    marker + "\n",
		"objects/info/alternates": filepath.Join(decoy, ".git", "objects") + "\n",
	} {
		p := filepath.Join(clone, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := syncSandboxBranch(clone, id, ""); err == nil {
		t.Fatal("the sync succeeded with a broken .git and a repository-shaped working folder")
	}
	if out, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
		t.Fatalf("the parent got wip/<id>: %s", out)
	}
	if _, err := childRepoGitErr(repo, "cat-file", "-e", marker); err == nil {
		t.Fatal("the separate repository's commit reached the parent")
	}
}
