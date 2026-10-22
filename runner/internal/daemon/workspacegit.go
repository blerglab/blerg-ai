package daemon

// A session's workspace, as host git reads it: the directory pinned by descriptor, and the
// repository in it vetted, so git reads the workspace's own repository and nothing a planted .git
// points at. Used by the commit lookup (commitlookup.go) and the session origin (sessionorigin.go);
// every git call goes through the shared hardened runner (internal/hostgit).

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

// pinnedGitDir is a directory git runs in, held open by descriptor: a name retargeted after the
// checks cannot make git read another repository.
type pinnedGitDir struct {
	root     *os.Root
	dir      *os.File
	info     os.FileInfo
	resolved string
	cwd      string
}

// pinGitDir opens path (symbolic links resolved) and, when expected is given, refuses it unless it
// is still that directory: the workspace the live launch was bound to.
func pinGitDir(path string, expected os.FileInfo) (*pinnedGitDir, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, false
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, false
	}
	if expected != nil && !workspaceRootUnchanged(root, resolved, expected) {
		_ = root.Close()
		return nil, false
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, false
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || (expected != nil && !os.SameFile(expected, info)) {
		_ = directory.Close()
		_ = root.Close()
		return nil, false
	}
	fd := strconv.FormatUint(uint64(directory.Fd()), 10)
	cwd := "/proc/self/fd/" + fd
	switch runtime.GOOS {
	case "linux":
	case "darwin":
		cwd = "/dev/fd/" + fd
	default:
		_ = directory.Close()
		_ = root.Close()
		return nil, false
	}
	return &pinnedGitDir{root: root, dir: directory, info: info, resolved: resolved, cwd: cwd}, true
}

func (p *pinnedGitDir) close() { _ = p.dir.Close(); _ = p.root.Close() }

// git runs one fixed git command in the pinned directory through the shared hardened runner
// (hostgit: its environment, overrides, transports refused, process group, memory limit), with
// safe.directory naming this directory alone and no repository looked for above it. extra are the
// call site's own -c overrides (key=value).
func (p *pinnedGitDir) gitWith(ctx context.Context, stdout io.Writer, extra []string, args ...string) error {
	// Go changes cwd before remapping ExtraFiles, so name the inherited parent descriptor.
	return hostgit.Run(ctx, hostgit.Options{
		Dir: p.cwd, ExtraFiles: []*os.File{p.dir}, SafeDirectory: p.resolved, Ceiling: filepath.Dir(p.resolved),
		MemoryLimit: true, Untrusted: true, Config: extra, Stdout: stdout,
	}, args...)
}

// git is gitWith for a commit read: paths quoted, rename detection bounded.
func (p *pinnedGitDir) git(ctx context.Context, stdout io.Writer, args ...string) error {
	return p.gitWith(ctx, stdout, []string{"core.quotePath=true", "diff.renameLimit=1000", "diff.renames=true"}, args...)
}

// gitFileMax bounds a .git file, commondir and gitdir read while vetting a repository.
const gitFileMax = 4096

func (p *pinnedGitDir) readSmall(name string) (string, bool) {
	f, err := p.root.Open(name)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, gitFileMax+1))
	if err != nil || len(raw) > gitFileMax {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

func readSmallFile(name string) (string, bool) {
	f, err := os.Open(name) //nolint:gosec // a path git itself would read, vetted by the caller; bounded read
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, gitFileMax+1))
	if err != nil || len(raw) > gitFileMax {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// borrowsObjects: the git directory's objects are not its own: objects is not a real directory of
// this user (a symbolic link to another repository's store), or it names alternates. Hard links or
// copied packs remain possible; they are only files the session could read anyway.
func borrowsObjects(gitDir string) bool {
	st, err := os.Lstat(filepath.Join(gitDir, "objects"))
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != os.Getuid() {
		return true
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if _, err := os.Lstat(filepath.Join(gitDir, "objects", "info", name)); err == nil {
			return true
		}
	}
	return false
}

// workspaceGitDir is the repository directory the workspace's .git names, refused unless it stays
// the workspace's own: a .git directory in it, or a .git file of a worktree whose gitdir is
// registered (worktrees/<name>, with a gitdir file naming this .git back) in a common directory.
// Neither may borrow objects from elsewhere (alternates, an objects directory that is a link)
// or redirect through a commondir. A
// symbolic link .git is refused. So a planted .git cannot point git at another repository.
func (p *pinnedGitDir) workspaceGitDir() (string, bool) {
	st, err := p.root.Lstat(".git")
	if err != nil {
		return "", false
	}
	own := filepath.Join(p.resolved, ".git")
	switch {
	case st.IsDir():
		if _, err := p.root.Lstat(".git/commondir"); err == nil {
			return "", false
		}
		if borrowsObjects(own) {
			return "", false
		}
		return own, true
	case st.Mode().IsRegular():
		line, ok := p.readSmall(".git")
		target, found := strings.CutPrefix(line, "gitdir: ")
		if !ok || !found || strings.ContainsAny(target, "\r\n\x00") {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(p.resolved, target)
		}
		gitDir, err := filepath.EvalSymlinks(target)
		if err != nil {
			return "", false
		}
		back, ok := readSmallFile(filepath.Join(gitDir, "gitdir"))
		if !ok {
			return "", false
		}
		if backInfo, err := os.Stat(filepath.Dir(back)); err != nil || !os.SameFile(backInfo, p.info) {
			return "", false
		}
		common, ok := readSmallFile(filepath.Join(gitDir, "commondir"))
		if !ok {
			return "", false
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		common, err = filepath.EvalSymlinks(common)
		if err != nil || filepath.Dir(gitDir) != filepath.Join(common, "worktrees") || borrowsObjects(common) {
			return "", false
		}
		return gitDir, true
	}
	return "", false
}

// vet checks what git itself will read: the top of the work tree it finds must be the pinned
// directory (a rename race cannot make it walk to a repository above), and, for a session's
// workspace, the git directory must be the one workspaceGitDir accepted. The repository a
// sandboxed session's clone was made from is the person's own (trusted is true): its git
// directory is not constrained, its top still is.
func (p *pinnedGitDir) vet(ctx context.Context, trusted bool) bool {
	want := ""
	if !trusted {
		dir, ok := p.workspaceGitDir()
		if !ok {
			return false
		}
		want = dir
	}
	out := &cappedOutput{max: 2 * gitFileMax, strict: true}
	if err := p.git(ctx, out, "rev-parse", "--show-toplevel", "--absolute-git-dir"); err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(out.buf.String()), "\n")
	if len(lines) != 2 {
		return false
	}
	top, err := os.Stat(lines[0])
	if err != nil || !os.SameFile(top, p.info) {
		return false
	}
	if want == "" {
		return true
	}
	got, err := filepath.EvalSymlinks(lines[1])
	return err == nil && got == want
}
