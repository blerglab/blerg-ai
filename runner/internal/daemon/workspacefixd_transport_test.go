package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestP6KillDispatchMustNotBlockAuthorityReplies(t *testing.T) {
	for _, duringSync := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "sync-awaiting-authority"}[duringSync], func(t *testing.T) { testKillAuthorityPump(t, duringSync) })
	}
}
func testKillAuthorityPump(t *testing.T, duringSync bool) {
	w, _, _, _ := localWorkspaceFixture(t)
	accepted := make(chan *websocket.Conn, 1)
	var peerWriteMu sync.Mutex
	hold := make(chan struct{})
	held := make(chan struct{}, 1)
	var holdNext atomic.Bool
	defer close(hold)
	replied := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		accepted <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req protocol.LocalWorkspaceControl
			if json.Unmarshal(raw, &req) != nil {
				return
			}
			if holdNext.Swap(false) {
				held <- struct{}{}
				select {
				case <-hold:
				case <-time.After(4 * time.Second):
					return
				}
			}
			peerWriteMu.Lock()
			err = conn.WriteJSON(protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", RequestID: req.RequestID, SessionID: req.SessionID, OK: true})
			peerWriteMu.Unlock()
			if err != nil {
				return
			}
			replied <- req.Operation
		}
	}))
	defer server.Close()
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	peer := <-accepted
	client := NewWSClient(Config{})
	client.conn = socket
	authority := NewWorkspaceAuthority(client)
	host, _, _ := newTestHost(t, &scriptedAgentProvider{}, "")
	host.Spawn(protocol.SpawnSession{SessionID: w.state.Session, Kind: "agent", Repo: "proj"})
	host.get(w.state.Session).localWorkspace = w
	host.cfg.Workspaces = &LocalWorkspaceService{Authority: authority, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}, CommitFinal: acknowledgeWorkspaceFinal}
	manager := &Manager{agents: host, client: client, config: ManagerConfig{ReposRootSetting: NewReposRootSetting(t.TempDir(), ""), Command: []string{"test-no-tmux"}}}
	handled := make(chan struct{})
	var handledOnce sync.Once
	client.OnMessage("kill_session", func(raw []byte) { manager.handleKillSession(raw); handledOnce.Do(func() { close(handled) }) })
	progressed := make(chan struct{}, 1)
	client.OnMessage("pump_probe", func([]byte) { progressed <- struct{}{} })
	done := make(chan error, 1)
	go func() { done <- client.readLoop(socket, func([]byte) {}) }()
	defer func() { socket.Close(); <-done }()
	l := protocol.SupervisorLaunch{SessionID: w.state.Session, LaunchGeneration: w.state.Launch.LaunchGeneration, Runtime: "sandbox", RuntimeGeneration: 1, ConnectionID: "connection"}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: 1}, Root: w.state.RootIdentity, ManifestDigest: w.state.Launch.Digest}
	w.authorize = func(ctx context.Context) error {
		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return authority.Current(short, l, binding)
	}
	if err = w.authorize(t.Context()); err != nil {
		t.Fatalf("positive authority transport: %v", err)
	}
	<-replied
	var syncDone chan error
	if duringSync {
		holdNext.Store(true)
		syncDone = make(chan error, 1)
		go func() { _, err := w.Sync(t.Context()); syncDone <- err }()
		select {
		case <-held:
		case <-time.After(time.Second):
			t.Fatal("sync never awaited authority")
		}
	}
	peerWriteMu.Lock()
	err = peer.WriteJSON(protocol.KillSession{Type: "kill_session", SessionID: w.state.Session})
	peerWriteMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	peerWriteMu.Lock()
	_ = peer.WriteJSON(protocol.KillSession{Type: "kill_session", SessionID: w.state.Session})
	_ = peer.WriteJSON(map[string]string{"type": "pump_probe"})
	peerWriteMu.Unlock()
	select {
	case <-progressed:
	case <-time.After(time.Second):
		t.Fatal("KILL_RPC: teardown blocked unrelated control frames")
	}
	if duringSync {
		hold <- struct{}{}
		select {
		case err := <-syncDone:
			if err != nil {
				t.Fatal("sync lost reply during kill", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("KILL_RPC: sync reply blocked behind kill")
		}
	}
	select {
	case <-replied:
	case <-time.After(time.Second):
		t.Fatal("stop never reached real authority transport")
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("kill handler did not finish")
	}
	deadline := time.Now().Add(time.Second)
	for {
		w.mu.Lock()
		stopped := w.state.Stopped
		w.mu.Unlock()
		if stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("KILL_RPC: authority reply was blocked by teardown")
		}
		time.Sleep(time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.state.Stopped {
		t.Fatalf("KILL_RPC: server answered Current immediately, but synchronous kill dispatch prevented receiving it; workspace stop was never executed")
	}
}
