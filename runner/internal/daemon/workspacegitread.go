package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Refusal names are fixed at the checks below, never supplied by a peer.
func gitReadRefusal(check string) protocol.CommitResult {
	logDaemonFailure("git read: "+check, nil)
	return protocol.CommitResult{ErrorCode: "unavailable"}
}

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
	if r.closed {
		r.mu.Unlock()
		return gitReadRefusal("router_closed"), true
	}
	if registration == nil {
		r.mu.Unlock()
		return gitReadRefusal("reader_not_registered"), true
	}
	if registration.suspended {
		r.mu.Unlock()
		return gitReadRefusal("reader_suspended"), true
	}
	if r.active >= 4 {
		r.mu.Unlock()
		return gitReadRefusal("reader_capacity"), true
	}
	reader, ok := registration.runtime.(workspaceGitReader)
	if !ok {
		r.mu.Unlock()
		return gitReadRefusal("reader_unsupported"), true
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
		return gitReadRefusal("duplicate_request"), true
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
		return gitReadRefusal("reader_changed_or_canceled"), true
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
	if req.BoardHistory {
		return readBoardHistory(ctx, req, binding)
	}
	if req.DatasetMerges {
		return readDatasetMerges(ctx, req, binding)
	}
	if req.Graph {
		return readBranchGraph(ctx, req.SessionID, binding, req.Depth)
	}
	if req.MergedChild != "" {
		if !req.IsZero() {
			if req.ChildLaunchGeneration == "" || !protocol.ValidSessionOriginCommit(req.ChildBase) {
				return protocol.CommitResult{ErrorCode: "invalid"}
			}
			return readChildMergedBase(ctx, req.SessionID, binding, req.MergedChild, req.ChildBase, "", req.ChildBranch)
		}
		return readChildMergedBase(ctx, req.SessionID, binding, req.MergedChild, "", "", req.ChildBranch)
	}
	if req.Branch != "" {
		return readWorkspaceBranch(ctx, req.SessionID, binding, req.Branch, req.Base, req.Patch)
	}
	return readWorkspaceCommit(ctx, req.SessionID, binding, req.SHA, req.Patch, req)
}

func (e *LocalExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	if err := req.Validate(e.manifest, e.generation); err != nil {
		return gitReadRefusal("local_reference_invalid")
	}
	if !e.valid() {
		return gitReadRefusal("local_root_changed")
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
		return gitReadRefusal("local_member_unavailable")
	}
	anchor := e.anchors[member.ID]
	if anchor.info == nil {
		return gitReadRefusal("local_anchor_missing")
	}
	live := func() bool {
		if !e.valid() || !e.memberAvailable(member.Path) {
			gitReadRefusal("local_delivery_root_or_member_changed")
			return false
		}
		st, err := anchor.root.Lstat(".git")
		if err != nil || workspaceRootIdentity(st) != e.gitRoots[member.ID] {
			gitReadRefusal("local_git_root_changed")
			return false
		}
		if worktree, ok := e.worktrees[member.ID]; ok {
			if !worktree.publicMatches(anchor.root, ".") {
				gitReadRefusal("local_worktree_pointer_changed")
				return false
			}
			_, safe := worktree.check(ctx)
			if !safe {
				gitReadRefusal("local_worktree_metadata_refused")
			}
			return safe
		}
		return true
	}
	binding := workspaceFileBinding{root: anchor.folder, info: anchor.info, live: live}
	result := readGitBinding(ctx, req, origin, binding)
	if !live() {
		return gitReadRefusal("local_git_binding_changed")
	}
	return result
}
func (v *VirtualHostExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	if req.Validate(v.manifest, v.namespace.generation) != nil || !v.namespace.valid() {
		return gitReadRefusal("virtual_reference_or_namespace_invalid")
	}
	member := v.members[req.MemberID]
	if member == nil {
		return gitReadRefusal("virtual_member_missing")
	}
	return member.readGit(ctx, req, origin)
}
func (r *authorizedHostExplorer) readGit(ctx context.Context, req protocol.ReadCommit, origin bool) protocol.CommitResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.SessionID != r.launch.SessionID {
		return gitReadRefusal("host_session_mismatch")
	}
	if !req.Matches(&r.binding) {
		return gitReadRefusal("host_runtime_mismatch")
	}
	if !r.current(ctx) {
		return gitReadRefusal("host_authority_unavailable")
	}
	reader, ok := r.reader.(workspaceGitReader)
	if !ok {
		return gitReadRefusal("host_reader_unsupported")
	}
	result := reader.readGit(ctx, req, origin)
	if !r.current(ctx) {
		return gitReadRefusal("host_delivery_authority_changed")
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
				result = readChildMergedBase(ctx, req.SessionID, binding, req.MergedChild, req.ChildBase, "refs/heads/"+sandboxBranch(req.SessionID), req.ChildBranch)
			} else {
				result = readGitBinding(ctx, req, false, binding)
			}
			return nil
		}
		return errors.New("member missing")
	})
	if err != nil {
		logDaemonFailure("git read: bound_member_failed", err)
		return failed
	}
	return result
}

func readLegacyGit(ctx context.Context, req protocol.ReadCommit, origin bool, binding workspaceFileBinding) protocol.CommitResult {
	if !req.IsZero() {
		return gitReadRefusal("legacy_reference_rejected")
	}
	if origin && isSandboxClone(binding.root, req.SessionID) {
		commit, ok := readSandboxOrigin(binding)
		return protocol.CommitResult{OK: ok, Commit: &protocol.CommitInfo{SHA: commit}}
	}
	return readGitBinding(ctx, req, origin, binding)
}
