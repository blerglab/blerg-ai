package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

// A repository can invent filter.<arbitrary-name>.process/clean drivers. Fixed
// -c overrides cannot enumerate those names safely under concurrent writes.
// Status therefore uses a private metadata snapshot whose configuration is
// entirely ours. The trusted runtime must protect its private temp directory
// from agent writes, just as it protects the launcher configuration and pipes.
type workspaceStatusSnapshot struct {
	directory   string
	objects     *os.File
	ledger      *workspaceSessionLedger
	reservation sessionGitUsage
}

func (s *workspaceStatusSnapshot) close() {
	if s.objects != nil {
		_ = s.objects.Close()
	}
	if os.RemoveAll(s.directory) == nil && s.ledger != nil {
		s.ledger.release(s.reservation)
		s.reservation = sessionGitUsage{}
	}
}
func (e *LocalExplorer) statusSnapshot(ctx context.Context, name string, worktree hostWorktreePin, isWorktree bool) (*workspaceStatusSnapshot, error) {
	return e.statusSnapshotForPlatform(ctx, name, worktree, isWorktree, runtime.GOOS)
}

// The Darwin copy path also runs on Linux with real Git in regression tests.
func (e *LocalExplorer) statusSnapshotForPlatform(ctx context.Context, name string, worktree hostWorktreePin, isWorktree bool, platform string) (_ *workspaceStatusSnapshot, resultErr error) {
	return e.statusSnapshotUsing(ctx, name, worktree, isWorktree, platform, false)
}

// Ignore readers need only HEAD, index, and private config, never refs or objects.
func (e *LocalExplorer) statusSnapshotUsing(ctx context.Context, name string, worktree hostWorktreePin, isWorktree bool, platform string, ignoreOnly bool) (_ *workspaceStatusSnapshot, resultErr error) {
	stage := "reserve private metadata"
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("%s: %w", stage, resultErr)
		}
	}()
	snapshot := &workspaceStatusSnapshot{}
	if e.manifest.Runtime != "host" {
		e.mu.Lock()
		if e.ledger == nil {
			e.ledger = &workspaceSessionLedger{measure: e.measureSessionGit}
		}
		e.mu.Unlock()
		snapshot.ledger = e.ledger
	}
	reserve := func(bytes int64, entries int) error {
		if snapshot.ledger == nil {
			return nil
		}
		usage := sessionGitUsage{metadata: bytes, entries: entries}
		if err := snapshot.ledger.reserve(ctx, usage); err != nil {
			return err
		}
		snapshot.reservation.metadata += bytes
		snapshot.reservation.entries += entries
		return nil
	}
	if err := reserve(0, 1); err != nil {
		return nil, err
	}
	stage = "create private directory"
	directory, err := os.MkdirTemp("", "blerg-workspace-status-")
	if err != nil {
		if snapshot.ledger != nil {
			snapshot.ledger.release(snapshot.reservation)
		}
		return nil, err
	}
	snapshot.directory = directory
	stage = "check private directory placement"
	// Refuse a misconfigured runtime whose private temp directory is public.
	relative, err := filepath.Rel(e.folder, directory)
	if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		snapshot.close()
		return nil, os.ErrPermission
	}
	success := false
	defer func() {
		if !success {
			snapshot.close()
		}
	}()
	stage = "select metadata roots"
	primary, primaryDir, _ := e.anchorFor(path.Join(name, ".git"))
	if primary == nil {
		return nil, os.ErrPermission
	}
	common := primary
	commonDir := primaryDir
	if isWorktree {
		primary = worktree.git
		primaryDir = "."
		common = worktree.common
		commonDir = "."
	}
	if !ignoreOnly {
		stage = "open objects"
		objects, err := workspaceOpenDirectory(common, path.Join(commonDir, "objects"))
		if err != nil {
			return nil, err
		}
		snapshot.objects = objects
		objectInfo, err := objects.Stat()
		if err != nil || !objectInfo.IsDir() {
			return nil, os.ErrPermission
		}
	}
	budget := &metadataBudget{entries: 200000, reserve: reserve}
	copyFile := func(root *os.Root, source, destination string, required bool) error {
		stage = "copy " + destination
		return copyStatusMetadataFile(ctx, root, source, filepath.Join(directory, destination), required, budget)
	}
	if err := copyFile(primary, path.Join(primaryDir, "HEAD"), "HEAD", true); err != nil {
		return nil, err
	}
	if err := copyFile(primary, path.Join(primaryDir, "index"), "index", false); err != nil {
		return nil, err
	}
	if !ignoreOnly {
		for _, file := range []string{"packed-refs", "shallow"} {
			if err := copyFile(common, path.Join(commonDir, file), file, false); err != nil {
				return nil, err
			}
		}
		stage = "copy refs"
		if err := copyStatusRefs(ctx, common, path.Join(commonDir, "refs"), filepath.Join(directory, "refs"), budget, 0); err != nil {
			return nil, err
		}
	}
	if ignoreOnly {
		if err := reserve(0, 1); err != nil {
			return nil, err
		}
		if err := os.Mkdir(filepath.Join(directory, "refs"), 0700); err != nil {
			return nil, err
		}
	}
	stage = "read object format"
	format := "sha1"
	configFile, err := workspaceOpenFile(common, path.Join(commonDir, "config"))
	if err != nil {
		return nil, err
	}
	configInfo, err := configFile.Stat()
	if err != nil || !workspaceRegularFile(configInfo) {
		_ = configFile.Close()
		return nil, os.ErrPermission
	}
	raw, err := io.ReadAll(io.LimitReader(configFile, 65537))
	_ = configFile.Close()
	if err != nil || len(raw) > 65536 {
		return nil, os.ErrPermission
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "objectformat") {
			format = strings.TrimSpace(value)
		}
	}
	config := "[core]\nrepositoryformatversion=0\nbare=false\n"
	switch format {
	case "sha1":
	case "sha256":
		config = "[core]\nrepositoryformatversion=1\nbare=false\n[extensions]\nobjectformat=sha256\n"
	default:
		return nil, errors.New("unsupported object format")
	}
	stage = "write private config"
	if err := reserve(int64(len(config)), 2); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(directory, "config"), []byte(config), 0600); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(directory, "objects"), 0700); err != nil {
		return nil, err
	}
	if platform == "darwin" && !ignoreOnly {
		// fdescfs has no directory-relative descendants. Copy only the bounded,
		// descriptor-vetted object store into the private snapshot; use pinned cwd
		// for worktree access, without any /dev/fd/N/child path.
		stage = "copy objects"
		if err := os.Remove(filepath.Join(directory, "objects")); err != nil {
			return nil, err
		}
		if err := copyStatusRefs(ctx, common, path.Join(commonDir, "objects"), filepath.Join(directory, "objects"), budget, 0); err != nil {
			return nil, err
		}
	}
	stage = "final metadata reservation"
	if err := reserve(0, 0); err != nil {
		return nil, err
	}
	success = true
	return snapshot, nil
}
func copyStatusMetadataFile(ctx context.Context, root *os.Root, source, destination string, required bool, budget *metadataBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := workspaceOpenFile(root, source)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || !workspaceRegularFile(before) {
		return os.ErrPermission
	}
	budget.entries--
	budget.bytes += before.Size()
	if budget.entries < 0 || budget.bytes > 256<<20 {
		return errors.New("metadata budget exceeded")
	}
	if budget.reserve != nil {
		if err := budget.reserve(before.Size(), 1); err != nil {
			return err
		}
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // G304: private mode-0700 allocation; refs add only single ReadDir components; O_EXCL refuses existing files.
	if err != nil {
		return err
	}
	count, copyErr := io.Copy(output, io.LimitReader(file, before.Size()+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	after, err := file.Stat()
	if err != nil || count != before.Size() || workspaceMetadataFingerprint(before) != workspaceMetadataFingerprint(after) {
		return errExplorerStale
	}
	return ctx.Err()
}
func copyStatusRefs(ctx context.Context, root *os.Root, source, destination string, budget *metadataBudget, depth int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if depth > 32 {
		return errors.New("metadata depth exceeded")
	}
	directory, err := workspaceOpenDirectory(root, source)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return os.ErrPermission
	}
	if budget.reserve != nil {
		if err := budget.reserve(0, 1); err != nil {
			return err
		}
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			budget.entries--
			if budget.entries < 0 {
				return errors.New("metadata budget exceeded")
			}
			child := path.Join(source, entry.Name())
			target := filepath.Join(destination, entry.Name())
			file, err := workspaceOpenFile(root, child)
			if err != nil {
				return err
			}
			st, err := file.Stat()
			_ = file.Close()
			if err != nil {
				return err
			}
			if st.IsDir() {
				if err := copyStatusRefs(ctx, root, child, target, budget, depth+1); err != nil {
					return err
				}
			} else {
				if err := copyStatusMetadataFile(ctx, root, child, target, true, budget); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			break
		}
	}
	return ctx.Err()
}
func (s *workspaceStatusSnapshot) options(p *pinnedGitDir, out io.Writer) hostgit.Options {
	return s.optionsForPlatform(p, out, runtime.GOOS)
}
func (s *workspaceStatusSnapshot) optionsForPlatform(p *pinnedGitDir, out io.Writer, platform string) hostgit.Options {
	worktree := "/proc/self/fd/3"
	objects := "/proc/self/fd/4"
	if platform == "darwin" {
		worktree = "."
		objects = filepath.Join(s.directory, "objects")
	}
	return hostgit.Options{Dir: p.cwd, DirFile: p.dir, ExtraFiles: []*os.File{p.dir, s.objects}, SafeDirectory: p.resolved, Ceiling: filepath.Dir(p.resolved), MemoryLimit: true, LimitKB: 256 << 10, Untrusted: true, Stdout: out,
		Env:    []string{"GIT_DIR=" + s.directory, "GIT_COMMON_DIR=" + s.directory, "GIT_WORK_TREE=" + worktree, "GIT_OBJECT_DIRECTORY=" + objects},
		Config: []string{"core.bare=false", "core.fsmonitor=false", "core.hooksPath=/dev/null", "core.untrackedCache=false", "index.threads=1", "status.showUntrackedFiles=normal", "status.aheadBehind=false"},
	}
}
