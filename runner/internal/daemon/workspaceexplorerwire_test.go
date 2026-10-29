package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type fakeExplorerRuntime struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (f *fakeExplorerRuntime) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	close(f.stopped)
	return explorerFailure(req, "cancelled")
}
func TestWorkspaceExplorerRuntimeCancellation(t *testing.T) {
	var router WorkspaceExplorerRouter
	fake := &fakeExplorerRuntime{started: make(chan struct{}), stopped: make(chan struct{})}
	router.Bind("session", fake)
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "request", SessionID: "session", Operation: "search"}
	raw, _ := json.Marshal(req)
	router.Handle(t.Context(), nil, raw)
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("runtime not dispatched")
	}
	req.Type = "cancel_workspace_explorer"
	req.SessionID = "foreign"
	raw, _ = json.Marshal(req)
	router.Handle(t.Context(), nil, raw)
	select {
	case <-fake.stopped:
		t.Fatal("foreign session cancelled request")
	default:
	}
	joined := make(chan struct{})
	go func() { router.Unbind("session"); close(joined) }()
	select {
	case <-fake.stopped:
	case <-time.After(time.Second):
		t.Fatal("runtime not cancelled before stop/freeze")
	}
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("runtime reader not joined")
	}
}

type deadlineExplorerSender struct{ *recordingSender }

func (s deadlineExplorerSender) SendWorkspaceExplorerResultContext(ctx context.Context, res protocol.WorkspaceExplorerResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Send(res)
}

func TestWorkspaceExplorerWorkerDeadlineRepliesAfterJoining(t *testing.T) {
	var router WorkspaceExplorerRouter
	defer router.Close()
	fake := &fakeExplorerRuntime{started: make(chan struct{}), stopped: make(chan struct{})}
	router.Bind("session", fake)
	sender := deadlineExplorerSender{newRecordingSender(1)}
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "deadline", SessionID: "session", Operation: "status"}
	raw, _ := json.Marshal(req)
	router.Handle(t.Context(), sender, raw)
	select {
	case <-fake.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not observe its existing two-second budget")
	}
	select {
	case value := <-sender.ch:
		res, ok := value.(protocol.WorkspaceExplorerResult)
		if !ok || res.OK || res.ErrorCode != "cancelled" || res.RequestID != req.RequestID {
			t.Fatalf("deadline did not produce an explicit refusal: %+v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("joined worker's deadline refusal was dropped; helper client would wait for its ten-second pipe deadline")
	}
}

func TestWorkspaceExplorerManagerRefusesLocalSandboxBackend(t *testing.T) {
	local, _, _ := explorerFixture(t)
	local.manifest.Runtime = "sandbox"
	manager := &Manager{}
	manager.BindWorkspaceExplorer("session", local)
	manager.explorer.mu.Lock()
	registered := manager.explorer.runtimes["session"]
	manager.explorer.mu.Unlock()
	if registered != nil {
		t.Fatal("host daemon registered a local sandbox-clone reader")
	}
}
