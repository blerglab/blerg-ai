package daemon

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func adversarialWorkspaceHost(t *testing.T) (*AgentHost, *agentTestSender, string) {
	t.Helper()
	host, sender, dir := newTestHost(t, &scriptedAgentProvider{}, "")
	workspaceFixture(t, dir, "README.md", "public fixture")
	host.Spawn(protocol.SpawnSession{SessionID: "workspace", Repo: "proj", Kind: "agent"})
	t.Cleanup(func() { host.Kill("workspace") })
	return host, sender, dir
}

func TestWorkspaceHostRejectsFolderRetargetedBeforeRead(t *testing.T) {
	host, sender, dir := adversarialWorkspaceHost(t)
	outside := t.TempDir()
	workspaceFixture(t, outside, "README.md", "private fixture")
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	workspaceLink(t, outside, dir)
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: "retarget", SessionID: "workspace", Path: "README.md"})
	if got := waitWorkspaceResult(t, sender, "retarget"); got.OK || got.ContentB64 != "" {
		t.Fatalf("read a different folder than the native launch: %+v", got)
	}
}

func TestWorkspaceWorkerRejectsLaunchEndedDuringRead(t *testing.T) {
	host, sender, _ := adversarialWorkspaceHost(t)
	binding := host.workspaceFileBinding("workspace")
	initialLive := binding.live
	var calls atomic.Int32
	binding.live = func() bool {
		if calls.Add(1) == 1 {
			return initialLive()
		}
		return false
	}
	queueWorkspaceFileRead(sender, protocol.ReadWorkspaceFile{RequestID: "stale", SessionID: "workspace", Path: "README.md"}, binding)
	got := waitWorkspaceResult(t, sender, "stale")
	if got.OK || got.ErrorCode != "unavailable" || got.ContentB64 != "" {
		t.Fatalf("stale launch bytes escaped: %+v", got)
	}
	if calls.Load() < 2 {
		t.Fatal("launch was not checked after disk access")
	}
}

type blockedWorkspaceResultSender struct {
	entered chan protocol.WorkspaceFileResult
	release chan struct{}
	done    chan struct{}
}

func (s *blockedWorkspaceResultSender) Send(message any) error {
	s.entered <- message.(protocol.WorkspaceFileResult)
	<-s.release
	s.done <- struct{}{}
	return nil
}

func TestWorkspaceWorkerBoundIncludesBlockedResultSend(t *testing.T) {
	host, _, _ := adversarialWorkspaceHost(t)
	waitUntil(t, "earlier workspace workers stopped", func() bool { return len(workspaceFileReads) == 0 })
	sender := &blockedWorkspaceResultSender{entered: make(chan protocol.WorkspaceFileResult, 4), release: make(chan struct{}), done: make(chan struct{}, 2)}
	var once sync.Once
	unblock := func() { once.Do(func() { close(sender.release) }) }
	t.Cleanup(func() {
		unblock()
		for range 2 {
			select {
			case <-sender.done:
			case <-time.After(15 * time.Second):
				t.Error("workspace worker did not stop")
			}
		}
		waitUntil(t, "workspace worker permits released", func() bool { return len(workspaceFileReads) == 0 })
	})
	binding := host.workspaceFileBinding("workspace")
	for _, id := range []string{"one", "two"} {
		queueWorkspaceFileRead(sender, protocol.ReadWorkspaceFile{RequestID: id, SessionID: "workspace", Path: "README.md"}, binding)
		select {
		case got := <-sender.entered:
			if !got.OK {
				t.Fatalf("initial worker failed: %+v", got)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("workspace worker did not reach send")
		}
	}
	start := time.Now()
	for range 20 {
		queueWorkspaceFileRead(sender, protocol.ReadWorkspaceFile{RequestID: "busy", SessionID: "workspace", Path: "README.md"}, binding)
	}
	if time.Since(start) > time.Second {
		t.Fatal("saturation blocked the daemon command path")
	}
	select {
	case got := <-sender.entered:
		t.Fatalf("more than two large results in flight: %+v", got)
	case <-time.After(10 * time.Millisecond):
	}
	unblock()
}
