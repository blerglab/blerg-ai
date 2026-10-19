package daemon

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type sandboxGitMetadata struct {
	source, target                       string
	directories                          map[string]os.FileInfo
	dot, pointer, backlinkPath, backlink string
}

// A linked checkout contains a .git FILE pointing outside the checkout. Mount
// only its verified common Git directory, never the containing host repository
// or a user profile. Shared refs/objects are writable so children can commit;
// this intentionally is not isolation from other worktrees of the same repo.
func sandboxWorktreeGit(project string) (*sandboxGitMetadata, error) {
	refuse := errors.New("sandbox worktree Git metadata is invalid or not owned by the daemon user")
	root, err := filepath.EvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dot := filepath.Join(root, ".git")
	info, err := os.Lstat(dot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, refuse
	}
	if info.IsDir() {
		return nil, nil
	}
	if !safeSandboxGitInfo(info, false) {
		return nil, refuse
	}
	raw, err := sandboxGitRead(dot)
	if err != nil || !strings.HasPrefix(raw, "gitdir: ") {
		return nil, refuse
	}
	pointer := strings.TrimPrefix(raw, "gitdir: ")
	if pointer == "" {
		return nil, refuse
	}
	hostGit := pointer
	targetGit := pointer
	if !filepath.IsAbs(pointer) {
		hostGit = filepath.Join(root, pointer)
		targetGit = filepath.Join(sandboxWorkdir, pointer)
	}
	hostGit, err = filepath.EvalSymlinks(hostGit)
	if err != nil {
		return nil, refuse
	}
	commonRef, err := sandboxGitRead(filepath.Join(hostGit, "commondir"))
	if err != nil {
		return nil, refuse
	}
	common := commonRef
	if !filepath.IsAbs(common) {
		common = filepath.Join(hostGit, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return nil, refuse
	}
	// Git's worktree metadata must be exactly common/worktrees/<id>, with a
	// reciprocal backlink to this checkout. A crafted .git file is not authority
	// to mount arbitrary host data, even when that data is owned by this user.
	if (filepath.Base(common) != ".git" && !strings.HasSuffix(filepath.Base(common), ".git")) || filepath.Dir(filepath.Dir(hostGit)) != common || filepath.Base(filepath.Dir(hostGit)) != "worktrees" {
		return nil, refuse
	}
	directories := map[string]os.FileInfo{}
	for _, dir := range []string{common, filepath.Join(common, "worktrees"), hostGit, filepath.Join(common, "objects"), filepath.Join(common, "objects", "info"), filepath.Join(common, "refs")} {
		st, e := os.Lstat(dir)
		if e != nil || !safeSandboxGitInfo(st, true) {
			return nil, refuse
		}
		directories[dir] = st
	}
	backlink, err := sandboxGitRead(filepath.Join(hostGit, "gitdir"))
	if err != nil {
		return nil, refuse
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(hostGit, backlink)
	}
	backlink, err = filepath.EvalSymlinks(backlink)
	if err != nil || backlink != dot {
		return nil, refuse
	}
	for _, p := range []string{filepath.Join(common, "HEAD"), filepath.Join(hostGit, "HEAD")} {
		if _, e := sandboxGitRead(p); e != nil {
			return nil, refuse
		}
	}
	if st, e := os.Lstat(filepath.Join(common, "config")); e != nil || !safeSandboxGitInfo(st, false) {
		return nil, refuse
	}
	if alternate, e := sandboxGitRead(filepath.Join(common, "objects", "info", "alternates")); e != nil && !os.IsNotExist(e) || len(strings.TrimSpace(alternate)) != 0 {
		return nil, errors.New("sandbox worktrees with external Git object alternates are unsupported")
	}
	target := filepath.Clean(filepath.Dir(filepath.Dir(targetGit)))
	if !filepath.IsAbs(target) || !safeSandboxGitTarget(target) {
		return nil, refuse
	}
	return &sandboxGitMetadata{source: common, target: target, directories: directories, dot: dot, pointer: raw, backlinkPath: filepath.Join(hostGit, "gitdir"), backlink: backlink}, nil
}
func safeSandboxGitInfo(info os.FileInfo, dir bool) bool {
	return info != nil && ownedByDaemon(info) && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0002 == 0 && ((dir && info.IsDir()) || (!dir && info.Mode().IsRegular()))
}
func sandboxGitRead(path string) (string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !safeSandboxGitInfo(info, false) || info.Size() > 8192 {
		return "", errors.New("invalid git metadata file")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("git metadata file changed while reading")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return "", err
	}
	if len(raw) > 8192 {
		return "", errors.New("git metadata file is too large")
	}
	s := strings.TrimSpace(string(raw))
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", errors.New("invalid git metadata value")
	}
	return s, nil
}

func safeSandboxGitTarget(path string) bool {
	for _, reserved := range []string{"/", "/workspace", "/home/agent", "/etc", "/usr", "/bin", "/sbin", "/proc", "/sys", "/dev", "/run", "/root", "/var/run"} {
		if path == reserved || (reserved != "/" && strings.HasPrefix(path, reserved+string(filepath.Separator))) {
			return false
		}
	}
	return true
}
func (m *sandboxGitMetadata) mountArg() string {
	return sandboxBindMount(m.source, m.target)
}
func sandboxBindMount(source, target string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"type=bind", "src=" + source, "dst=" + target})
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// Recheck directly before Docker resolves the bind source, including directory
// identity rather than merely following a replaced pathname. The repository
// remains trusted same-user/group storage; this is not an adversarial host-FS
// boundary against an actor allowed to mutate it concurrently with Docker.
func (m *sandboxGitMetadata) validate() error {
	if m == nil {
		return nil
	}
	refuse := errors.New("sandbox worktree Git metadata changed before launch")
	for path, original := range m.directories {
		now, err := os.Lstat(path)
		if err != nil || !safeSandboxGitInfo(now, true) || !os.SameFile(now, original) {
			return refuse
		}
	}
	pointer, err := sandboxGitRead(m.dot)
	if err != nil || pointer != m.pointer {
		return refuse
	}
	backlink, err := sandboxGitRead(m.backlinkPath)
	if err != nil {
		return refuse
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(filepath.Dir(m.backlinkPath), backlink)
	}
	backlink, err = filepath.EvalSymlinks(backlink)
	if err != nil || backlink != m.backlink {
		return refuse
	}
	return nil
}
