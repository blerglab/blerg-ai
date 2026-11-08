package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// workspaceInstallTarget refuses a sandbox-controlled parent chain: the tree is
// frozen here, so no parent can change between this walk and the rename.
func workspaceInstallTarget(root, clone string, create bool) error {
	rel, err := filepath.Rel(root, clone)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return errWorkspaceMemberCollision
	}
	tree, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = tree.Close() }()
	parts := strings.Split(rel, string(filepath.Separator))
	parent := ""
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		info, err := tree.Lstat(parent)
		switch {
		case err == nil && info.IsDir():
		case err == nil, !os.IsNotExist(err):
			return errors.Join(errWorkspaceMemberCollision, err)
		case create:
			if err := tree.Mkdir(parent, 0700); err != nil {
				return err
			}
		}
	}
	if _, err := tree.Lstat(rel); !os.IsNotExist(err) {
		return errWorkspaceMemberCollision
	}
	return nil
}

func workspaceStagingPath(r workspaceWorkerRequest) string {
	return filepath.Join(filepath.Dir(r.State.Root), "staging", r.State.ActiveWorker)
}

func workspaceStagingSourceMount(r workspaceWorkerRequest) string {
	p := workspaceStagingPath(r)
	if strings.HasPrefix(r.State.RemoteMountSource, "volume:") {
		return "type=volume,src=" + strings.TrimPrefix(r.State.RemoteMountSource, "volume:") + ",dst=" + p + ",volume-subpath=staging/" + r.State.ActiveWorker
	}
	return sandboxBindMount(p, p)
}

func workspaceStagingMount(r workspaceWorkerRequest, source string) (string, error) {
	if !sandboxCloneIDPattern.MatchString(r.State.ActiveWorker) {
		return "", errors.New("staging requires a private worker token")
	}
	if r.Action == "stage-prepare" {
		return workspaceStagingSourceMount(r), nil
	}
	// Both paths must share one mount for an atomic rename. Only this peer sees
	// the allocation, and it has no source mounts.
	if strings.HasPrefix(source, "volume:") {
		return "type=volume,src=" + strings.TrimPrefix(source, "volume:") + ",dst=" + filepath.Dir(r.State.Root), nil
	}
	return sandboxBindMount(filepath.Dir(source), filepath.Dir(r.State.Root)), nil
}

func runWorkspaceStaging(ctx context.Context, r workspaceWorkerRequest) (response workspaceWorkerResponse, err error) {
	worker := dockerWorkspaceWorker{}
	r.Action = "stage-init"
	if response, err = worker.Run(ctx, r); err != nil {
		return response, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceWorkerCleanup)
		defer cancel()
		r.Action = "stage-clean"
		if _, cleanupErr := worker.Run(cleanup, r); cleanupErr != nil {
			err = &workspaceWorkerUncertain{err: errors.Join(err, cleanupErr)}
		}
	}()
	r.Action = "stage-prepare"
	if response, err = worker.Run(ctx, r); err != nil {
		return response, err
	}
	r.Staged = &response.Member
	r.Action = "stage-install"
	return worker.Run(ctx, r)
}

func executeWorkspaceStaging(ctx context.Context, r workspaceWorkerRequest) workspaceWorkerResponse {
	response := workspaceWorkerResponse{State: r.State}
	fail := func(err error) workspaceWorkerResponse { response.Error = err.Error(); return response }
	if !sandboxCloneIDPattern.MatchString(r.State.ActiveWorker) {
		return fail(errors.New("invalid staging token"))
	}
	stage := workspaceStagingPath(r)
	switch r.Action {
	case "stage-init":
		if err := os.MkdirAll(filepath.Dir(stage), 0700); err != nil {
			return fail(err)
		}
		if _, err := workspaceDirectoryIdentity(filepath.Dir(stage)); err != nil {
			return fail(err)
		}
		if err := os.Mkdir(stage, 0700); err != nil {
			return fail(err)
		}
	case "stage-clean":
		// The sandbox has never been given this path or its parent. A token is
		// never reused, including after an uncertain worker outcome.
		if err := os.RemoveAll(stage); err != nil {
			return fail(err)
		}
	case "stage-prepare":
		private := r
		private.Action = "prepare"
		private.State.Root = stage
		private.State.Preparing = true
		private.State.RuntimeIdentity = ""
		private.State.Members = nil
		private.Clone = filepath.Join(stage, "clone")
		private.Source.Member.Path = "clone"
		response = executeWorkspaceWorker(ctx, private)
		response.State = r.State
		response.Member.Member = r.Source.Member
	case "stage-install":
		if r.Staged == nil || r.Staged.Clone != filepath.Join(stage, "clone") || r.Staged.Member != r.Source.Member || !r.Staged.Source.OwnObjects {
			return fail(errors.New("invalid staged receipt"))
		}
		m := *r.Staged
		if id, err := workspaceDirectoryIdentity(m.Clone); err != nil || id != m.CloneIdentity {
			return fail(errors.New("staged clone replaced"))
		}
		if err := checkSandboxCloneTreeContext(ctx, m.Clone); err != nil {
			return fail(err)
		}
		hooks, err := os.ReadDir(filepath.Join(m.Clone, ".git", "hooks"))
		if err != nil && !os.IsNotExist(err) {
			return fail(err)
		}
		for _, hook := range hooks {
			if !strings.HasSuffix(hook.Name(), ".sample") {
				return fail(errors.New("staged clone contains a hook"))
			}
		}
		if err := resetCloneAlternates(m.Clone, &m.Source); err != nil {
			return fail(err)
		}
		if _, err := hostgit.Output(ctx, hostgit.Options{Dir: m.Clone, Timeout: sandboxGitTimeout, Untrusted: true}, "fsck", "--full", "--strict"); err != nil {
			return fail(err)
		}
		if err := workspaceInstallTarget(r.State.Root, r.Clone, false); err != nil {
			return fail(err)
		}
		reserved := protocol.ReservedMemberPath(m.Member.Path)
		if reserved {
			if err := claimReservedFolder(r.State.Root, reservedPaths(r.State)); err != nil {
				return fail(err)
			}
		}
		release := func(err error) workspaceWorkerResponse {
			if gitDir, ok := rootGitDir(r.State); ok && reserved {
				err = errors.Join(err, releaseReservedFolder(r.State.Root, gitDir))
			}
			return fail(err)
		}
		if err := workspaceInstallTarget(r.State.Root, r.Clone, true); err != nil {
			return release(err)
		}
		if err := os.Rename(m.Clone, r.Clone); err != nil {
			return release(err)
		}
		m.Clone = r.Clone
		undo := func(err error) workspaceWorkerResponse {
			removeErr := os.RemoveAll(r.Clone)
			return release(errors.Join(err, removeErr))
		}
		w := &LocalWorkspace{state: r.State, cloneOnly: true}
		m.WorkerPins = workerPins(m)
		w.state.Members = append(append([]workspacePrivateMember(nil), r.State.Members...), m)
		for i := range w.state.Members {
			applyWorkerPins(&w.state.Members[i])
		}
		if err := w.checkAggregateGitBudget(ctx); err != nil {
			return undo(errors.Join(errWorkspaceMemberBudget, err))
		}
		if gitDir, ok := rootGitDir(r.State); ok && reserved {
			if err := addReservedExclude(gitDir, r.State.Root); err != nil {
				return undo(err)
			}
		}
		response.State.WorkerRoot, err = workspaceDirectoryIdentity(r.State.Root)
		if err != nil {
			return undo(err)
		}
		response.Member = m
	}
	return response
}
