package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceLiveStagingPolicies(t *testing.T) {
	r := workspacePrepareRequest(t)
	r.State.Preparing = false
	r.State.RuntimeIdentity = "live"
	for _, action := range []string{"stage-init", "stage-prepare", "stage-install", "stage-clean"} {
		r.Action = action
		sources, err := workspaceWorkerSources(r)
		if err != nil {
			t.Fatalf("%s refused: %v", action, err)
		}
		if (len(sources) == 1) != (action == "stage-prepare") {
			t.Fatalf("%s sources: %+v", action, sources)
		}
	}
}

func TestWorkspaceLiveStagingUncertainKeepsFence(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.w.state.ActiveWorker = "uncertain-stage"
	f.w.state.Membership = &workspaceMembershipJournal{Operation: membershipTestOp, Phase: membershipFencing}
	if err := f.service.abandonMembership(t.Context(), f.w); err == nil {
		t.Fatal("uncertain worker abandoned")
	}
	if f.w.state.Membership == nil || f.runtime.thaw != 0 {
		t.Fatal("writers reopened without worker cleanup proof")
	}
}

func TestWorkspaceMembershipCheckWithoutSource(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	m := w.state.Members[1]
	if err := os.Rename(m.Source.Repo, m.Source.Repo+"-unmounted"); err != nil {
		t.Fatal(err)
	}
	response := executeWorkspaceWorker(t.Context(), workspaceWorkerRequest{Action: "membership-check", State: w.state, Record: w.record, Clone: m.Clone})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
}

func TestWorkspaceLiveStagingBudgetRollback(t *testing.T) {
	r := stagingRequest(t)
	w, _, _, _ := localWorkspaceFixture(t)
	r.State.Members = w.state.Members
	path := filepath.Join(w.state.Members[0].Clone, ".git", "large-metadata")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(protocol.DefaultWorkspaceBudget().MetadataBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	response := executeWorkspaceStaging(t.Context(), r)
	if !strings.Contains(response.Error, errWorkspaceMemberBudget.Error()) {
		t.Fatal(response.Error)
	}
	if _, err := os.Lstat(r.Clone); !os.IsNotExist(err) {
		t.Fatal("failed install retained clone", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("rollback touched existing member", err)
	}
}

func TestWorkspaceLiveStagingMountRule(t *testing.T) {
	for _, remote := range []string{"", "volume:allocation-test"} {
		r := workspacePrepareRequest(t)
		r.State.Root = filepath.Join(t.TempDir(), "tree")
		r.State.ActiveWorker = "private-stage"
		r.State.RemoteMountSource = remote
		rootSource := r.State.Root
		if remote != "" {
			rootSource = remote
		}
		for _, action := range []string{"stage-prepare", "stage-install"} {
			r.Action = action
			mount, err := workspaceStagingMount(r, rootSource)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"create", "--mount", mount}
			if action == "stage-prepare" {
				args = append(args, "--mount", sandboxBindMount(r.Source.Path, r.Source.Path)+",readonly")
			}
			if err := validateWorkspaceWorkerMounts(r, args); err != nil {
				t.Fatalf("%s %s: %v", remote, action, err)
			}
			args = append(args, "--mount", workspaceStorageMount(rootSource, r.State.Root), "--mount", sandboxBindMount(r.Source.Path, r.Source.Path)+",readonly")
			if err := validateWorkspaceWorkerMounts(r, args); err == nil {
				t.Fatalf("%s %s admitted mixed mounts", remote, action)
			}
		}
	}
}

func TestP2DockerLiveAddStaging(t *testing.T) {
	w, container := p2DockerWorkspace(t)
	f := sandboxMembershipFrom(t, w, &testWorkspaceRuntime{})
	if err := container.Freeze(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Thaw(context.Background()) })
	w.state.RuntimeIdentity = container.Context + ":" + container.ID
	w.state.WorkerContext = container.Context
	w.state.ActiveWorker = "live-staging-" + ccUUID()
	source := WorkspaceSource{Member: f.msg.Proposed.Members[2], Path: f.source, CommonPath: filepath.Join(f.source, ".git")}
	ctx, cancel := context.WithTimeout(t.Context(), membershipCloneSeconds*time.Second)
	defer cancel()
	response, err := (dockerWorkspaceWorker{}).Run(ctx, workspaceWorkerRequest{Action: "prepare", State: w.state, Source: source, Clone: f.clone})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Member.Source.OwnObjects || response.Member.Clone != f.clone {
		t.Fatalf("bad live receipt: %+v", response.Member)
	}
	if err := container.Thaw(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := p2Docker(t, "exec", container.ID, "git", "-C", "/workspace/three", "rev-parse", "HEAD"); got != response.Member.Source.Base {
		t.Fatalf("installed clone: %s", got)
	}
	if got := p2Docker(t, "exec", container.ID, "sh", "-c", "test ! -e /workspace/staging && echo private"); got != "private" {
		t.Fatal("staging exposed")
	}
}

func stagingRequest(t *testing.T) workspaceWorkerRequest {
	t.Helper()
	r := workspacePrepareRequest(t)
	r.State.Root = filepath.Join(t.TempDir(), "tree")
	if err := os.Mkdir(r.State.Root, 0700); err != nil {
		t.Fatal(err)
	}
	r.Clone = filepath.Join(r.State.Root, r.Source.Member.Path)
	r.State.Preparing = false
	r.State.RuntimeIdentity = "live"
	r.State.ActiveWorker = "staging-test"
	if err := os.Mkdir(filepath.Join(filepath.Dir(r.State.Root), "staging"), 0700); err != nil {
		t.Fatal(err)
	}
	r.Action = "stage-init"
	if response := executeWorkspaceStaging(t.Context(), r); response.Error != "" {
		t.Fatal(response.Error)
	}
	r.Action = "stage-prepare"
	response := executeWorkspaceStaging(t.Context(), r)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	r.Staged = &response.Member
	r.Action = "stage-install"
	return r
}

func TestWorkspaceLiveStagingInstallWithoutSource(t *testing.T) {
	r := stagingRequest(t)
	if _, err := os.Stat(r.Clone); !os.IsNotExist(err) {
		t.Fatal("staged clone visible before verification")
	}
	if err := os.Rename(r.Source.Path, r.Source.Path+"-unmounted"); err != nil {
		t.Fatal(err)
	}
	response := executeWorkspaceStaging(t.Context(), r)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if response.Member.Clone != r.Clone || !response.Member.Source.OwnObjects {
		t.Fatalf("bad receipt: %+v", response.Member)
	}
	if _, err := os.Stat(filepath.Join(r.Clone, ".git", "objects")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLiveStagingRejectsUntrustedMetadata(t *testing.T) {
	for _, kind := range []string{"include", "hook", "corrupt", "collision"} {
		t.Run(kind, func(t *testing.T) {
			r := stagingRequest(t)
			gitDir := filepath.Join(r.Staged.Clone, ".git")
			switch kind {
			case "include":
				if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[include]\npath=/unmounted/config\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hook":
				if err := os.WriteFile(filepath.Join(gitDir, "hooks", "post-checkout"), []byte("unsafe hook"), 0700); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.RemoveAll(filepath.Join(gitDir, "objects")); err != nil {
					t.Fatal(err)
				}
			case "collision":
				if err := os.Mkdir(r.Clone, 0700); err != nil {
					t.Fatal(err)
				}
			}
			response := executeWorkspaceStaging(t.Context(), r)
			if response.Error == "" {
				t.Fatalf("%s accepted", kind)
			}
			if kind == "include" && !strings.Contains(response.Error, "include") {
				t.Fatal(response.Error)
			}
			if _, err := os.Stat(r.Staged.Clone); err != nil {
				t.Fatalf("verification failure lost staged receipt: %v", err)
			}
		})
	}
}

func TestWorkspaceInstallTargetRefusesLinkedParents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sibling", filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sibling"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, clone := range []string{"linked/member", "inside/member", "sibling"} {
		if err := workspaceInstallTarget(root, filepath.Join(root, clone), true); err == nil {
			t.Fatalf("%s: installed through an unsafe target", clone)
		}
	}
	if err := workspaceInstallTarget(root, filepath.Join(root, "a", "b", "member"), true); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(root, "a", "b")); err != nil || !info.IsDir() {
		t.Fatalf("parent not created: %v", err)
	}
}
