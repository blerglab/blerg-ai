//go:build linux || darwin

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceExplorerWorktreeSpecialPointers(t *testing.T) {
	for _, pointer := range []string{"gitdir", "commondir"} {
		for _, kind := range []string{"fifo", "symlink", "replace"} {
			t.Run(pointer+"/"+kind, func(t *testing.T) {
				e, root, req := explorerFixture(t)
				source := filepath.Join(root, "source")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				git := func(args ...string) {
					t.Helper()
					cmd := exec.Command("git", args...)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("fixture: %s %v", out, err)
					}
				}
				git("init", source)
				git("-C", source, "-c", "user.name=Example", "-c", "user.email=agent@example.com", "commit", "--allow-empty", "-m", "fixture")
				git("-C", source, "worktree", "add", "-b", "fixture", filepath.Join(root, "member"))
				data, err := os.ReadFile(filepath.Join(root, "member", ".git"))
				if err != nil {
					t.Fatal(err)
				}
				metadata := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: "))
				target := filepath.Join(metadata, pointer)
				replace := func() {
					t.Helper()
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
					if kind == "symlink" {
						if err := os.Symlink(filepath.Join(metadata, "HEAD"), target); err != nil {
							t.Fatal(err)
						}
					} else if err := unix.Mkfifo(target, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "replace" {
					e.pinMembers()
					replace()
				} else {
					replace()
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					if kind != "replace" {
						e.pinMembers()
					}
					req.Operation = "status"
					req.Path = "member"
					req.MemberID = "one"
					if result := e.Explore(context.Background(), req); result.OK {
						t.Error("special pointer accepted")
					}
				}()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("metadata FIFO blocked preparation/status without a writer")
				}
			})
		}
	}
}

func TestWorkspaceDescriptorWalkMovedAncestors(t *testing.T) {
	for _, schedule := range []string{"outside", "replace-deeper"} {
		t.Run(schedule, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0700); err != nil {
				t.Fatal(err)
			}
			writeExplorerFile(t, filepath.Join(root, "a", "b"), "visible.txt")
			pinned, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer pinned.Close()
			file, err := workspaceWalkOpen(pinned, "a/b/visible.txt", func(i int) {
				if i != 1 {
					return
				}
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				if schedule == "replace-deeper" {
					must(os.Rename(filepath.Join(root, "a", "b"), filepath.Join(root, "a", "saved")))
					must(os.Mkdir(filepath.Join(root, "a", "b"), 0700))
					writeExplorerFile(t, filepath.Join(root, "a", "b"), "visible.txt")
				} else {
					must(os.Rename(filepath.Join(root, "a"), filepath.Join(outside, "a")))
					if schedule == "out-and-back" {
						must(os.Rename(filepath.Join(outside, "a"), filepath.Join(root, "a")))
					}
				}
			})
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatal("moved/replaced descriptor chain accepted")
			}
		})
	}
}

func TestWorkspaceExplorerRootAndMemberVisibility(t *testing.T) {
	for _, component := range []string{".ssh", ".git", ".env", ".aws"} {
		t.Run(component, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), component, "public")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			writeExplorerFile(t, root, "visible.txt")
			info, _ := os.Stat(root)
			manifest := protocol.WorkspaceManifest{LaunchGeneration: "launch", Runtime: "host"}
			if e, err := newLocalExplorer(root, info, manifest, 1, func() bool { return true }); err == nil {
				_ = e.Close()
				t.Fatal("protected root accepted")
			}
			root = t.TempDir()
			info, _ = os.Stat(root)
			manifest.Members = []protocol.WorkspaceMember{{ID: "opaque", Name: "visible", Path: "ordinary/" + component}}
			if e, err := newLocalExplorer(root, info, manifest, 1, func() bool { return true }); err == nil {
				_ = e.Close()
				t.Fatal("protected member accepted")
			}
		})
	}
}

func TestWorkspaceExplorerListSearchPathMatrix(t *testing.T) {
	e, root, req := explorerFixture(t)
	writeExplorerFile(t, root, "visible.txt")
	for _, op := range []string{"list", "search"} {
		for _, bad := range []string{"/", "/etc", "../", "../outside", "a/../../outside", "a/../", "./", "a//b", "C:\\outside", "a\\..\\outside", "%2e%2e/outside"} {
			t.Run(op+"/"+bad, func(t *testing.T) {
				req.Operation = op
				req.Path = bad
				req.Filter = "visible"
				res := e.Explore(t.Context(), req)
				if res.OK || res.ErrorCode != "forbidden" || len(res.Entries) > 0 {
					t.Fatalf("unsafe path accepted: %+v", res)
				}
			})
		}
	}
}

func TestWorkspaceExplorerVisitedExhaustion(t *testing.T) {
	e, root, req := explorerFixture(t)
	// Nonmatching files consume visits too; the hit limit cannot terminate this.
	for i := 0; i < protocol.WorkspaceSearchVisited+1; i++ {
		writeExplorerFile(t, root, fmt.Sprintf("entry-%05d", i))
	}
	req.Operation = "search"
	req.Filter = "absent"
	res := e.Explore(t.Context(), req)
	if !res.OK || !res.Truncated || len(res.Entries) != 0 {
		t.Fatalf("visited budget not enforced: %+v", res)
	}
}

func TestWorkspaceExplorerAggregateVisitedExhaustion(t *testing.T) {
	namespace := t.TempDir()
	info, _ := os.Stat(namespace)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Digest: "digest"}
	sources := map[string]HostWorkspaceSource{}
	for i := 0; i < 2; i++ {
		id := fmt.Sprint(i)
		source := t.TempDir()
		st, _ := os.Stat(source)
		sources[id] = HostWorkspaceSource{Path: source, Root: workspaceRootIdentity(st)}
		manifest.Members = append(manifest.Members, protocol.WorkspaceMember{ID: id, Name: "member" + id, Path: "member" + id, SourceIdentity: "source" + id, CommonDirectoryIdentity: "common" + id, Destination: "example-org/member" + id})
		for j := 0; j < 5100; j++ {
			writeExplorerFile(t, source, fmt.Sprintf("entry-%05d", j))
		}
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: "digest", Root: workspaceRootIdentity(info)}
	v, err := NewVirtualHostExplorer(namespace, manifest, binding, sources, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	req := protocol.WorkspaceExplorerRequest{WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Operation: "search", Filter: "absent"}
	res := v.Explore(t.Context(), req)
	if !res.OK || !res.Truncated || len(res.Entries) != 0 {
		t.Fatalf("aggregate budget not enforced: %+v", res)
	}
}
func TestWorkspaceExplorerFIFOAndSocket(t *testing.T) {
	e, root, req := explorerFixture(t)
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	// Bind relative to cwd in a short-lived fixture process: Darwin's Unix
	// socket sockaddr path limit is shorter than many Go test temp paths.
	socket := exec.CommandContext(t.Context(), "python3", "-c", "import socket; s=socket.socket(socket.AF_UNIX); s.bind('socket'); s.close()")
	socket.Dir = root
	if out, err := socket.CombinedOutput(); err != nil {
		t.Fatalf("socket fixture: %v %s", err, out)
	}
	result := e.Explore(context.Background(), req)
	if !result.OK || len(result.Entries) != 0 {
		t.Fatalf("special files disclosed: %+v", result)
	}
}
func TestWorkspaceExplorerDevice(t *testing.T) {
	e, root, req := explorerFixture(t)
	if err := unix.Mknod(filepath.Join(root, "device"), unix.S_IFCHR|0600, int(unix.Mkdev(1, 3))); err != nil {
		// Read-only fallback exercises an existing device, without creating
		// or modifying anything outside this test's temporary workspace.
		info, statErr := os.Stat("/dev/null")
		if statErr != nil || info.Mode()&os.ModeDevice == 0 {
			t.Fatal("device fixture unavailable")
		}
		if workspaceRegularFile(info) {
			t.Fatal("device accepted as a regular file")
		}
		devRoot, statErr := os.Stat("/dev")
		if statErr != nil {
			t.Fatal(statErr)
		}
		if file := readWorkspaceFile("/dev", "null", workspaceFileLimits{expectedRoot: devRoot}); file.OK || file.ErrorCode != "forbidden" {
			t.Fatal("existing device delivered content")
		}
		t.Log("CAP_MKNOD unavailable; existing pinned /dev/null refused")
		return
	}
	result := e.Explore(context.Background(), req)
	if !result.OK || len(result.Entries) != 0 {
		t.Fatalf("device metadata disclosed: %+v", result)
	}
	if file := readWorkspaceFile(root, "device", workspaceFileLimits{}); file.OK {
		t.Fatal("device delivered content")
	}
}

func TestWorkspaceStatusCancellationJoinsBlockedGit(t *testing.T) {
	e, root, req := explorerFixture(t)
	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", member).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %s %v", out, err)
	}
	e.pinMembers()
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	script := "#!/bin/sh\nprintf '%s' \"$$\" > '" + marker + "'\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Cleanup(hostgit.SetBinaryForTest(filepath.Join(bin, "git")))
	var router WorkspaceExplorerRouter
	router.Bind("session", e)
	defer router.Close()
	req.Operation = "status"
	req.Path = "member"
	req.MemberID = "one"
	req.Type = "workspace_explorer"
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	router.Handle(t.Context(), &agentTestSender{}, raw)
	deadline := time.After(3 * time.Second)
	var pid int
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(string(data))
			break
		}
		select {
		case <-deadline:
			t.Fatal("Git did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	start := time.Now()
	router.Unbind("session")
	if time.Since(start) > time.Second {
		t.Fatal("blocked Git not canceled and joined")
	}
	if pid <= 0 || unix.Kill(pid, 0) != unix.ESRCH {
		t.Fatal("Git process outlived reader join")
	}
}

func TestWorkspaceExplorerCursorReplayMatrix(t *testing.T) {
	e, _, _ := explorerFixture(t)
	valid := explorerCursor{Binding: "directory/root/runtime", After: "entry", Expires: time.Now().Add(time.Minute).Unix()}
	token := e.signCursor(valid)
	if _, ok := e.readCursor(token, valid); !ok {
		t.Fatal("valid cursor refused")
	}
	for _, binding := range []string{"other-directory/root/runtime", "directory/other-root/runtime", "directory/root/next-runtime"} {
		want := valid
		want.Binding = binding
		if _, ok := e.readCursor(token, want); ok {
			t.Fatal("cursor scope replay accepted")
		}
	}
	other, _, _ := explorerFixture(t)
	if _, ok := other.readCursor(token, valid); ok {
		t.Fatal("cross-explorer cursor accepted")
	}
	expired := valid
	expired.Expires = time.Now().Add(-time.Minute).Unix()
	if _, ok := e.readCursor(e.signCursor(expired), valid); ok {
		t.Fatal("expired valid-MAC cursor accepted")
	}
}

func TestWorkspaceExplorerVirtualProtectedSources(t *testing.T) {
	namespace := t.TempDir()
	info, _ := os.Stat(namespace)
	source := filepath.Join(t.TempDir(), ".ssh", "selected")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(source)
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "member", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/member"}}}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: "digest", Root: workspaceRootIdentity(info)}
	if v, err := NewVirtualHostExplorer(namespace, manifest, binding, map[string]HostWorkspaceSource{"one": {Path: source, Root: workspaceRootIdentity(st)}}, func() bool { return true }); err == nil {
		_ = v.Close()
		t.Fatal("virtual source below protected directory accepted")
	}
}

func TestWorkspaceExplorerProtectedRootAlias(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, ".ssh", "public")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(filepath.Join(parent, ".ssh"), alias); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(alias, "public")
	info, _ := os.Stat(folder)
	if e, err := newLocalExplorer(folder, info, protocol.WorkspaceManifest{Runtime: "host", LaunchGeneration: "launch"}, 1, func() bool { return true }); err == nil {
		_ = e.Close()
		t.Fatal("ancestor alias exposed protected root")
	}
}
