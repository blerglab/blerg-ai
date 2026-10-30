package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Allocations live in the signed workspace journal, including an intent written
// before Git creates anything. An interrupted creation is retained, never guessed.
type hostChildAllocation struct {
	MemberID    string                 `json:"member_id"`
	Source      WorkspaceSource        `json:"source"`
	Path        string                 `json:"path"`
	Root        protocol.WorkspaceRoot `json:"root"`
	GitPath     string                 `json:"git_path"`
	GitIdentity protocol.WorkspaceRoot `json:"git_identity"`
	Base        string                 `json:"base"`
	Released    bool                   `json:"released"`
}

func hostChildGit(ctx context.Context, path string, root protocol.WorkspaceRoot, args ...string) (string, error) {
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

func (w *LocalWorkspace) allocateHostChild(ctx context.Context, parent workspacePrivateManifest) (*discovery.BoundPins, error) {
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
	w.state.HostChild = true
	w.state.Root = root
	identity, err := workspaceDirectoryIdentity(root)
	if err != nil {
		return nil, err
	}
	w.state.RootIdentity, w.state.RuntimeRoot = identity, identity
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
			return nil, fmt.Errorf("host child member %s requires a Git repository with a commit", member.ID)
		}
		filters, filterErr := hostChildGit(ctx, source.Path, source.RootIdentity, "config", "--get-regexp", `^filter\.`)
		if filters != "" || filterErr != nil && !hostChildExitOne(filterErr) {
			return nil, errors.New("host child worktree checkout with configured Git filters is unsupported")
		}
		actual, e := hostChildGit(ctx, source.Path, source.RootIdentity, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil || actual != source.CommonPath {
			return nil, errors.New("parent repository common directory changed")
		}
		branch, branchErr := hostChildGit(ctx, source.Path, source.RootIdentity, "rev-parse", "--verify", "--quiet", "refs/heads/"+sandboxBranch(w.state.Session))
		if branchErr == nil || branch != "" {
			return nil, fmt.Errorf("host child branch %s already exists for member %s; refusing to reuse it", sandboxBranch(w.state.Session), member.ID)
		}
		if !hostChildExitOne(branchErr) {
			return nil, errors.New("host child branch availability could not be verified")
		}

		target := root
		if member.Path != "." {
			target = filepath.Join(root, filepath.FromSlash(member.Path))
		}
		// A single '.' member needs Git to create the root itself. Retain the
		// allocation directory as the namespace and use a separate repository leaf.
		if target == root {
			target = filepath.Join(root, "repo")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		allocation := hostChildAllocation{MemberID: member.ID, Source: *source, Path: target, Base: base}
		w.state.HostChildren = append(w.state.HostChildren, allocation)
		if err := w.save(); err != nil {
			return nil, err
		}
		if _, err := hostChildGit(ctx, source.Path, source.RootIdentity, "worktree", "add", "-b", sandboxBranch(w.state.Session), "--", target, base); err != nil {
			return nil, fmt.Errorf("create host child member %s worktree: %w", member.ID, err)
		}
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
	if len(pins.Sources) == 1 && pins.Sources[0].Member.Path == "." {
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
			if err != nil || branch != sandboxBranch(w.state.Session) {
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
			if err != nil || branch != sandboxBranch(w.state.Session) {
				result.SyncError = "host child worktree branch changed"
				break
			}
			head, err := hostChildGit(ctx, a.Path, a.Root, "rev-parse", "--verify", "HEAD")
			if err != nil || !protocol.ValidSessionOriginCommit(head) {
				result.SyncError = "host child commit unavailable"
				break
			}
			result.Branch, result.Head = branch, head
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
