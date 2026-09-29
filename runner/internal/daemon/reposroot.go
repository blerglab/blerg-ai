package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The daemon's repos root — where session checkouts live — starts from
// BLERG_RUNNER_REPOS_ROOT, but the owner can change it from the app
// (set_repos_root). A change is saved to a small settings file in the daemon's
// state directory so it survives a restart; on boot a saved value overrides
// the environment variable, which is then only the first-run default.
//
// The state directory is deliberately NOT inside the repos root: the file
// that says where the root is cannot live in the thing it relocates.

// DaemonStateDirEnv overrides where the daemon keeps its own state.
const DaemonStateDirEnv = "BLERG_RUNNER_DAEMON_STATE_DIR"

// daemonSettingsFile is the settings file's name inside the state directory.
const daemonSettingsFile = "settings.json"

// daemonSettings is the on-disk shape of the settings file. Only fields a
// person changed from the app appear; an absent field means "use the env".
type daemonSettings struct {
	ReposRoot string `json:"repos_root,omitempty"`
}

// DaemonStateDir returns where the daemon keeps its own state:
// $BLERG_RUNNER_DAEMON_STATE_DIR when set, else ~/.blerg-runner-daemon.
// "" when neither can be determined (no HOME) — the setting then cannot be
// saved, and changing it from the app is refused rather than applied until
// the next restart silently undoes it.
func DaemonStateDir() string {
	if v := os.Getenv(DaemonStateDirEnv); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".blerg-runner-daemon")
}

// DaemonSettingsPath is the settings file inside stateDir ("" for "").
func DaemonSettingsPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, daemonSettingsFile)
}

// ReposRootSetting is the daemon's live repos root, shared by everything that
// reads it (spawns, the repo listing in hello/heartbeat, recovery records).
// Safe for concurrent use.
type ReposRootSetting struct {
	mu        sync.RWMutex
	path      string
	stateFile string // "" = cannot persist, so Set refuses
}

// NewReposRootSetting returns a setting holding root, persisted to stateFile
// on change ("" = not persistable; Set then refuses).
func NewReposRootSetting(root, stateFile string) *ReposRootSetting {
	return &ReposRootSetting{path: root, stateFile: stateFile}
}

// LoadReposRootSetting builds the boot-time setting. A repos root saved in
// stateFile wins over envRoot; a missing file is the normal first-run case
// and falls back silently; an unreadable, corrupt or unusable saved value
// falls back to envRoot with a logged warning — a bad settings file must
// never stop the daemon from starting. source is "settings" or "env".
func LoadReposRootSetting(stateFile, envRoot string) (setting *ReposRootSetting, source string) {
	saved, err := readSavedReposRoot(stateFile)
	switch {
	case err != nil:
		log.Printf("daemon: ignoring saved repos root (%v); using BLERG_RUNNER_REPOS_ROOT", err)
	case saved != "":
		if info, statErr := os.Stat(saved); statErr != nil || !info.IsDir() {
			// Kept anyway: it was valid when chosen, and silently moving new
			// sessions somewhere else (an unplugged drive coming back later)
			// would be worse than a spawn that says the folder is missing.
			log.Printf("daemon: saved repos root %s is not currently a directory", saved)
		}
		return NewReposRootSetting(saved, stateFile), "settings"
	}
	return NewReposRootSetting(envRoot, stateFile), "env"
}

// readSavedReposRoot returns the repos root saved in stateFile, "" when there
// is none (no file, or no value in it), or an error when the file exists but
// cannot be used.
func readSavedReposRoot(stateFile string) (string, error) {
	if stateFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(stateFile) //nolint:gosec // stateFile is the daemon's own settings path from its configuration
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", stateFile, err)
	}
	var s daemonSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("parse %s: %w", stateFile, err)
	}
	if s.ReposRoot == "" {
		return "", nil
	}
	if problem := reposRootShapeProblem(s.ReposRoot); problem != "" {
		return "", fmt.Errorf("%s: saved repos root %q: %s", stateFile, s.ReposRoot, problem)
	}
	return filepath.Clean(s.ReposRoot), nil
}

// Get returns the current repos root.
func (s *ReposRootSetting) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// WithRoot runs fn with the current root, holding off any change until fn
// returns. Session-record reads and writes go through it: a record written,
// deleted or listed under a root a concurrent Set is moving away from would
// otherwise be lost — or, for a delete, resurrected by the move. fn must not
// call Get, WithRoot or Set.
func (s *ReposRootSetting) WithRoot(fn func(root string)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.path)
}

// Set validates requested (see PrepareReposRoot — it may create the
// directory), saves it, and only then makes it the live root. On any error
// nothing changes, in memory or on disk. Returns the root now in effect.
//
// When the root actually changes, onChange(old, new) runs before Set
// returns and still under the setting's lock, so nothing reading or writing
// through WithRoot — nor a second Set — can interleave with it. It must not
// call Get, WithRoot or Set.
func (s *ReposRootSetting) Set(requested string, onChange func(old, next string)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateFile == "" {
		return s.path, errors.New("this daemon has no state directory to save the setting in (set " + DaemonStateDirEnv + ")")
	}
	root, err := PrepareReposRoot(requested)
	if err != nil {
		return s.path, err
	}
	if err := saveReposRoot(s.stateFile, root); err != nil {
		return s.path, fmt.Errorf("could not save the setting: %w", err)
	}
	old := s.path
	s.path = root
	if onChange != nil && old != root {
		onChange(old, root)
	}
	return root, nil
}

// saveReposRoot writes root into stateFile atomically (temp file + rename),
// keeping any other settings already there. The directory is created 0700 and
// the file 0600: it is the daemon's own state, not something for other users.
func saveReposRoot(stateFile, root string) error {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		return err
	}
	var s daemonSettings
	if data, err := os.ReadFile(stateFile); err == nil { //nolint:gosec // stateFile is the daemon's own settings path from its configuration
		// A corrupt existing file is simply replaced.
		_ = json.Unmarshal(data, &s)
	}
	s.ReposRoot = root
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(stateFile), "."+daemonSettingsFile+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, stateFile)
}

// forbiddenReposRoots are roots no one means: the filesystem root (every
// directory on the machine would become a "repo"), and the kernel's pseudo
// filesystems. Anything under the latter is refused too.
var forbiddenReposRoots = []string{"/", "/proc", "/sys", "/dev"}

// reposRootShapeProblem checks what can be checked without touching the
// filesystem: non-empty, absolute, no control characters, not a forbidden
// root. Returns "" when the shape is fine.
func reposRootShapeProblem(p string) string {
	if p == "" {
		return "a folder is required"
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return "the path contains control characters"
	}
	if !filepath.IsAbs(p) {
		return "the path must be absolute (start with /)"
	}
	if forbiddenReposRoot(filepath.Clean(p)) {
		return "that folder can't be the repos folder"
	}
	return ""
}

func forbiddenReposRoot(clean string) bool {
	for _, f := range forbiddenReposRoots {
		if clean == f || (f != "/" && strings.HasPrefix(clean, f+"/")) {
			return true
		}
	}
	return false
}

// PrepareReposRoot validates requested as a new repos root and makes sure it
// is usable, returning the cleaned path to store: absolute, not "/" or a
// pseudo filesystem (also after following symlinks — a link to "/" is "/"),
// an existing directory or one it could create (mkdir -p, 0750), and
// writable by the daemon's user (probed with a temp file). A symlink to a
// directory is accepted and stored as given: resolveProjectPath resolves the
// root itself before its containment check, so a symlinked root is contained
// correctly. The error text is shown to the person who asked, so it says
// what is wrong in plain words.
func PrepareReposRoot(requested string) (string, error) {
	if problem := reposRootShapeProblem(requested); problem != "" {
		return "", errors.New(problem)
	}
	root := filepath.Clean(requested)

	if info, err := os.Lstat(root); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if _, err := os.Stat(root); err != nil {
			return "", fmt.Errorf("%s is a symbolic link to something that doesn't exist", root)
		}
	}
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", root)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", fmt.Errorf("can't create %s: %s", root, plainFSError(err))
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("can't read %s: %s", root, plainFSError(err))
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", root)
	}
	if problem := sharedFolderProblem(info); problem != "" {
		return "", fmt.Errorf("%s %s", root, problem)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("can't resolve %s: %s", root, plainFSError(err))
	}
	if forbiddenReposRoot(resolved) {
		return "", fmt.Errorf("%s leads to %s, which can't be the repos folder", root, resolved)
	}
	probe, err := os.CreateTemp(root, ".blerg-runner-write-check-*")
	if err != nil {
		return "", fmt.Errorf("the daemon can't write to %s: %s", root, plainFSError(err))
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	// The daemon keeps session records in <root>/.blerg-runner. One that is
	// already there must be private to this user; anyone else's is refused
	// rather than adopted.
	if _, err := secureRecordsDir(root, false); err != nil {
		return "", fmt.Errorf("can't use %s: %w", root, err)
	}
	return root, nil
}

// sharedFolderProblem refuses a root other users can tamper with: one that
// isn't the daemon user's own yet is group- or world-writable (a shared
// scratch folder like /tmp), or one of its own that anyone can write into
// without the sticky bit (anyone could swap its .blerg-runner directory).
// A group-writable folder of its own is fine — the usual user-private-group
// setup. Returns "" when the folder is acceptable.
func sharedFolderProblem(info fs.FileInfo) string {
	perm := info.Mode().Perm()
	owned := ownedByDaemon(info)
	switch {
	case !owned && perm&0o022 != 0:
		return "belongs to another user and others can write to it — pick a folder of your own"
	case owned && perm&0o002 != 0 && info.Mode()&fs.ModeSticky == 0:
		return "can be written by any user — pick a folder only you can write to (or chmod o-w it)"
	}
	return ""
}

// plainFSError turns the common filesystem errors into a short reason
// without repeating the path (callers already name it).
func plainFSError(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrExist):
		return "a file with that name is in the way"
	case errors.Is(err, fs.ErrNotExist):
		return "it doesn't exist"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
