package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspacePrepareRootFailurePreservesAllocation(t *testing.T) {
	request := workspacePrepareRequest(t)
	request.Source.Member.Path = "."
	request.Clone = request.State.Root
	before, err := os.Stat(request.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	response := executeWorkspaceWorkerWithDissociate(t.Context(), request, fetchWorkspaceWorkerPack, func(context.Context, string, *sandboxClone) error { return errors.New("injected dissociation failure") })
	if !strings.Contains(response.Error, "injected dissociation failure") {
		t.Fatal(response.Error)
	}
	after, err := os.Stat(request.State.Root)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("allocation root removed: %v", err)
	}
	entries, err := os.ReadDir(request.State.Root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial clone retained: %v %v", entries, err)
	}
}

func TestWorkspaceDigestGitlink(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	clone := w.state.Members[0].Clone
	head := childRepoGit(t, clone, "rev-parse", "HEAD")
	childRepoGit(t, clone, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	childRepoGit(t, clone, "commit", "-m", "gitlink")
	head = childRepoGit(t, clone, "rev-parse", "HEAD")
	if _, err := workspaceTreeDigest(t.Context(), clone, head); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clone, "module"), 0700); err != nil {
		t.Fatal(err)
	}
	m := w.state.Members[0]
	m.Source.Objects = filepath.Join(clone, ".git", "objects")
	if err := workspaceVerifyFetchedBytes(t.Context(), m, head); err != nil {
		t.Fatal(err)
	}
	if clean, err := settleWorkspaceMember(t.Context(), &m, w.state.Session, w.branch()); err != nil || !clean {
		t.Fatalf("uninitialized gitlink blocked settlement: clean=%v err=%v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(clone, "module", "local-work"), []byte("retain me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspaceVerifyFetchedBytes(t.Context(), m, head); err == nil {
		t.Fatal("populated submodule allowed cleanup")
	}
}

func TestWorkspaceEveryActionMountRule(t *testing.T) {
	// Each fake Docker command is a fresh copy of this test binary. The race
	// runtime's default exit delay otherwise dominates the multi-peer protocol.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	// This explicit inventory is compared with the production dispatch switches.
	actions := []string{"storage-preview", "prepare", "stage-init", "stage-prepare", "stage-install", "stage-clean", "membership-check", "membership-discard", "membership-retire", "copy", "release-member", "quota-allocate", "quota-recover", "quota-validate", "quota-release"}
	raw, err := os.ReadFile("workspaceworker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "case \"") {
			continue
		}
		for _, quoted := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "case "), ":"), ", ") {
			action := strings.Trim(quoted, "\"")
			found := false
			for _, known := range actions {
				if known == action {
					found = true
				}
			}
			if !found {
				t.Fatalf("worker action %q needs mount coverage", action)
			}
		}
	}
	for _, action := range append(actions, "new-unclassified-action", "prepare-initial") {
		t.Run(action, func(t *testing.T) {
			w, _, _, sources := localWorkspaceFixture(t)
			bin := t.TempDir()
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(self, filepath.Join(bin, "docker")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("BLERG_WORKSPACE_WORKER_IMAGE", "worker-test")
			w.state.ActiveWorker, w.state.WorkerContext = "split-test", "test-context"
			requestAction := action
			if action == "prepare-initial" {
				requestAction = "prepare"
				w.state.Preparing = true
				w.state.RuntimeIdentity = ""
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, runErr := (dockerWorkspaceWorker{}).Run(ctx, workspaceWorkerRequest{Action: requestAction, State: w.state, Source: sources[0], Clone: w.state.Members[0].Clone})
			if action == "new-unclassified-action" {
				if runErr == nil {
					t.Fatal("unsafe/unclassified action admitted")
				}
				if _, err := os.Stat(filepath.Join(bin, "calls.jsonl")); !os.IsNotExist(err) {
					t.Fatal("refusal happened after Docker launch")
				}
				return
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			raw, err := os.ReadFile(filepath.Join(bin, "calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var args []string
				if err := json.Unmarshal([]byte(line), &args); err != nil {
					t.Fatal(err)
				}
				if len(args) < 3 || args[2] != "create" {
					continue
				}
				sourceMount, allocationMount := false, false
				for i, arg := range args {
					if arg != "--mount" {
						continue
					}
					for _, source := range sources {
						if action == "prepare-initial" && source.Path == sources[0].Path {
							continue
						}
						if strings.Contains(args[i+1], "src="+source.Path+",") || strings.Contains(args[i+1], "src="+source.CommonPath+",") {
							sourceMount = true
							if action != "prepare" && action != "stage-prepare" {
								t.Fatalf("%s exposes source: %s", action, args[i+1])
							}
						}
					}
					allocationMount = allocationMount || strings.Contains(args[i+1], "src="+w.state.Root+",") || strings.Contains(args[i+1], "src="+filepath.Dir(w.state.Root)+",")
				}
				if action != "prepare-initial" && sourceMount && allocationMount {
					t.Fatal("source and sandbox allocation share a container")
				}
			}
		})
	}
}

func TestWorkspacePackPairByteCap(t *testing.T) {
	bin := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(bin, "docker")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	request := workspaceWorkerRequest{Action: "copy"}
	for _, limit := range []int64{4, 5} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		err := runWorkspacePackPairLimit(ctx, request, []string{"run", "test-pack-server"}, []string{"run", "test-pack-writer"}, limit)
		cancel()
		if limit == 4 && (err == nil || !strings.Contains(err.Error(), "size limit")) {
			t.Fatalf("pair ignored cap: %v", err)
		}
		if limit == 5 && err != nil {
			t.Fatalf("pair rejected exact cap: %v", err)
		}
	}
}

func TestWorkspaceWorkerMountBoundaryRejectsMixedPeers(t *testing.T) {
	request := workspaceWorkerRequest{Action: "copy"}
	request.State.Root = "/allocation/tree"
	request.State.Sources = []WorkspaceSource{{Path: "/source/repo", CommonPath: "/source/common.git"}}
	for _, remote := range []string{"", "volume:test-allocation"} {
		request.State.RemoteMountSource = remote
		root := workspaceStorageMount(request.State.Root, request.State.Root)
		if remote != "" {
			root = workspaceStorageMount(remote, request.State.Root)
		}
		args := []string{"run", "--mount", root, "--mount", sandboxBindMount("/source", "/source")}
		if err := validateWorkspaceWorkerMounts(request, args); err == nil {
			t.Fatal("mixed parent mount admitted")
		}
		// Test the peer production path too: it must refuse before invoking Docker.
		if err := runWorkspacePackPair(t.Context(), request, args, []string{"run"}); err == nil {
			t.Fatal("mixed pair admitted")
		}
	}
}

func TestWorkspacePrepareExcludeFailureCleansClone(t *testing.T) {
	request := workspacePrepareRequest(t)
	request.State.Launch.Members[0].Path = "."
	request.Source.Member.Path = protocol.WorkspaceReservedFolder + "/added"
	request.Clone = filepath.Join(request.State.Root, request.Source.Member.Path)
	gitDir := filepath.Join(request.State.Root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info", "exclude"), 0700); err != nil {
		t.Fatal(err)
	}
	response := executeWorkspaceWorkerWithDissociate(t.Context(), request, fetchWorkspaceWorkerPack, dissociateClone)
	if response.Error == "" {
		t.Fatal("bad exclude accepted")
	}
	if _, err := os.Stat(request.Clone); !os.IsNotExist(err) {
		t.Fatalf("partial clone retained: %v (%s)", err, response.Error)
	}
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatal("existing root Git removed")
	}
}

func TestWorkspaceLegacyStrictFsckRetainsHistory(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	m := w.state.Members[0]
	tree := childRepoGit(t, m.Clone, "rev-parse", "HEAD^{tree}")
	// Write malformed legacy history deliberately: newer Git validates commit
	// headers on hash-object unless --literally bypasses the writer's check.
	raw := "tree " + tree + "\nauthor Missing Email 1 +0000\ncommitter Missing Email 1 +0000\n\nold history\n"
	cmd := exec.Command("git", "-C", m.Clone, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(raw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create legacy object: %v %s", err, out)
	}
	oid := strings.TrimSpace(string(out))
	childRepoGit(t, m.Clone, "update-ref", "refs/heads/legacy", oid)
	m.Source.OwnObjects = false
	if err := resetCloneAlternates(m.Clone, &m.Source); err != nil {
		t.Fatal(err)
	}
	if err := migrateWorkspaceObjects(t.Context(), m.Clone, &m.Source); err == nil || !strings.Contains(err.Error(), "work is retained") {
		t.Fatalf("legacy history accepted without recovery: %v", err)
	}
	if got := childRepoGit(t, m.Clone, "rev-parse", "refs/heads/legacy"); got != oid {
		t.Fatal("legacy ref lost")
	}
	if _, err := os.Stat(m.Clone); err != nil {
		t.Fatalf("recovery path lost: %v", err)
	}
}
