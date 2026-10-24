package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// All v2 Git consumers enter the registered reader/lifecycle boundary, including
// suspended or retired readers. A missing registration cannot downgrade to cwd.
type workspaceGitReader interface {
	readGit(context.Context, protocol.ReadCommit, bool) protocol.CommitResult
}

func (r *WorkspaceExplorerRouter) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) (protocol.CommitResult, bool) {
	failed := protocol.CommitResult{ErrorCode: "unavailable"}
	r.mu.Lock()
	registration := r.runtimes[req.SessionID]
	v2 := r.gitV2[req.SessionID] || !req.IsZero()
	if !v2 {
		r.mu.Unlock()
		return failed, false
	}
	if r.closed || registration == nil || registration.suspended || r.active >= 4 {
		r.mu.Unlock()
		return failed, true
	}
	reader, ok := registration.runtime.(workspaceGitReader)
	if !ok {
		r.mu.Unlock()
		return failed, true
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	key := "git:" + req.RequestID
	if r.pending == nil {
		r.pending = map[string]explorerWork{}
	}
	if _, exists := r.pending[key]; exists {
		r.mu.Unlock()
		cancel()
		return failed, true
	}
	r.pending[key] = explorerWork{session: req.SessionID, cancel: cancel, done: done}
	r.active++
	r.wait.Add(1)
	r.mu.Unlock()
	defer r.wait.Done()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.pending, key)
		r.active--
		close(done)
		r.mu.Unlock()
	}()
	result := reader.readGit(ctx, req, origin)
	r.mu.Lock()
	current := r.runtimes[req.SessionID] == registration && !registration.suspended && !r.closed
	r.mu.Unlock()
	if !current || ctx.Err() != nil {
		return failed, true
	}
	return result, true
}

func readGitBinding(ctx context.Context, req protocol.ReadCommit, origin bool, binding workspaceFileBinding) protocol.CommitResult {
	if origin {
		commit, ok := readWorkspaceOrigin(ctx, binding, nil)
		if !ok {
			return protocol.CommitResult{ErrorCode: "unavailable"}
		}
		return protocol.CommitResult{OK: true, Commit: &protocol.CommitInfo{SHA: commit}}
	}
	if req.MergedChild != "" {
		if !req.IsZero() {
			if req.ChildLaunchGeneration == "" || !protocol.ValidSessionOriginCommit(req.ChildBase) {
				return protocol.CommitResult{ErrorCode: "invalid"}
			}
			return readChildMergedBase(ctx, req.SessionID, binding, req.MergedChild, req.ChildBase, "")
		}
		return readChildMerged(ctx, req.SessionID, binding, req.MergedChild)
	}
	return readWorkspaceCommit(ctx, req.SessionID, binding, req.SHA, req.Patch)
}

func (e *LocalExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	failed := protocol.CommitResult{ErrorCode: "unavailable"}
	if req.Validate(e.manifest, e.generation) != nil || !e.valid() {
		return failed
	}
	// Only pinned launch members participate. Discovery and request paths cannot
	// create a Git context. Runtime clones with alternates use P2's source adapter.
	var member protocol.WorkspaceMember
	for _, m := range e.manifest.Members {
		if m.ID == req.MemberID {
			member = m
			break
		}
	}
	if member.ID == "" || !e.memberAvailable(member.Path) {
		return failed
	}
	anchor := e.anchors[member.ID]
	if anchor.info == nil {
		return failed
	}
	live := func() bool {
		if !e.valid() || !e.memberAvailable(member.Path) {
			return false
		}
		st, err := anchor.root.Lstat(".git")
		if err != nil || workspaceRootIdentity(st) != e.gitRoots[member.ID] {
			return false
		}
		if worktree, ok := e.worktrees[member.ID]; ok {
			if !worktree.publicMatches(anchor.root, ".") {
				return false
			}
			_, safe := worktree.check(ctx)
			return safe
		}
		return true
	}
	binding := workspaceFileBinding{root: anchor.folder, info: anchor.info, live: live}
	result := readGitBinding(ctx, req, origin, binding)
	if !live() {
		return failed
	}
	return result
}
func (v *VirtualHostExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	if req.Validate(v.manifest, v.namespace.generation) != nil || !v.namespace.valid() {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	member := v.members[req.MemberID]
	if member == nil {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	return member.readGit(ctx, req, origin)
}
func (r *authorizedHostExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	failed := protocol.CommitResult{ErrorCode: "unavailable"}
	if req.SessionID != r.launch.SessionID || !req.Matches(&r.binding) || !r.current(ctx) {
		return failed
	}
	reader, ok := r.reader.(workspaceGitReader)
	if !ok {
		return failed
	}
	result := reader.readGit(ctx, req, origin)
	if !r.current(ctx) {
		return failed
	}
	return result
}
func (r *boundWorkspaceExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	failed := protocol.CommitResult{ErrorCode: "unavailable"}
	result := failed
	w := r.workspace
	err := w.WithBoundRuntimeMember(ctx, req.WorkspaceRuntimeRef, req.Member(), func(ctx context.Context, runtime WorkspaceContainer, path string) error {
		if w.state.StopRequested {
			return errors.New("workspace stopping")
		}
		if origin {
			head, err := runtime.Head(ctx, path)
			if err != nil {
				return err
			}
			if head != "" && !protocol.ValidSessionOriginCommit(head) {
				return errors.New("invalid head")
			}
			result = protocol.CommitResult{OK: true, Commit: &protocol.CommitInfo{SHA: head}}
			return nil
		}
		// Synced commits are read only from this member's trusted source, never
		// by host Git against a clone written by the container.
		for _, member := range w.state.Members {
			if member.Member.ID != req.MemberID {
				continue
			}
			source := member.Source.Repo
			info, err := os.Lstat(source)
			if err != nil || workspaceRootIdentity(info) != member.SourceIdentity {
				return errors.New("source changed")
			}
			binding := workspaceFileBinding{root: filepath.Clean(source), info: info, live: func() bool {
				st, err := os.Lstat(source)
				return ctx.Err() == nil && err == nil && workspaceRootIdentity(st) == member.SourceIdentity
			}}
			if req.MergedChild != "" {
				if req.ChildLaunchGeneration == "" || !protocol.ValidSessionOriginCommit(req.ChildBase) {
					return errors.New("child provenance missing")
				}
				result = readChildMergedBase(ctx, req.SessionID, binding, req.MergedChild, req.ChildBase, "refs/heads/wip/"+req.SessionID)
			} else {
				result = readGitBinding(ctx, req, false, binding)
			}
			return nil
		}
		return errors.New("member missing")
	})
	if err != nil {
		return failed
	}
	return result
}

func readLegacyGit(ctx context.Context, req protocol.ReadCommit, origin bool, binding workspaceFileBinding) protocol.CommitResult {
	if !req.IsZero() {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	if origin && isSandboxClone(binding.root, req.SessionID) {
		commit, ok := readSandboxOrigin(binding)
		return protocol.CommitResult{OK: ok, Commit: &protocol.CommitInfo{SHA: commit}}
	}
	return readGitBinding(ctx, req, origin, binding)
}
