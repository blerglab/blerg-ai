package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Go Docker-CLI fixture receives the production argv for all three containers.
// It speaks only the bounded control protocol; no Git parser sees both mounts.
func workspaceCopyDockerMain() bool {
	if filepath.Base(os.Args[0]) != "docker" {
		return false
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		os.Exit(121)
	}
	log, err := os.OpenFile(filepath.Join(filepath.Dir(path), "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(122)
	}
	_ = json.NewEncoder(log).Encode(os.Args[1:])
	_ = log.Close()
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "--context" {
		args = args[2:]
	}
	if len(args) == 0 {
		os.Exit(123)
	}
	switch args[0] {
	case "run":
		if len(args) == 2 && args[1] == "test-pack-server" {
			fmt.Print("12345")
		} else if len(args) == 2 && args[1] == "test-pack-writer" {
			_, _ = io.Copy(io.Discard, os.Stdin)
		}
	case "create":
		fmt.Println(strings.Repeat("a", 64))
	case "inspect":
		fmt.Println("split-test")
	case "start":
		var request workspaceWorkerRequest
		decoder := json.NewDecoder(os.Stdin)
		if decoder.Decode(&request) != nil {
			os.Exit(124)
		}
		if request.Action != "copy" {
			_ = json.NewEncoder(os.Stdout).Encode(workspaceWorkerResponse{State: request.State})
			return true
		}
		m := request.State.Members[0]
		branch := "refs/heads/wip/" + request.State.Session
		encoder := json.NewEncoder(os.Stdout)
		_ = encoder.Encode(workspacePackMessage{Kind: "pack", Repo: m.Source.Repo, Clone: m.Clone, Branch: branch, Target: branch})
		if decoder.Decode(new(workspacePackMessage)) != nil {
			os.Exit(125)
		}
		_ = encoder.Encode(workspacePackMessage{Kind: "response", Response: &workspaceWorkerResponse{State: request.State}})
	}
	return true
}

func TestWorkspaceCopyAllContainerMounts(t *testing.T) {
	for _, remote := range []string{"bind", "volume:workspace-test"} {
		t.Run(remote, func(t *testing.T) { testWorkspaceCopyAllContainerMounts(t, remote) })
	}
}

func testWorkspaceCopyAllContainerMounts(t *testing.T, remote string) {
	w, _, _, _ := localWorkspaceFixture(t)
	if remote != "bind" {
		w.state.RemoteMountSource = remote
	}
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
	w.state.ActiveWorker = "split-test"
	w.state.WorkerContext = "test-context"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := (dockerWorkspaceWorker{}).Run(ctx, workspaceWorkerRequest{Action: "copy", State: w.state}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(bin, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	containers := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		if len(args) < 3 || (args[2] != "create" && args[2] != "run") {
			continue
		}
		containers++
		allocation, source := false, false
		for i, arg := range args {
			if arg != "--mount" {
				continue
			}
			mount := args[i+1]
			allocation = allocation || strings.Contains(mount, "dst="+w.state.Root)
			for _, s := range w.state.Sources {
				source = source || strings.Contains(mount, "src="+s.Path) || strings.Contains(mount, "src="+s.CommonPath+",")
			}
		}
		if allocation && source {
			t.Fatalf("container combines source and allocation: %v", args)
		}
	}
	if containers != 3 {
		t.Fatalf("expected control, server and writer; got %d: %s", containers, raw)
	}
}

func TestWorkspacePackByteCap(t *testing.T) {
	var out bytes.Buffer
	err := copyWorkspacePack(&out, strings.NewReader("12345"), 4)
	if err == nil || !strings.Contains(err.Error(), "size limit") || out.String() != "1234" {
		t.Fatalf("pack cap failed: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := copyWorkspacePack(&out, strings.NewReader("1234"), 4); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePrepareDissociateFailureCleansNonLiveClone(t *testing.T) {
	request := workspacePrepareRequest(t)
	response := executeWorkspaceWorkerWithDissociate(t.Context(), request, fetchWorkspaceWorkerPack, func(context.Context, string, *sandboxClone) error {
		return errors.New("injected dissociation failure")
	})
	if !strings.Contains(response.Error, "injected dissociation failure") {
		t.Fatalf("wrong failure: %s", response.Error)
	}
	if _, err := os.Lstat(request.Clone); !os.IsNotExist(err) {
		t.Fatalf("partial clone left after failed preparation: %v", err)
	}
}

func TestWorkspacePrepareIndependentObjectsBudgetNonLive(t *testing.T) {
	request := workspacePrepareRequest(t)
	response := executeWorkspaceWorkerWithDissociate(t.Context(), request, fetchWorkspaceWorkerPack, func(ctx context.Context, path string, c *sandboxClone) error {
		if err := dissociateClone(ctx, path, c); err != nil {
			return err
		}
		f, err := os.Create(filepath.Join(path, ".git", "objects", "oversized"))
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		return f.Truncate(1 << 40) // sparse: measures accounting without allocating disk
	})
	if !strings.Contains(response.Error, errWorkspaceMemberBudget.Error()) {
		t.Fatalf("non-live object budget accepted: %s", response.Error)
	}
	if _, err := os.Lstat(request.Clone); !os.IsNotExist(err) {
		t.Fatalf("over-budget clone left: %v", err)
	}
}

func workspacePrepareRequest(t *testing.T) workspaceWorkerRequest {
	t.Helper()
	w, _, _, sources := localWorkspaceFixture(t)
	state := w.state
	state.Root = t.TempDir()
	state.Members = nil
	state.Preparing = true
	state.RuntimeIdentity = ""
	return workspaceWorkerRequest{Action: "prepare", State: state, Source: sources[0], Clone: filepath.Join(state.Root, sources[0].Member.Path)}
}

func TestWorkspaceIndependentAlternatesNeedNoSourceMount(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	m := w.state.Members[0]
	m.Source.Objects = filepath.Join(t.TempDir(), "unmounted-source-objects")
	if err := resetCloneAlternates(m.Clone, &m.Source); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePackDigestBindsCommitAndBytes(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	m := w.state.Members[0]
	head := childRepoGit(t, m.Clone, "rev-parse", "HEAD")
	want, err := workspaceTreeDigest(t.Context(), m.Clone, head)
	if err != nil {
		t.Fatal(err)
	}
	got, err := workspaceTreeDigest(t.Context(), m.Source.Repo, head)
	if err != nil || got != want {
		t.Fatalf("identical trees differ: %v", err)
	}
	childRepoGit(t, m.Clone, "-c", "user.name=Example", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "metadata-only change")
	newHead := childRepoGit(t, m.Clone, "rev-parse", "HEAD")
	got, err = workspaceTreeDigest(t.Context(), m.Clone, newHead)
	if err != nil || got == want {
		t.Fatalf("different commit accepted: %v", err)
	}
	blob := childRepoGit(t, m.Source.Repo, "rev-parse", head+":source-one.txt")
	object := filepath.Join(m.Source.Objects, blob[:2], blob[2:])
	if err := os.Chmod(object, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceTreeDigest(t.Context(), m.Source.Repo, head); err == nil {
		t.Fatal("corrupt existing source object accepted")
	}
}

func TestWorkspaceBorrowedObjectsMigration(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			w, _, _, _ := localWorkspaceFixture(t)
			m := w.state.Members[0]
			m.Source.OwnObjects = false
			if err := resetCloneAlternates(m.Clone, &m.Source); err != nil {
				t.Fatal(err)
			}
			if !complete {
				objects := filepath.Join(m.Clone, ".git", "objects")
				entries, err := os.ReadDir(objects)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "info" {
						if err := os.RemoveAll(filepath.Join(objects, entry.Name())); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			err := migrateWorkspaceObjects(t.Context(), m.Clone, &m.Source)
			if complete {
				if err != nil || !m.Source.OwnObjects {
					t.Fatalf("complete local objects did not migrate: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(m.Clone, ".git", "objects", "info", "alternates"))
				if err != nil || len(data) != 0 {
					t.Fatalf("migration retained dependency: %q %v", data, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "new session") || m.Source.OwnObjects {
					t.Fatalf("incomplete migration accepted: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(m.Clone, ".git", "objects", "info", "alternates"))
				if err != nil || string(data) != m.Source.Objects+"\n" {
					t.Fatalf("failed migration lost recovery dependency: %q %v", data, err)
				}
			}
		})
	}
}

func TestWorkspaceCopyServerCannotWriteSourceGit(t *testing.T) {
	bin, log := fakeDocker(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BLERG_WORKSPACE_WORKER_IMAGE", "worker-test")
	root := t.TempDir()
	source := t.TempDir()
	common := filepath.Join(source, ".git")
	request := workspaceWorkerRequest{Action: "copy"}
	request.State.Root = root
	request.State.ActiveWorker = "split-test"
	request.State.WorkerContext = "test-peer"
	request.State.Sources = []WorkspaceSource{{Path: source, CommonPath: common}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, _ = (dockerWorkspaceWorker{}).Run(ctx, request)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "src="+source) || strings.Contains(string(raw), "src="+common) {
		t.Fatal("copy control worker exposes source alongside sandbox data")
	}
}

func TestWorkspacePackPeersHaveDisjointMounts(t *testing.T) {
	for _, remote := range []string{"", "volume:workspace-test"} {
		t.Run(remote, func(t *testing.T) {
			request := workspaceWorkerRequest{}
			request.State.Root = "/sandbox/tree"
			request.State.RemoteMountSource = remote
			source := WorkspaceSource{Path: "/source/repo", CommonPath: "/source/common.git"}
			server := workspacePackServerArgs(request, "/sandbox/tree/member", "server", "worker-test", "", "policy")
			writer := workspacePackWriterArgs(request, source, source.Path, "refs/heads/wip/test", "refs/heads/wip/test", "writer", "worker-test", "", "policy")
			mounts := func(args []string) []string {
				var result []string
				for i := range args {
					if args[i] == "--mount" {
						result = append(result, args[i+1])
					}
				}
				return result
			}
			serverMounts, writerMounts := mounts(server), mounts(writer)
			if len(serverMounts) != 1 || !strings.Contains(serverMounts[0], "dst=/sandbox/tree") || !strings.HasSuffix(serverMounts[0], ",readonly") {
				t.Fatalf("unsafe server mounts: %v", serverMounts)
			}
			for _, mount := range serverMounts {
				if strings.Contains(mount, "/source/") {
					t.Fatalf("server exposes person's Git: %s", mount)
				}
			}
			if len(writerMounts) != 2 || writerMounts[1] != sandboxBindMount(source.CommonPath, source.CommonPath) {
				t.Fatalf("writer Git mount missing: %v", writerMounts)
			}
			for _, mount := range writerMounts {
				if strings.Contains(mount, "/sandbox/") || strings.Contains(mount, "workspace-test") {
					t.Fatalf("writer exposes sandbox: %s", mount)
				}
			}
			for _, args := range [][]string{server, writer} {
				joined := strings.Join(args, " ")
				for _, flag := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--memory=512m", "--pids-limit=64", "--pull=never", "--signal=KILL"} {
					if !strings.Contains(joined, flag) {
						t.Errorf("peer lacks %s", flag)
					}
				}
			}
		})
	}
}

func TestWorkspacePackControlIsBounded(t *testing.T) {
	stream := &workspacePackStream{reader: bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", workspacePackControlLimit+1)), workspacePackControlLimit)}
	if _, err := stream.receive(); !errors.Is(err, bufio.ErrBufferFull) {
		t.Fatalf("unbounded control message accepted: %v", err)
	}
}

func TestWorkspacePackRefusesBorrowedObjects(t *testing.T) {
	stream := &workspacePackStream{}
	if _, err := stream.fetch(t.Context(), time.Second, "/clone", "test", &sandboxClone{}, "branch", "target"); err == nil || !strings.Contains(err.Error(), "retained") || !strings.Contains(err.Error(), "new session") {
		t.Fatalf("missing actionable borrowed-object recovery: %v", err)
	}
}
