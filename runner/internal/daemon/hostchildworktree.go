package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Allocations live in the signed workspace journal, including an intent written
// before Git creates anything. Interrupted creations are retained; synchronous
// failures roll back only resources whose ownership this attempt established.
type hostChildAllocation struct {
	MemberID      string                 `json:"member_id"`
	Source        WorkspaceSource        `json:"source"`
	Path          string                 `json:"path"`
	Root          protocol.WorkspaceRoot `json:"root"`
	GitPath       string                 `json:"git_path"`
	GitIdentity   protocol.WorkspaceRoot `json:"git_identity"`
	Base          string                 `json:"base"`
	BranchCreated bool                   `json:"branch_created,omitempty"`
	Released      bool                   `json:"released"`
}

type hostChildGitFailure struct {
	reason string
	cause  error
}

func (e *hostChildGitFailure) Error() string { return e.reason + ": " + failurelog.Cause(e.cause) }
func (e *hostChildGitFailure) Unwrap() error { return e.cause }

func hostChildGit(ctx context.Context, path string, root protocol.WorkspaceRoot, args ...string) (output string, resultErr error) {
	stderr := &cappedOutput{max: 4096}
	defer func() {
		if resultErr != nil {
			reason := hostChildGitReason(stderr.buf.String())
			if stderr.buf.Len() == 0 && hostChildExitOne(resultErr) {
				reason = "Git found no matching value or reference"
			}
			operation := "other"
			for _, allowed := range []string{"rev-parse", "config", "worktree", "update-ref", "symbolic-ref", "status"} {
				if len(args) > 0 && args[0] == allowed {
					operation = allowed
					break
				}
			}
			failurelog.Default.Event("host_child_git:"+operation+":"+reason, slog.LevelError, "component", "daemon", "operation", operation, "cause", failurelog.Cause(resultErr), "git_stderr", reason)
			resultErr = &hostChildGitFailure{reason: reason, cause: resultErr}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, sandboxGitTimeout)
	defer cancel()
	identity, err := workspaceDirectoryIdentity(path)
	if err != nil || identity != root {
		return "", errors.New("host child directory identity changed")
	}
	st, err := os.Stat(path)
	if err != nil || workspaceRootIdentity(st) != root {
		return "", errors.New("host child directory identity changed")
	}
	p, ok := pinGitDir(path, st)
	if !ok {
		return "", errors.New("host child directory unavailable")
	}
	defer p.close()
	p.stderr = stderr
	if !p.vet(ctx, true) {
		return "", errors.New("host child repository unavailable")
	}
	out := &cappedOutput{max: 1 << 20, strict: true}
	err = p.git(ctx, out, args...)
	if out.over {
		return "", errors.New("host child git output exceeded limit")
	}
	return strings.TrimSpace(out.buf.String()), err
}

func (s *LocalWorkspaceService) hostChildParent(ctx context.Context, msg protocol.SpawnSession) (*workspacePrivateManifest, error) {
	if s.Resolve == nil {
		return nil, errors.New("trusted host child selection unavailable")
	}
	_, parentID, err := s.Resolve(ctx, msg)
	if err != nil || parentID == "" {
		return nil, err
	}
	if s.AuthorizeChild == nil || s.TrustedLaunch == nil {
		return nil, errors.New("host child authorization unavailable")
	}
	if err := s.AuthorizeChild(ctx, parentID, msg.SessionID); err != nil {
		return nil, err
	}
	record, err := workspaceRecord(parentID)
	if err != nil {
		return nil, err
	}
	parent, err := readPrivateWorkspace(record)
	if err != nil {
		return nil, err
	}
	trusted, err := s.TrustedLaunch(ctx, parentID)
	if err != nil || !reflect.DeepEqual(trusted, parent.Launch) || !parent.Host || parent.StopRequested || parent.Stopped || parent.RuntimePreparing {
		return nil, errors.New("parent host workspace unavailable")
	}
	if err := protocol.ValidateWorkspaceChild(parent.Launch, *msg.Workspace); err != nil {
		return nil, err
	}
	return &parent, nil
}

func (w *LocalWorkspace) allocateHostChild(ctx context.Context, parent workspacePrivateManifest) (result *discovery.BoundPins, resultErr error) {
	defer func() {
		if resultErr != nil {
			logDaemonFailure("host child allocation", resultErr)
		}
	}()
	root := filepath.Join(DaemonStateDir(), "host-children", w.state.Session)
	if len(w.state.Launch.Members) == 0 {
		return nil, errors.New("host child requires at least one Git repository")
	}
	for _, source := range parent.Sources {
		rel, e := filepath.Rel(source.Path, root)
		if e != nil || rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("host child storage must be outside parent checkout")
		}
	}

	// No existing allocation may silently become a new launch.
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, err
	}
	// This attempt owns only the newly created namespace. Interrupted journals
	// still use conservative recovery; synchronous failures roll back here.
	namespace := root
	namespaceIdentity, identityErr := workspaceDirectoryIdentity(namespace)
	if identityErr != nil {
		logDaemonFailure("host child namespace identity", identityErr)
		return nil, identityErr
	}
	first := len(w.state.HostChildren)
	defer func() {
		if resultErr != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxGitTimeout)
			defer cancel()
			if err := w.rollbackHostChild(cleanupCtx, namespace, namespaceIdentity, first); err != nil {
				logDaemonFailure("host child allocation rollback", err)
				resultErr = fmt.Errorf("%w; host child cleanup incomplete: %w", resultErr, err)
			}
		}
	}()
	w.state.HostChild = true
	w.state.Root = root
	w.state.RootIdentity, w.state.RuntimeRoot = namespaceIdentity, namespaceIdentity
	var err error
	w.state.Sources = nil
	if err := w.save(); err != nil {
		return nil, err
	}
	pins := &discovery.BoundPins{Manifest: w.state.Launch, AnchorPath: root}
	success := false
	defer func() {
		if !success {
			pins.Close()
		}
	}()
	pins.Anchor, err = os.Open(root) // #nosec G304 -- session root is derived from validated workspace ID in private daemon storage.
	if err != nil {
		return nil, err
	}
	// A child's root member needs Git to create its own folder, so the allocation
	// stays the namespace and the repository takes a leaf. Reserved members nest
	// inside it, exactly as they do for the parent.
	root = hostChildRoot(root, w.state.Launch.Members)
	for _, member := range w.state.Launch.Members {
		var source *WorkspaceSource
		for i := range parent.Sources {
			if parent.Sources[i].Member.ID == member.ID {
				source = &parent.Sources[i]
				break
			}
		}
		if source == nil {
			return nil, errors.New("host child member outside parent workspace")
		}
		common, e := workspaceDirectoryIdentity(source.CommonPath)
		if e != nil || common != source.CommonIdentity {
			return nil, errors.New("parent repository identity changed")
		}
		base, e := hostChildGit(ctx, source.Path, source.RootIdentity, "rev-parse", "--verify", "HEAD^{commit}")
		if e != nil || !protocol.ValidSessionOriginCommit(base) {
			if e != nil {
				return nil, fmt.Errorf("host child member %s requires a Git commit: %w", member.ID, e)
			}
			return nil, fmt.Errorf("host child member %s requires a Git repository with a commit", member.ID)
		}
		filters, filterErr := hostChildGit(ctx, source.Path, source.RootIdentity, "config", "--get-regexp", `^filter\.`)
		if filters != "" || filterErr != nil && !hostChildExitOne(filterErr) {
			if filterErr != nil && !hostChildExitOne(filterErr) {
				return nil, fmt.Errorf("host child filter check: %w", filterErr)
			}
			return nil, errors.New("host child worktree checkout with configured Git filters is unsupported")
		}
		actual, e := hostChildGit(ctx, source.Path, source.RootIdentity, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil || actual != source.CommonPath {
			if e != nil {
				return nil, fmt.Errorf("parent repository common directory check: %w", e)
			}
			return nil, errors.New("parent repository common directory changed")
		}
		branch, branchErr := hostChildGit(ctx, source.Path, source.RootIdentity, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.branch())
		if branchErr == nil || branch != "" {
			return nil, fmt.Errorf("host child branch %s already exists for member %s; refusing to reuse it", w.branch(), member.ID)
		}
		if !hostChildExitOne(branchErr) {
			return nil, fmt.Errorf("host child branch availability could not be verified: %w", branchErr)
		}

		target := root
		if member.Path != "." {
			target = filepath.Join(root, filepath.FromSlash(member.Path))
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		if err := os.Mkdir(target, 0700); err != nil {
			return nil, err
		}
		targetIdentity, err := workspaceDirectoryIdentity(target)
		if err != nil {
			return nil, err
		}
		allocation := hostChildAllocation{MemberID: member.ID, Source: *source, Path: target, Root: targetIdentity, Base: base}
		w.state.HostChildren = append(w.state.HostChildren, allocation)
		if err := w.save(); err != nil {
			return nil, err
		}
		// Create the ref atomically before checkout, so rollback knows it owns it.
		if _, err := hostChildGit(ctx, source.Path, source.RootIdentity, "update-ref", "refs/heads/"+w.branch(), base, ""); err != nil {
			return nil, fmt.Errorf("create host child member %s branch: %w", member.ID, err)
		}
		w.state.HostChildren[len(w.state.HostChildren)-1].BranchCreated = true
		if err := w.save(); err != nil {
			return nil, err
		}
		if _, err := hostChildGit(ctx, source.Path, source.RootIdentity, "worktree", "add", "--", target, w.branch()); err != nil {
			return nil, fmt.Errorf("create host child member %s worktree: %w", member.ID, err)
		}
		allocation.BranchCreated = true
		allocation.Root, err = workspaceDirectoryIdentity(target)
		if err != nil {
			return nil, err
		}
		allocation.GitPath, err = hostChildGit(ctx, target, allocation.Root, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, err
		}
		allocation.GitIdentity, err = workspaceDirectoryIdentity(allocation.GitPath)
		if err != nil {
			return nil, err
		}
		w.state.HostChildren[len(w.state.HostChildren)-1] = allocation
		runtimeSource := WorkspaceSource{Member: member, Path: target, CommonPath: source.CommonPath, RootIdentity: allocation.Root, CommonIdentity: source.CommonIdentity}
		w.state.Sources = append(w.state.Sources, runtimeSource)
		if err := w.save(); err != nil {
			return nil, err
		}
		directory, e := os.Open(target) // #nosec G304 -- target is the newly created journalled worktree, beneath the private session root.
		if e != nil {
			return nil, e
		}
		commonDir, e := os.Open(source.CommonPath)
		if e != nil {
			_ = directory.Close()
			return nil, e
		}
		pins.Sources = append(pins.Sources, discovery.SourcePin{Member: member, CanonicalPath: target, CommonDirectoryPath: source.CommonPath, Directory: directory, CommonDirectory: commonDir})
	}
	if len(pins.Sources) == 0 {
		return nil, errors.New("host child requires at least one Git repository")
	}
	// Keep the repo layout physically accurate for file readers.
	if pins.Sources[0].Member.Path == "." {
		_ = pins.Anchor.Close()
		pins.AnchorPath = pins.Sources[0].CanonicalPath
		pins.Anchor, err = os.Open(pins.AnchorPath)
		if err != nil {
			return nil, err
		}
		w.state.Root, w.state.RootIdentity, w.state.RuntimeRoot = pins.AnchorPath, w.state.Sources[0].RootIdentity, w.state.Sources[0].RootIdentity
		if err := w.save(); err != nil {
			return nil, err
		}
	}
	success = true
	return pins, nil
}

func hostChildRoot(root string, members []protocol.WorkspaceMember) string {
	for _, member := range members {
		if member.Path == "." {
			return filepath.Join(root, "repo")
		}
	}
	return root
}

func (a hostChildAllocation) validate(ctx context.Context) error {
	if a.Root == (protocol.WorkspaceRoot{}) || a.GitIdentity == (protocol.WorkspaceRoot{}) {
		return errors.New("allocation creation unproven")
	}
	common, err := workspaceDirectoryIdentity(a.Source.CommonPath)
	if err != nil || common != a.Source.CommonIdentity {
		return errors.New("repository identity changed")
	}
	git, err := workspaceDirectoryIdentity(a.GitPath)
	if err != nil || git != a.GitIdentity {
		return errors.New("worktree metadata identity changed")
	}
	path, err := hostChildGit(ctx, a.Path, a.Root, "rev-parse", "--absolute-git-dir")
	if err != nil || path != a.GitPath {
		return errors.New("worktree metadata changed")
	}
	path, err = hostChildGit(ctx, a.Path, a.Root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || path != a.Source.CommonPath {
		return errors.New("worktree common directory changed")
	}
	return nil
}

func (w *LocalWorkspace) releaseHostChildren(ctx context.Context) error {
	for i := range w.state.HostChildren {
		a := &w.state.HostChildren[i]
		if a.Released {
			continue
		}
		reason := a.validate(ctx)
		if reason == nil {
			branch, err := hostChildGit(ctx, a.Path, a.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
			if err != nil || branch != w.branch() {
				reason = errors.New("worktree branch changed")
			}
		}
		if reason == nil {
			status, err := hostChildGit(ctx, a.Path, a.Root, "status", "--porcelain=v1", "--untracked-files=all")
			if err != nil || status != "" {
				reason = errors.New("worktree has uncommitted changes or untracked files")
			}
		}
		if reason == nil {
			_, reason = hostChildGit(ctx, a.Path, a.Root, "worktree", "remove", "--", a.Path)
		}
		for j := range w.state.Members {
			result := &w.state.Members[j].Result
			if result.MemberID != a.MemberID {
				continue
			}
			if reason != nil {
				result.RecoveryLocation = a.Path
				result.SyncError = "host child worktree retained: " + reason.Error()
			} else {
				a.Released = true
				result.RecoveryLocation = ""
			}
		}
		if err := w.save(); err != nil {
			return err
		}
	}
	return nil
}

func (w *LocalWorkspace) collectHostChildren(ctx context.Context) {
	for i := range w.state.Members {
		result := &w.state.Members[i].Result
		result.Pending, result.SyncError = true, "host child allocation unavailable"
		for _, a := range w.state.HostChildren {
			if a.MemberID != result.MemberID {
				continue
			}
			result.Base, result.RecoveryLocation = a.Base, a.Path
			if err := a.validate(ctx); err != nil {
				result.SyncError = err.Error()
				break
			}
			branch, err := hostChildGit(ctx, a.Path, a.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
			if err != nil || branch != w.branch() {
				result.SyncError = "host child worktree branch changed"
				break
			}
			head, err := hostChildGit(ctx, a.Path, a.Root, "rev-parse", "--verify", "HEAD")
			if err != nil || !protocol.ValidSessionOriginCommit(head) {
				result.SyncError = "host child commit unavailable"
				break
			}
			result.Branch, result.Head = branch, head
			if pin, ok := pinGitDir(a.Path, nil); ok {
				if workspaceRootIdentity(pin.info) == a.Root && pin.vet(ctx, true) {
					result.Summary = deliveredSummary(ctx, pin, result.Base, head)
				}
				pin.close()
			}
			result.Pending, result.SyncError, result.RecoveryLocation = false, "", ""
			break
		}
	}
}

// Install the exact journalled external metadata roots without broadening the
// normal explorer policy for user-planted .git pointers.
func (w *LocalWorkspace) bindHostChildExplorer(ctx context.Context, reader WorkspaceExplorer) error {
	if !w.state.HostChild {
		return nil
	}
	for _, a := range w.state.HostChildren {
		if err := a.validate(ctx); err != nil {
			return err
		}
		local, ok := reader.(*LocalExplorer)
		if !ok {
			return errors.New("host child explorer layout unavailable")
		}
		anchor, ok := local.anchors[a.MemberID]
		if !ok {
			return errors.New("host child explorer member unavailable")
		}
		var name string
		for _, m := range w.state.Launch.Members {
			if m.ID == a.MemberID {
				name = m.Path
				break
			}
		}
		pin, ok := local.pinHostWorktreeAllocation(name, anchor.info, &a)
		if !ok {
			return errors.New("host child explorer metadata unavailable")
		}
		public, err := local.openFile(filepath.ToSlash(filepath.Join(name, ".git")))
		if err != nil {
			_ = pin.git.Close()
			_ = pin.common.Close()
			return err
		}
		st, err := public.Stat()
		_ = public.Close()
		if err != nil {
			_ = pin.git.Close()
			_ = pin.common.Close()
			return err
		}
		local.worktrees[a.MemberID] = pin
		local.gitRoots[a.MemberID] = workspaceRootIdentity(st)
	}
	return nil
}

func hostChildExitOne(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

// Treat stderr as untrusted data: retain only known Git explanations. Quoted
// refs, paths, URLs, configuration values and arbitrary hook output never leave
// this boundary, even when they contain credentials or control characters.
func hostChildGitReason(stderr string) string {
	for _, item := range []struct{ match, reason string }{
		{"No space left", "Git disk space exhausted"},
		{"Permission denied", "Git permission denied"},
		{"File exists", "Git lock file already exists"},
		{"Unable to create", "Git could not create a lock or file"},
		{"cannot lock ref", "Git could not lock the branch ref"},
		{"already exists", "Git target or branch already exists"},
		{"already checked out", "Git branch is already checked out"},
		{"not empty", "Git target directory is not empty"},
		{"Cannot allocate memory", "Git memory allocation failed"},
		{"not a git repository", "Git repository unavailable"},
		{"unable to read config", "Git configuration unreadable"},
		{"invalid config", "Git configuration invalid"},
		{"unable to checkout", "Git checkout failed"},
		{"not a valid", "Git reference invalid"},
	} {
		if strings.Contains(stderr, item.match) {
			return item.reason
		}
	}
	if stderr != "" {
		return "Git failed (unrecognized diagnostic redacted)"
	}
	return "Git failed without stderr"
}

func (w *LocalWorkspace) rollbackHostChild(ctx context.Context, namespace string, expected protocol.WorkspaceRoot, first int) error {
	identity, err := workspaceDirectoryIdentity(namespace)
	if err != nil || identity != expected {
		return errors.New("rollback namespace identity changed")
	}
	for i := len(w.state.HostChildren) - 1; i >= first; i-- {
		a := w.state.HostChildren[i]
		identity, err := workspaceDirectoryIdentity(a.Path)
		if err == nil {
			if identity != a.Root {
				return errors.New("rollback target identity changed")
			}
			if _, err := os.Lstat(filepath.Join(a.Path, ".git")); err == nil {
				// A synchronous checkout may have succeeded before Git returned an
				// error. Establish its registered metadata before deleting anything.
				a.GitPath, err = hostChildGit(ctx, a.Path, a.Root, "rev-parse", "--absolute-git-dir")
				if err != nil {
					return err
				}
				a.GitIdentity, err = workspaceDirectoryIdentity(a.GitPath)
				if err != nil {
					return err
				}
				if err := a.validate(ctx); err != nil {
					return err
				}
				branch, err := hostChildGit(ctx, a.Path, a.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
				if err != nil || branch != w.branch() {
					return errors.New("rollback target branch changed")
				}
				head, err := hostChildGit(ctx, a.Path, a.Root, "rev-parse", "HEAD")
				if err != nil || head != a.Base {
					return errors.New("rollback target commit changed")
				}
				status, err := hostChildGit(ctx, a.Path, a.Root, "status", "--porcelain=v1", "--untracked-files=all")
				if err != nil || status != "" {
					return errors.New("rollback target has changes")
				}
				if _, err := hostChildGit(ctx, a.Source.Path, a.Source.RootIdentity, "worktree", "remove", "--", a.Path); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			} else if err := os.Remove(a.Path); err != nil {
				return errors.New("rollback target is not empty or unavailable")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if a.BranchCreated {
			if _, err := hostChildGit(ctx, a.Source.Path, a.Source.RootIdentity, "update-ref", "-d", "refs/heads/"+w.branch(), a.Base); err != nil {
				return err
			}
		}
		w.state.HostChildren = w.state.HostChildren[:i]
	}
	// Remove empty namespace directories only, never recursively erase content.
	var directories []string
	if err := filepath.WalkDir(namespace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return errors.New("rollback namespace contains unexpected content")
		}
		directories = append(directories, path)
		return nil
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Remove(directories[i]); err != nil {
			return err
		}
	}
	w.state.Sources = nil
	w.state.Root = ""
	w.state.RootIdentity, w.state.RuntimeRoot = protocol.WorkspaceRoot{}, protocol.WorkspaceRoot{}
	return w.save()
}
