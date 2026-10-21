package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A sandboxed session never gets write access to any part of the person's repository: not its
// working tree, not its .git (refs, config, hooks, objects). That holds for every sandbox the daemon
// starts: a Local sandbox agent or terminal session from the launch sheet, a board's card run, a
// cron, and a sandboxed child. Instead the daemon gives the session a clone of its own:
//
//   - `git clone --shared` of the repository into <state>/sandbox-clones/<session-id>, on a new
//     branch wip/<session-id> starting at the repository's current commit. Only that clone is
//     mounted, writable, at /workspace. The repository itself is never mounted. A folder that is
//     not a repository yet (a new folder, a scratch folder) is made one first, with no commit, when
//     it is empty; a non-empty folder that is not a repository is refused.
//   - --shared makes the clone borrow the repository's objects through objects/info/alternates.
//     That objects directory is mounted READ-ONLY at its own absolute path, so the alternates
//     pointer resolves in the container and the session can read history but write nothing there.
//     New objects go into the clone's own object store.
//   - The session's work comes back by a fetch the HOST runs into the repository, for that one
//     branch only (+refs/heads/wip/<id>:refs/heads/wip/<id>), at the end of each turn and when
//     the session stops. The fetch ignores the clone's configuration and hooks as far as Git
//     allows (hardenedGitArgs, a clean environment). The session can therefore move nothing in the
//     repository but its own wip/<id> branch.
//   - When the session stops, anything it left uncommitted is committed onto wip/<id> inside the
//     container (sandboxCloneSettle), so the last fetch brings it back too.
//   - The daemon never runs host Git in the clone: whether the clone still holds work is asked
//     inside the container, its HEAD is read as plain files (readCloneHead), and the clone is
//     removed with a plain directory removal.
//
// What the clone needs is recorded by the daemon beside it (<session-id>.json, outside anything
// the session can reach): the repository, its objects directory and the start commit. A recovery
// mounts from that record, never from the clone's own (session-writable) alternates file.

const sandboxCloneDirName = "sandbox-clones"

// sandboxGitTimeout bounds each host git call: a hung command must not hold a spawn or a stop.
var sandboxGitTimeout = 2 * time.Minute

var sandboxCloneIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// sandboxCloneRoot is where the daemon keeps sandbox clones ("" when it has no state dir).
func sandboxCloneRoot() string {
	state := DaemonStateDir()
	if state == "" {
		return ""
	}
	return filepath.Join(state, sandboxCloneDirName)
}

// sandboxBranch names the branch a sandboxed session's commits land on.
func sandboxBranch(sessionID string) string { return "wip/" + sessionID }

// sandboxClone is the daemon's record of one session's clone (<root>/<id>.json).
type sandboxClone struct {
	Repo    string `json:"repo"`    // the repository's top-level directory on the host
	Objects string `json:"objects"` // its objects directory, mounted read-only for the alternates
	// Base is the commit the session started from; "" when the repository had no commit yet.
	Base string `json:"base"`
}

// hardenedGitArgs are the -c overrides every daemon git call carries, so that a hook or a
// configured command in a repository a sandboxed session could write never runs on the host.
var hardenedGitArgs = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "core.sshCommand=false",
	"-c", "protocol.ext.allow=never",
	"-c", "protocol.file.allow=always",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "fetch.fsckObjects=true",
}

// hostGit runs git in dir with hardenedGitArgs and a clean environment: no system or global
// configuration, no inherited GIT_* variable, no prompt. It is bounded by sandboxGitTimeout, not by
// a caller's context: it also runs from a session's kill, which must finish its cleanup whoever
// asked for it.
func hostGit(dir string, args ...string) (string, error) {
	return hostGitTimeout(sandboxGitTimeout, dir, args...)
}

// sandboxFetchTimeout bounds the host fetch of a session's branch; sandboxCloneMaxBytes bounds what the
// clone's own object store may hold for a fetch to be tried at all.
var (
	sandboxFetchTimeout          = 60 * time.Second
	sandboxCloneMaxBytes   int64 = 1 << 30
	sandboxCloneMaxEntries       = 200000
	// sandboxSyncBudget bounds a whole turn-end sync: pause, checks, fetch, rev-parse, unpause.
	sandboxSyncBudget = 80 * time.Second
	// sandboxDockerTimeout bounds one docker pause or unpause.
	sandboxDockerTimeout = 10 * time.Second
)

// dockerRunTimeout is dockerRun bounded by timeout. A var so tests can stub it.
var dockerRunTimeout = func(timeout time.Duration, args ...string) error {
	cmd := exec.Command("docker", args...) //nolint:gosec // literal docker binary, daemon-built argv; bounded by the timer below
	if err := cmd.Start(); err != nil {
		return err
	}
	timer := time.AfterFunc(timeout, func() { _ = cmd.Process.Kill() })
	err := cmd.Wait()
	timer.Stop()
	return err
}

// unpauseSandbox unpauses a sandboxed session's container, retrying once; a failure is logged loudly, since a
// session left paused would hang with no sign of why.
func unpauseSandbox(container, sessionID string) {
	for attempt := 1; attempt <= 2; attempt++ {
		if err := dockerRunTimeout(sandboxDockerTimeout, "unpause", container); err == nil {
			return
		} else if attempt == 2 {
			log.Printf("SANDBOX CONTAINER LEFT PAUSED: agent session %s: docker unpause %s failed twice: %v; run `docker unpause %s`", sessionID, container, err, container)
		}
	}
}

// syncSandboxAtTurnEnd is the turn-end sync of a sandboxed session: its container must be known (fail
// closed: a fetch never runs against a clone that could be changing).
func syncSandboxAtTurnEnd(path, sessionID string) error {
	container, ok := sandboxContainer(sessionID)
	if !ok {
		return fmt.Errorf("fetch %s refused: the session's container is not known, so it cannot be paused", sandboxBranch(sessionID))
	}
	_, err := syncSandboxBranch(path, sessionID, container)
	return err
}

// checkSandboxCloneTree refuses a clone whose .git a fetch must not read: anything under it that is
// a symbolic link or not a plain file or directory, a .git that is not a directory, a commondir
// file (which points Git at another directory), or an object store over sandboxCloneMaxBytes. It
// never follows a link, so nothing it accepts resolves outside the clone.
func checkSandboxCloneTree(path string) error {
	for _, p := range []string{path, filepath.Join(path, ".git")} {
		if st, err := os.Lstat(p); err != nil || !st.IsDir() {
			return errors.New("the clone or its .git is not a plain directory")
		}
	}
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Lstat(filepath.Join(gitDir, "commondir")); err == nil {
		return errors.New("the clone's .git has a commondir file")
	}
	// The fetch reads <clone>/.git and nothing else (syncSandboxBranch): it must be a repository.
	for _, need := range []struct {
		name string
		dir  bool
	}{{"HEAD", false}, {"objects", true}, {"refs", true}} {
		st, err := os.Lstat(filepath.Join(gitDir, need.name))
		if err != nil || st.IsDir() != need.dir || (!need.dir && !st.Mode().IsRegular()) {
			return errors.New("the clone's .git is not a valid repository (no " + need.name + ")")
		}
	}
	var objects int64
	entries := 0
	objectsDir := filepath.Join(gitDir, "objects") + string(filepath.Separator)
	err := filepath.WalkDir(gitDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entries++; entries > sandboxCloneMaxEntries {
			return fmt.Errorf("the clone's .git has more than %d entries", sandboxCloneMaxEntries)
		}
		mode := d.Type()
		if mode&os.ModeSymlink != 0 || (!mode.IsDir() && !mode.IsRegular()) {
			rel, _ := filepath.Rel(path, p)
			return fmt.Errorf("the clone's %s is not a plain file or directory", rel)
		}
		if mode.IsRegular() && strings.HasPrefix(p, objectsDir) {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if objects += info.Size(); objects > sandboxCloneMaxBytes {
				return fmt.Errorf("the clone's objects exceed %d bytes", sandboxCloneMaxBytes)
			}
		}
		return nil
	})
	return err
}

func hostGitTimeout(timeout time.Duration, dir string, args ...string) (string, error) {
	full := append(append([]string{"-C", dir}, hardenedGitArgs...), args...)
	cmd := exec.Command("git", full...) //nolint:gosec,noctx // literal git binary; dir and args are daemon-built (validated session id, resolved repo path); bounded by the timer below
	// Its own process group, so a timeout stops the helpers it starts (upload-pack, index-pack) too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	timer := time.AfterFunc(timeout, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	err := cmd.Wait()
	timer.Stop()
	return strings.TrimSpace(out.String()), err
}

func sandboxClonePaths(sessionID string) (dir, record string, ok bool) {
	root := sandboxCloneRoot()
	if root == "" || !sandboxCloneIDPattern.MatchString(sessionID) {
		return "", "", false
	}
	return filepath.Join(root, sessionID), filepath.Join(root, sessionID+".json"), true
}

// isSandboxClone reports whether path is the daemon-made clone of sessionID.
func isSandboxClone(path, sessionID string) bool {
	dir, _, ok := sandboxClonePaths(sessionID)
	return ok && filepath.Clean(path) == dir
}

// createSandboxClone makes the sandboxed session sessionID its own clone of the repository at
// repoPath, on a new branch wip/<sessionID> at the repository's current commit, and returns the
// clone's path. It never returns repoPath: any failure is an error, and the caller refuses the
// start rather than mounting the person's checkout.
//
// An empty folder that is not a repository yet (a new folder, a scratch folder) is made one first,
// with no commit: the clone then starts with no history, and wip/<sessionID> is the first branch to
// get commits. A folder with content that is not the top of a repository is refused.
func createSandboxClone(repoPath, sessionID, reposRoot string) (string, error) {
	refuse := func(why string) (string, error) {
		return "", fmt.Errorf("could not give this sandboxed session its own clone of the repository: %s; the session was not started (a sandbox never works in your checkout)", why)
	}
	dir, record, ok := sandboxClonePaths(sessionID)
	if !ok {
		return refuse("invalid session id, or the daemon has no state directory")
	}
	repo, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return refuse("the repository folder is missing")
	}
	if _, err := os.Lstat(filepath.Join(repo, ".git")); os.IsNotExist(err) {
		// Never inside one of the person's checkouts (an empty subfolder of a checkout): a
		// repository made there would be embedded in theirs.
		if top, err := hostGit(filepath.Dir(repo), "rev-parse", "--show-toplevel"); err == nil && insideACheckout(top, reposRoot) {
			return refuse(filepath.Base(repo) + " is an empty folder inside the Git repository " + filepath.Base(top) + "; a sandbox works in a clone of a whole repository, so start it on " + filepath.Base(top) + " or on a folder outside it")
		}
		if !emptyDir(repo) {
			return refuse(filepath.Base(repo) + " is not a Git repository and is not empty (make it one with git init and a first commit, or run the session on this machine unsandboxed)")
		}
		if out, err := hostGit(repo, "init", "--quiet", "--initial-branch=main"); err != nil {
			log.Printf("sandbox clone %s: git init: %v: %s", sessionID, err, out)
			return refuse(filepath.Base(repo) + " is not a Git repository, and it could not be made one: " + firstLine(out))
		}
	}
	top, err := hostGit(repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return refuse(filepath.Base(repo) + " is not a Git repository")
	}
	if resolved, e := filepath.EvalSymlinks(top); e != nil || resolved != repo {
		return refuse(filepath.Base(repo) + " is not the top of a Git repository")
	}
	// No commit yet (a new repository): the clone starts with none either.
	base, err := hostGit(repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		base = ""
	}
	if _, err := hostGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+sandboxBranch(sessionID)); err == nil {
		return refuse("the repository already has a branch " + sandboxBranch(sessionID))
	}
	common, err := hostGit(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return refuse("its Git directory could not be read")
	}
	objects, err := filepath.EvalSymlinks(filepath.Join(common, "objects"))
	if err != nil || validCloneObjects(objects) != nil {
		return refuse("its object store is not a plain directory owned by this user")
	}
	if raw, e := os.ReadFile(filepath.Join(objects, "info", "alternates")); e == nil && len(bytes.TrimSpace(raw)) > 0 {
		return refuse("repositories that borrow objects from another repository (alternates) are not supported")
	}
	root := filepath.Dir(dir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return refuse("the clone directory could not be created")
	}
	if _, err := os.Lstat(dir); err == nil {
		return refuse("a clone for this session already exists")
	}
	if out, err := hostGit(root, "clone", "--quiet", "--shared", "--no-checkout", "--no-tags", repo, dir); err != nil {
		_ = os.RemoveAll(dir)
		log.Printf("sandbox clone %s: git clone: %v: %s", sessionID, err, out)
		return refuse("git clone failed: " + firstLine(out))
	}
	branchArgs := []string{"checkout", "--quiet", "-b", sandboxBranch(sessionID), base}
	if base == "" {
		branchArgs = []string{"symbolic-ref", "HEAD", "refs/heads/" + sandboxBranch(sessionID)}
	}
	if out, err := hostGit(dir, branchArgs...); err != nil {
		_ = os.RemoveAll(dir)
		log.Printf("sandbox clone %s: git %s: %v: %s", sessionID, branchArgs[0], err, out)
		return refuse("git " + branchArgs[0] + " failed: " + firstLine(out))
	}
	// The clone's own copy of the repository's default branch goes: a clone that holds only
	// wip/<id> is one the settle probe can call clean once that branch is brought back.
	if out, err := hostGit(dir, "for-each-ref", "--format=%(refname)", "refs/heads/"); err == nil {
		for _, ref := range strings.Fields(out) {
			if ref != "refs/heads/"+sandboxBranch(sessionID) {
				_, _ = hostGit(dir, "update-ref", "-d", ref)
			}
		}
	}
	raw, _ := json.Marshal(sandboxClone{Repo: repo, Objects: objects, Base: base})
	if err := os.WriteFile(record, raw, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return refuse("the clone's record could not be written")
	}
	return dir, nil
}

// insideACheckout reports whether top, the work tree that encloses a new folder, is a checkout of
// the person's under the repos root (strictly inside it). A repos root that itself sits inside a
// repository (a home directory kept in Git) is not one: New folders and scratch folders directly
// under it are made repositories of their own as usual. With no repos root known, any enclosing
// work tree counts.
func insideACheckout(top, reposRoot string) bool {
	if reposRoot == "" {
		return true
	}
	root, err := filepath.EvalSymlinks(reposRoot)
	if err != nil {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	return strings.HasPrefix(top, root+string(filepath.Separator))
}

// emptyDir reports whether dir is a directory with nothing in it.
func emptyDir(dir string) bool {
	f, err := os.Open(dir) //nolint:gosec // the session's own resolved workspace folder, only listed
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(1)
	return len(names) == 0 && errors.Is(err, io.EOF)
}

// validCloneObjects checks a repository objects directory before it is mounted (read-only) into
// a container: absolute, resolved, a directory named "objects" owned by this user and not
// writable by others, inside a Git directory, and not a reserved container path.
func validCloneObjects(objects string) error {
	bad := errors.New("invalid objects directory")
	if !filepath.IsAbs(objects) || filepath.Base(objects) != "objects" || !safeSandboxGitTarget(objects) {
		return bad
	}
	if resolved, err := filepath.EvalSymlinks(objects); err != nil || resolved != objects {
		return bad
	}
	st, err := os.Lstat(objects)
	if err != nil || !safeSandboxGitInfo(st, true) {
		return bad
	}
	if head, err := os.Lstat(filepath.Join(filepath.Dir(objects), "HEAD")); err != nil || !head.Mode().IsRegular() {
		return bad
	}
	return nil
}

// loadSandboxClone reads the daemon's record of sessionID's clone, re-validated.
func loadSandboxClone(sessionID string) (*sandboxClone, error) {
	_, record, ok := sandboxClonePaths(sessionID)
	if !ok {
		return nil, errors.New("no sandbox clone")
	}
	raw, err := os.ReadFile(record) //nolint:gosec // daemon-owned record under the private clone root, named from a validated id
	if err != nil {
		return nil, err
	}
	var c sandboxClone
	if json.Unmarshal(raw, &c) != nil || c.Repo == "" || (c.Base != "" && !protocol.ValidSessionOriginCommit(c.Base)) || validCloneObjects(c.Objects) != nil {
		return nil, errors.New("the sandbox clone's record is invalid")
	}
	return &c, nil
}

// sandboxCloneMount is the read-only mount of the repository's objects a clone's alternates
// need, at their own absolute path.
func sandboxCloneMount(sessionID string) (string, error) {
	c, err := loadSandboxClone(sessionID)
	if err != nil {
		return "", fmt.Errorf("the sandboxed session's clone cannot be mounted: %w", err)
	}
	return sandboxBindMount(c.Objects, c.Objects) + ",readonly", nil
}

// resetCloneAlternates rewrites, on the host and right before a fetch, the clone's
// objects/info/alternates from the daemon's own record, so a session that rewrote it cannot make the
// host's fetch read objects from anywhere else. It refuses (and the fetch is skipped) when the
// clone's .git, objects or objects/info is not a plain directory of this user (a symlink could
// redirect the write), when objects/info/http-alternates exists, or when the repository's own
// objects borrow from another store (a nested chain).
func resetCloneAlternates(path string, c *sandboxClone) error {
	info := filepath.Join(path, ".git", "objects", "info")
	for _, dir := range []string{filepath.Join(path, ".git"), filepath.Join(path, ".git", "objects"), info} {
		st, err := os.Lstat(dir)
		if err != nil || !safeSandboxGitInfo(st, true) {
			return errors.New("the clone's object directories are not plain directories of this user")
		}
	}
	if validCloneObjects(c.Objects) != nil {
		return errors.New("the repository's objects directory is no longer valid")
	}
	if raw, err := os.ReadFile(filepath.Join(c.Objects, "info", "alternates")); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		return errors.New("the repository's objects borrow from another store (a nested alternates chain)")
	}
	if _, err := os.Lstat(filepath.Join(c.Objects, "info", "http-alternates")); err == nil {
		return errors.New("the repository has objects/info/http-alternates")
	}
	root, err := os.OpenRoot(info)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat("http-alternates"); err == nil {
		return errors.New("the clone has objects/info/http-alternates")
	}
	// Written to a new name and renamed over: a rename replaces whatever "alternates" is (a
	// symlink included) without following it, and the root keeps every step inside info.
	tmp := "alternates.blerg-tmp"
	_ = root.Remove(tmp)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(c.Objects + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = root.Remove(tmp)
		return errors.New("the clone's alternates could not be rewritten")
	}
	if err := root.Rename(tmp, "alternates"); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// syncSandboxBranch brings the session's wip/<id> into the repository: a fetch the host runs for
// that one branch, hardened against the clone's own configuration and hooks. It reports whether
// the branch now exists in the repository with commits of its own.
//
// container, when the session's container still runs, is paused for the whole check-reset-fetch
// sequence and always unpaused after, so the clone cannot change between the checks and the fetch.
// At stop the container is already gone ("").
func syncSandboxBranch(path, sessionID, container string) (bool, error) {
	if !isSandboxClone(path, sessionID) {
		return false, errors.New("not a sandbox clone")
	}
	c, err := loadSandboxClone(sessionID)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(sandboxSyncBudget)
	if container != "" {
		if err := dockerRunTimeout(sandboxDockerTimeout, "pause", container); err != nil {
			unpauseSandbox(container, sessionID) // a pause that timed out may still have happened
			return false, fmt.Errorf("fetch %s refused: the session's container could not be paused: %w", sandboxBranch(sessionID), err)
		}
		defer unpauseSandbox(container, sessionID)
	}
	if err := checkSandboxCloneTree(path); err != nil {
		return false, fmt.Errorf("fetch %s refused: %w", sandboxBranch(sessionID), err)
	}
	if err := resetCloneAlternates(path, c); err != nil {
		return false, fmt.Errorf("fetch %s refused: %w", sandboxBranch(sessionID), err)
	}
	branch := "refs/heads/" + sandboxBranch(sessionID)
	// A clone of a repository with no commit has no wip/<id> until the session's first commit:
	// nothing to bring back yet. Read as plain files (the tree check above refused any link).
	if c.Base == "" {
		if _, ok := readCloneRef(path, branch); !ok {
			return false, nil
		}
	}
	// The person may have wip/<id> checked out (to look at the work while the session runs): Git
	// refuses to fetch into a checked-out branch, and moving it under their working tree would be
	// wrong anyway. The commits then go to wip/<id>.incoming, and the caller says so once.
	target := branch
	if checkedOutAnywhere(c.Repo, branch) {
		target = branch + ".incoming"
	}
	// Fetched from <clone>/.git, never the clone's top folder (which a session could make look like
	// a bare repository), and with upload-pack --strict, which refuses anything but that exact Git
	// directory.
	fetchTimeout := min(sandboxFetchTimeout, time.Until(deadline)-2*sandboxDockerTimeout)
	if fetchTimeout <= 0 {
		return false, fmt.Errorf("fetch %s refused: the sync ran out of time", sandboxBranch(sessionID))
	}
	if out, err := hostGitTimeout(fetchTimeout, c.Repo, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"--upload-pack=git-upload-pack --strict", filepath.Join(path, ".git"), "+"+branch+":"+target); err != nil {
		return false, fmt.Errorf("fetch %s from the session's clone: %w: %s", sandboxBranch(sessionID), err, firstLine(out))
	}
	if target != branch {
		return true, &sandboxCheckedOutError{sessionID: sessionID}
	}
	tip, err := hostGitTimeout(10*time.Second, c.Repo, "rev-parse", "--verify", "--quiet", branch)
	if err != nil {
		return false, fmt.Errorf("read %s after the fetch: %w", sandboxBranch(sessionID), err)
	}
	return tip != c.Base, nil
}

// sandboxCheckedOutError says the session's commits were fetched, but into wip/<id>.incoming,
// because the person has wip/<id> checked out. Not a failure: said once per session.
type sandboxCheckedOutError struct{ sessionID string }

func (e *sandboxCheckedOutError) Error() string {
	b := sandboxBranch(e.sessionID)
	return b + " is checked out in your repository, so the session's commits are on " + b + ".incoming instead; switch away from " + b + " (or merge " + b + ".incoming into it) and they land on " + b + " again"
}

// checkedOutAnywhere reports whether ref is the checked-out branch of the repository at repo or of
// any of its worktrees. Read with the hardened host Git in the person's repository (never the clone).
func checkedOutAnywhere(repo, ref string) bool {
	out, err := hostGitTimeout(10*time.Second, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "branch "+ref {
			return true
		}
	}
	return false
}

// sandboxSettleScript runs in the session's container when the session stops. Work left
// uncommitted on wip/<id> is committed there first (never through host Git, and with the clone's
// hooks and fsmonitor off, so the session's own setup cannot stop it), so the last fetch brings it
// back. Then it says whether the clone still holds anything a fetch of wip/<id> would not bring
// back: a HEAD off wip/<id>, uncommitted changes, a stash or another branch. Exit 0: clean;
// 1: not clean; anything else: unknown.
func sandboxSettleScript(sessionID string) string {
	// g: the clone's hooks and fsmonitor off, and every untracked file shown whatever the clone's
	// configuration says. untracked: files Git does not track, honoring only the work tree's own
	// .gitignore files, never .git/info/exclude or core.excludesFile (which the session could use
	// to hide work from the check and have the clone removed with it).
	return `cd /workspace || exit 3
g() { git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c commit.gpgsign=false -c status.showUntrackedFiles=all -c core.excludesFile=/dev/null "$@"; }
untracked() { g ls-files -z --others --exclude-per-directory=.gitignore; }
b=refs/heads/` + sandboxBranch(sessionID) + `
dirty() { [ -n "$(g status --porcelain --untracked-files=all)" ] || [ "$(untracked | wc -c)" -ne 0 ]; }
if [ "$(g symbolic-ref -q HEAD)" = "$b" ] && dirty; then
  g add -A || exit 1
  untracked | g update-index --add -z --stdin || exit 1
  if [ -z "$(g config user.email)" ]; then set -- -c user.name=Blerg -c user.email=blerg@localhost; fi
  g "$@" commit -q --no-verify -m "` + sandboxSnapshotMessage + `" || exit 1
fi
test "$(g symbolic-ref -q HEAD)" = "$b" || exit 1
dirty && exit 1
test -z "$(g for-each-ref --format=x refs/heads/ refs/stash | sed 1d)" || exit 1
exit 0`
}

// sandboxSnapshotMessage is the subject of the commit that holds a stopped session's uncommitted work.
const sandboxSnapshotMessage = "Uncommitted work when the session stopped (committed by Blerg)"

// sandboxCloneSettle runs sandboxSettleScript in the session's container. It runs there, never on
// the host, because it reads the clone's (session-writable) configuration. ok is false when the
// answer is unknown (no container, a timeout): the clone is then kept.
var sandboxCloneSettle = func(container, sessionID string) (clean, ok bool) {
	cmd := exec.Command("docker", "exec", "-w", sandboxWorkdir, container, "sh", "-c", sandboxSettleScript(sessionID)) //nolint:gosec // literal docker binary, daemon-built argv (validated session id); bounded by the timer below
	if err := cmd.Start(); err != nil {
		return false, false
	}
	timer := time.AfterFunc(sandboxSettleTimeout, func() { _ = cmd.Process.Kill() })
	err := cmd.Wait()
	timer.Stop()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, true
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, true
	}
	return false, false
}

// sandboxSettleTimeout bounds the stop-time commit and probe in the container.
var sandboxSettleTimeout = 60 * time.Second

// releaseSandboxClone is the end of a sandboxed session's clone, when its session is stopped or
// discarded for good: its branch is fetched into the repository first; a branch without commits
// of its own is then deleted from the repository (it holds nothing), and the clone is removed
// only when the settle step, run in the container before it went, said it holds nothing else.
// Work is never deleted. A path that is not a daemon-made sandbox clone is left alone.
func releaseSandboxClone(path, sessionID string, clean, known bool) {
	if !isSandboxClone(path, sessionID) {
		return
	}
	dir, record, _ := sandboxClonePaths(sessionID)
	c, err := loadSandboxClone(sessionID)
	if err != nil {
		log.Printf("sandbox clone %s: kept at %s: %v", sessionID, dir, err)
		return
	}
	commits, err := syncSandboxBranch(path, sessionID, "")
	var checkedOut *sandboxCheckedOutError
	if errors.As(err, &checkedOut) {
		log.Printf("sandbox clone %s: %v", sessionID, err)
	} else if err != nil {
		log.Printf("sandbox clone %s: kept at %s: %v", sessionID, dir, err)
		return
	}
	branch := "refs/heads/" + sandboxBranch(sessionID)
	switch {
	case commits:
		log.Printf("sandbox clone %s: branch %s kept in %s: it has commits", sessionID, sandboxBranch(sessionID), c.Repo)
	case c.Base != "":
		if out, err := hostGit(c.Repo, "update-ref", "-d", branch, c.Base); err != nil {
			log.Printf("sandbox clone %s: branch %s kept: %v: %s", sessionID, sandboxBranch(sessionID), err, out)
		}
	}
	if !known || !clean {
		log.Printf("sandbox clone %s: kept at %s: it may hold work that is not on %s", sessionID, dir, sandboxBranch(sessionID))
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("sandbox clone %s: could not be removed: %v", sessionID, err)
		return
	}
	_ = os.Remove(record)
}

// sandboxFinishing serializes finishSandbox per session: a terminal's kill and the end of its
// attach stream can both reach it. One small mutex per sandboxed session the daemon ever ended.
var sandboxFinishing sync.Map

// finishSandbox ends a sandboxed session's container and clone for good: the stop-time commit and
// probe run in the container while it still exists, then the container is removed, then the
// clone's branch is fetched into the repository and the clone removed if nothing else is in it.
// Safe to call twice and for a session that never had a clone (then it only removes the
// container).
func finishSandbox(sessionID string) {
	v, _ := sandboxFinishing.LoadOrStore(sessionID, &sync.Mutex{})
	mu, _ := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock() // kept in the map: a delete here would let a third caller run beside a second
	dir, record, ok := sandboxClonePaths(sessionID)
	clone := false
	if ok {
		if _, err := os.Lstat(record); err == nil {
			clone = true
		}
	}
	clean, known := false, false
	if clone {
		container, registered := sandboxContainer(sessionID)
		if !registered {
			container = dockerContainerName(sessionID) // a container from before a restart
		}
		clean, known = sandboxCloneSettle(container, sessionID)
	}
	removeSandboxContainer(sessionID)
	if !clone {
		return
	}
	// The last fetch runs without a pause because the container is gone. If it is not (a failed
	// docker rm), the clone could still change under the fetch: nothing is fetched, the clone is
	// kept, and the log says so.
	if !sandboxContainerGone(dockerContainerName(sessionID)) {
		log.Printf("sandbox clone %s: kept at %s, not fetched: its container %s could not be removed; remove it (docker rm -f) and the clone's wip/%s can be fetched by hand", sessionID, dir, dockerContainerName(sessionID), sessionID)
		return
	}
	releaseSandboxClone(dir, sessionID, clean, known)
}

// sandboxContainerGone reports whether docker says no container of that name exists (any state).
// An error is "not known to be gone".
var sandboxContainerGone = func(container string) bool {
	out, err := dockerOutput("ps", "-a", "--filter", "name=^/"+container+"$", "--format", "{{.Names}}")
	return err == nil && strings.TrimSpace(string(out)) == ""
}

// readCloneHead reads the commit a sandbox clone's HEAD names, as plain files and never through
// Git (whose configuration in the clone the session controls): HEAD, then the loose ref or
// packed-refs. A HEAD on a branch with no commit yet is ("", true). Any link, odd entry or
// malformed content is refused (ok false).
func readCloneHead(path string) (commit string, ok bool) {
	head, ok := readCloneFile(path, "HEAD", 512)
	if !ok {
		return "", false
	}
	head = strings.TrimSpace(head)
	if ref, isRef := strings.CutPrefix(head, "ref: "); isRef {
		if commit, found := readCloneRef(path, ref); found {
			return commit, true
		}
		if !validCloneRefName(ref) {
			return "", false
		}
		return "", true // a branch with no commit yet
	}
	if !protocol.ValidSessionOriginCommit(head) {
		return "", false
	}
	return head, true
}

// readCloneRef resolves ref (refs/heads/...) in the clone at path from plain files.
func readCloneRef(path, ref string) (string, bool) {
	if !validCloneRefName(ref) {
		return "", false
	}
	if raw, ok := readCloneFile(path, ref, 512); ok {
		id := strings.TrimSpace(raw)
		return id, protocol.ValidSessionOriginCommit(id)
	}
	packed, ok := readCloneFile(path, "packed-refs", 4<<20)
	if !ok {
		return "", false
	}
	for _, line := range strings.Split(packed, "\n") {
		id, name, found := strings.Cut(strings.TrimSpace(line), " ")
		if found && name == ref && protocol.ValidSessionOriginCommit(id) {
			return id, true
		}
	}
	return "", false
}

var cloneRefNamePattern = regexp.MustCompile(`^refs/heads/[A-Za-z0-9._/-]{1,200}$`)

func validCloneRefName(ref string) bool {
	return cloneRefNamePattern.MatchString(ref) && !strings.Contains(ref, "..") && !strings.Contains(ref, "//") && !strings.HasSuffix(ref, "/")
}

// readCloneFile reads <path>/.git/<name>: a regular file, no link anywhere on the way, at most max
// bytes.
func readCloneFile(path, name string, maxBytes int64) (string, bool) {
	gitDir := filepath.Join(path, ".git")
	if st, err := os.Lstat(gitDir); err != nil || !st.IsDir() {
		return "", false
	}
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return "", false
	}
	defer func() { _ = root.Close() }()
	// Every component is checked with Lstat, so a link inside the clone is refused as well.
	parts := strings.Split(name, "/")
	for i := range parts {
		st, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return "", false
		}
		if i < len(parts)-1 {
			if !st.IsDir() {
				return "", false
			}
			continue
		}
		if !st.Mode().IsRegular() || st.Size() > maxBytes {
			return "", false
		}
	}
	// Non-blocking, and checked to be the same regular file after the open: a FIFO swapped in
	// between the Lstat and the open must not block the read.
	before, err := root.Lstat(name)
	if err != nil {
		return "", false
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return "", false
	}
	return string(raw), true
}

// discardSandboxClone removes a clone made for a spawn that failed before the session existed:
// nothing ran in it.
func discardSandboxClone(path, sessionID string) {
	if !isSandboxClone(path, sessionID) {
		return
	}
	dir, record, _ := sandboxClonePaths(sessionID)
	_ = os.RemoveAll(dir)
	_ = os.Remove(record)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	if line == "" {
		return "unknown error"
	}
	return line
}
