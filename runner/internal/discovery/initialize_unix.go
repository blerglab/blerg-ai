//go:build linux || darwin

package discovery

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
	"golang.org/x/sys/unix"
)

// Initialization is launch-only. It never changes a running session's membership.
func (s *Service) initialize(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) (selectionRecord, error) {
	if req.Runtime != "host" || (req.RootIdentity != "" && req.RootIdentity != root.tag) {
		return selectionRecord{}, errInvalid
	}
	rel := req.Path
	if req.Folder != nil {
		rel = req.Folder.Path
	} else {
		// New and scratch launches create one leaf, never reuse a person's folder.
		if rel == "" || strings.ContainsAny(rel, "/\\") || rel == "." || rel == ".." || (strings.HasPrefix(rel, ".") && !scratch.Valid(rel)) {
			return selectionRecord{}, errInvalid
		}
		if _, err := cleanPath(rel); err != nil {
			return selectionRecord{}, err
		}
		if err := refuseRepositoryAncestors(root.file, op); err != nil {
			return selectionRecord{}, err
		}
		if err := unix.Mkdirat(int(root.file.Fd()), rel, 0700); err != nil {
			return selectionRecord{}, errInvalid
		}
	}
	rel, err := cleanPath(rel)
	if err != nil {
		return selectionRecord{}, err
	}
	dir, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return selectionRecord{}, err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil || repo != nil {
		return selectionRecord{}, errInvalid
	}
	if req.Folder != nil && entryFor(root, rel, info, repo).SourceIdentity != req.Folder.SourceIdentity {
		return selectionRecord{}, errStale
	}
	sources := []protocol.DiscoverySource{}
	if err := collectRepositories(root, rel, op, &sources); err != nil || len(sources) > 0 {
		return selectionRecord{}, errInvalid
	}
	// No nested repository, including an ancestor above the configured root.
	options := hostgit.Options{Dir: filepath.Join(root.name, filepath.FromSlash(rel)), DirFile: dir, MemoryLimit: true}
	if runtime.GOOS == "linux" {
		options.Dir = "/proc/self/fd/" + strconv.FormatUint(uint64(dir.Fd()), 10)
		options.ExtraFiles = []*os.File{dir}
	}
	if err := refuseRepositoryAncestors(dir, op); err != nil {
		return selectionRecord{}, err
	}
	// Reserve .git exclusively so init can never reinitialize existing metadata.
	if err := unix.Mkdirat(int(dir.Fd()), ".git", 0700); err != nil {
		return selectionRecord{}, errInvalid
	}
	if _, err := hostgit.Output(op.ctx, options, "init", "--template=", "--initial-branch=main"); err != nil {
		return selectionRecord{}, errUnavailable
	}
	options.Config = []string{"user.name=Blerg", "user.email=blerg@example.invalid", "commit.gpgSign=false"}
	if _, err := hostgit.Output(op.ctx, options, "commit", "--allow-empty", "-m", "Initial commit"); err != nil {
		return selectionRecord{}, errUnavailable
	}
	current, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return selectionRecord{}, err
	}
	defer func() { _ = current.Close() }()
	if repo == nil {
		return selectionRecord{}, errUnavailable
	}
	// Re-resolve the repository with fresh source identity after initialization.
	return s.resolve(root, protocol.DiscoverWorkspace{Operation: "resolve", Runtime: "host", RootIdentity: root.tag, Members: []protocol.SelectionRef{{Path: path.Clean(rel), SourceIdentity: repo.source.SourceIdentity}}}, op)
}

// Check metadata directly: Git can refuse to inspect an ancestor owned by another
// user, which must never turn into permission to initialize a nested worktree.
func refuseRepositoryAncestors(dir *os.File, op *operation) error {
	fd, err := unix.FcntlInt(dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return errUnavailable
	}
	defer func() { _ = unix.Close(fd) }()
	for range 1024 {
		if op.ctx.Err() != nil {
			return errBudget
		}
		var entry unix.Stat_t
		err = unix.Fstatat(fd, ".git", &entry, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			return errInvalid
		}
		if !errors.Is(err, unix.ENOENT) {
			return errUnavailable
		}
		var current unix.Stat_t
		if unix.Fstat(fd, &current) != nil {
			return errUnavailable
		}
		parent, err := unix.Openat(fd, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return errUnavailable
		}
		var above unix.Stat_t
		err = unix.Fstat(parent, &above)
		_ = unix.Close(fd)
		fd = parent
		if err != nil {
			return errUnavailable
		}
		if current.Dev == above.Dev && current.Ino == above.Ino {
			return nil
		}
	}
	return errBudget
}
