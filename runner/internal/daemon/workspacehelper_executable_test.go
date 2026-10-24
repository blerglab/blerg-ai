//go:build linux || darwin

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceExplorerExecutablePipesAndViewerProgress(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "workspace-explorer")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/workspace-explorer")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, out)
	}
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			root := t.TempDir()
			member := filepath.Join(root, "member")
			if err := os.Mkdir(member, 0700); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 501; i++ {
				writeExplorerFile(t, member, fmt.Sprintf("%04d-%s.txt", i, strings.Repeat("x", 140)))
			}
			writeExplorerFile(t, member, "visible.txt")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--public-root", root)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			enc := json.NewEncoder(input)
			dec := json.NewDecoder(output)
			manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: runtime, LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "member", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/member"}}}
			ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}
			if err := enc.Encode(WorkspaceExplorerStart{SessionID: "session", Manifest: manifest, Runtime: ref}); err != nil {
				t.Fatal(err)
			}
			var prepared protocol.WorkspaceRuntimePrepared
			if err := dec.Decode(&prepared); err != nil {
				t.Fatalf("ordinary inherited pipe startup: %v", err)
			}
			if prepared.Binding.Validate(manifest) != nil {
				t.Fatal("invalid root binding")
			}
			req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", SessionID: "session", RequestID: "tree", WorkspaceRuntimeRef: ref, Operation: "list", Path: "member"}
			if err := enc.Encode(req); err != nil {
				t.Fatal(err)
			}
			// A page is much larger than the OS pipe buffer. Hold the pipe beyond the
			// former 100ms mutex-acquisition limit before accepting either response.
			time.Sleep(150 * time.Millisecond)
			if err := enc.Encode(protocol.ReadWorkspaceFile{Type: "read_workspace_file", SessionID: "session", RequestID: "viewer", WorkspaceRuntimeRef: ref, Path: "member/visible.txt"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			gotViewer, gotTree := false, false
			for i := 0; i < 2; i++ {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					t.Fatal(err)
				}
				var envelope struct {
					Type      string
					RequestID string `json:"request_id"`
					OK        bool
				}
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Type == "workspace_file_result" && envelope.RequestID == "viewer" && envelope.OK {
					gotViewer = true
				}
				if envelope.Type == "workspace_explorer_result" && envelope.RequestID == "tree" && envelope.OK {
					gotTree = true
				}
			}
			if !gotViewer || !gotTree {
				t.Fatal("admitted viewer or tree response lost")
			}
			req.RequestID = "navigation-tree"
			if err := enc.Encode(req); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			if err := enc.Encode(protocol.ReadWorkspaceFile{Type: "read_workspace_file", SessionID: "session", RequestID: "canceled-viewer", WorkspaceRuntimeRef: ref, Path: "member/visible.txt"}); err != nil {
				t.Fatal(err)
			}
			if err := enc.Encode(protocol.ReadWorkspaceFile{Type: "cancel_workspace_file", SessionID: "session", RequestID: "canceled-viewer"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			req.RequestID = "after-navigation"
			req.Path = ""
			if err := enc.Encode(req); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				var envelope struct {
					RequestID string `json:"request_id"`
				}
				if err := dec.Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.RequestID == "canceled-viewer" {
					t.Fatal("navigation delivered canceled viewer")
				}
			}
			req.Path = "member"
			req.RequestID = "blocked-output"
			if err := enc.Encode(req); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			start := time.Now()
			// Keep stdin open: write failure must terminate the helper itself,
			// rather than wait forever for a coordinator that cannot read it.
			if err := cmd.Wait(); err == nil {
				t.Fatal("poisoned output stream exited successfully")
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("blocked-output shutdown exceeded bound")
			}

		})
	}
}

func TestWorkspaceOutputQueueReservesViewerAndCancels(t *testing.T) {
	var q workspaceOutputQueue
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 4)
	order := make(chan string, 4)
	go func() {
		done <- q.send(ctx, false, func() error { close(started); <-release; order <- "first"; return nil })
	}()
	<-started
	go func() { done <- q.send(ctx, false, func() error { order <- "tree"; return nil }) }()
	// Observe admission directly, without relying on goroutine scheduling.
	wait := func(viewer bool) {
		t.Helper()
		for {
			q.mu.Lock()
			n := len(q.explorer)
			if viewer {
				n = len(q.viewer)
			}
			q.mu.Unlock()
			if n > 0 {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("queue admission timed out")
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	wait(false)
	go func() { done <- q.send(ctx, true, func() error { order <- "viewer"; return nil }) }()
	wait(true)
	close(release)
	for i, want := range []string{"first", "viewer", "tree"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("service %d: %s != %s", i, got, want)
			}
		case <-ctx.Done():
			t.Fatal("viewer did not progress")
		}
	}
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := q.send(canceled, true, func() error { t.Error("canceled output sent"); return nil }); err == nil {
		t.Fatal("canceled output accepted")
	}
}
