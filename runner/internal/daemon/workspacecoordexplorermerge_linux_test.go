//go:build linux

package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestCoordExplorerReleaseErrorUnderSessionLock(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	// A failed owned helper must be joined and reported while P2 holds its lock.
	cmd := exec.Command("/bin/sh", "-c", "cat >/dev/null; exit 1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	remote := &remoteWorkspaceExplorer{cmd: cmd, input: input.(*os.File), output: output.(*os.File)}
	reader := &boundWorkspaceExplorer{workspace: w, remote: remote}
	var router WorkspaceExplorerRouter
	defer router.Close()
	router.Bind(w.state.Session, reader)
	w.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- router.Release(t.Context(), w.state.Session) }()
	select {
	case err = <-done:
		w.mu.Unlock()
		if err == nil {
			t.Fatal("failed helper exit accepted")
		}
	case <-time.After(time.Second):
		w.mu.Unlock()
		<-done // Let the negative control unwind after releasing the held lock.
		t.Fatal("reader release reacquired held P2 lock")
	}
	router.mu.Lock()
	registration := router.runtimes[w.state.Session]
	router.mu.Unlock()
	if registration == nil || !registration.suspended {
		t.Fatal("failed helper release lost fenced registration")
	}
	if !remote.failed || remote.cmd != nil {
		t.Fatal("failed helper was not joined")
	}
}

func TestCoordDockerProductionExplorerSyncShellStop(t *testing.T) {
	if os.Getenv("BLERG_EXPLORER_DOCKER_TEST") != "1" {
		t.Skip("production explorer image required")
	}
	w, d, r, _ := dockerCoordRig(t)
	session, ref := w.state.Session, w.state.Binding.WorkspaceRuntimeRef
	runtime, err := w.runtimeExplorer(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	reader := runtime.(*boundWorkspaceExplorer)
	manager := &Manager{agents: &AgentHost{}}
	integration := NewIntegratedWorkspaceService(&WSClient{shellRuntime: r.service, handlers: map[string]func([]byte){}}, func() *Manager { return manager })
	router := &manager.agents.explorer
	defer router.Close()
	router.Bind(session, reader)
	d.CancelStatus = func(ctx context.Context) error { return integration.CancelStatus(ctx, session) }
	d.ResumeStatus = func(ctx context.Context) error { return integration.ResumeStatus(ctx, session) }
	d.CloseStatus = func(ctx context.Context) error { return integration.CloseStatus(ctx, session) }
	status := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", SessionID: session, RequestID: "initial", WorkspaceRuntimeRef: ref, Operation: "status", Path: w.state.Members[0].Member.Path}
	if got := reader.Explore(t.Context(), status); !got.OK {
		t.Fatalf("production status before sync: %+v", got)
	}
	r.open(t, "production-explorer", 0)
	r.input(t, 1, "setsid /bin/sh -c 'while :; do printf x >> /workspace/background.txt; sleep 0.01; done' </dev/null >/dev/null 2>&1 &\nprintf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	childCommit(t, w.state.Members[0].Clone, "merge-viewer.txt")
	sender := newRecordingSender(32)
	run, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() {
		for n := 0; run.Err() == nil; n++ {
			req := status
			req.RequestID = fmt.Sprint(n)
			raw, _ := json.Marshal(req)
			router.Handle(run, sender, raw)
			time.Sleep(time.Millisecond)
		}
	})
	wg.Go(func() {
		for seq := uint64(2); run.Err() == nil; {
			ctx, stop := context.WithTimeout(run, time.Second)
			reply, callErr := r.call(ctx, shellruntime.RPC{SessionID: session, Op: "input", Grant: r.grant, Seq: seq, Data: []byte("printf z\n")})
			if callErr == nil && reply.OK {
				seq++
			}
			stop()
			time.Sleep(5 * time.Millisecond)
		}
	})
	results, syncErr := w.Sync(t.Context())
	cancel()
	wg.Wait()
	if syncErr != nil || len(results) == 0 || results[0].Pending || results[0].SyncError != "" {
		t.Fatalf("production sync: %+v %v", results, syncErr)
	}
	router.mu.Lock()
	registration := router.runtimes[session]
	resumed := registration != nil && !registration.suspended
	router.mu.Unlock()
	if !resumed {
		t.Fatal("sync lost production reader registration")
	}
	status.RequestID = "after-thaw"
	if got := reader.Explore(t.Context(), status); !got.OK {
		t.Fatalf("production status after thaw: %+v", got)
	}
	path := "/workspace/" + w.state.Members[0].Member.Path + "/merge-viewer.txt"
	got := reader.ReadFile(t.Context(), protocol.ReadWorkspaceFile{Type: "read_workspace_file", SessionID: session, RequestID: "viewer", WorkspaceRuntimeRef: ref, Path: path})
	if !got.OK || got.ContentB64 != base64.StdEncoding.EncodeToString([]byte("merge-viewer.txt\n")) {
		t.Fatalf("production viewer after thaw: %+v", got)
	}
	// An idle helper still needs EOF and successful exit before container stop.
	if _, err = w.Stop(t.Context()); err != nil {
		t.Fatalf("production helper not joined before container stop: %v", err)
	}
	if !reader.remote.closed || reader.remote.cmd != nil || reader.remote.failed {
		t.Fatal("production helper exit unproven")
	}
	if r.service.Lifecycle(session) != nil {
		t.Fatal("stop retained P5 registration")
	}
	if absent, e := d.Absent(t.Context()); e != nil || !absent {
		t.Fatal("stop did not prove exact runtime absence", e)
	}
	router.mu.Lock()
	registration = router.runtimes[session]
	router.mu.Unlock()
	if registration != nil {
		t.Fatal("stop retained production reader registration")
	}
}

type mergeStalledReader struct {
	entered chan struct{}
	release chan struct{}
}

func (r *mergeStalledReader) Explore(_ context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	close(r.entered)
	<-r.release
	return explorerFailure(req, "unavailable")
}
func (r *mergeStalledReader) ReadFile(context.Context, protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	return protocol.WorkspaceFileResult{}
}

func TestCoordExplorerCancellationDeadlineRetainsFence(t *testing.T) {
	reader := &mergeStalledReader{entered: make(chan struct{}), release: make(chan struct{})}
	var router WorkspaceExplorerRouter
	defer router.Close()
	router.Bind("session", reader)
	raw, _ := json.Marshal(protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", SessionID: "session", RequestID: "blocked", Operation: "status"})
	router.Handle(t.Context(), newRecordingSender(1), raw)
	<-reader.entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- router.Suspend(ctx, "session") }()
	select {
	case err := <-done:
		close(reader.release)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("reader join did not report deadline", err)
		}
	case <-time.After(time.Second):
		close(reader.release)
		<-done
		t.Fatal("reader join ignored lifecycle deadline")
	}
	router.mu.Lock()
	registration := router.runtimes["session"]
	router.mu.Unlock()
	if registration == nil || !registration.suspended {
		t.Fatal("timed-out join lost reader fence")
	}
}
