//go:build linux || darwin

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// Keep host runtime state across a lost server Hub and a second mid-turn
// connection. Control calls and Files use the shipped client and read pump;
// the host reader traverses a real directory. No engine process is needed.
func TestWorkspaceRestartWireHostTurn(t *testing.T) {
	f := newTopicFx(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, f.sess); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "host", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "fixture", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	token := "bdt_" + strings.Repeat("a", 64)
	b := desktopauth.Binding{DesktopID: topicDaemonID, OwnerID: topicOwner, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	desktopFakeCore(t, f.api, map[string]desktopauth.Binding{token: b})
	var handlerHub atomic.Value
	handlerHub.Store(f.api.hub)
	var handler atomic.Value
	handler.Store(f.api.hub.ServeDaemon(topicDaemonToken, f.pool))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.Load().(http.HandlerFunc)(w, r) }))
	defer srv.Close()
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http"), DaemonToken: token, DesktopID: topicDaemonID, DaemonMode: "local", DaemonName: "wire-host", HeartbeatInterval: time.Hour, ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond})
	authority := daemon.NewWorkspaceAuthority(client)
	connected := make(chan struct{}, 4)
	client.SetOnConnect(func() { connected <- struct{}{} })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx, func() []string { return []string{f.sess} }) }()
	defer func() {
		if dc := handlerHub.Load(); dc != nil {
			hub := dc.(*Hub)
			if peer := hub.GetDaemon(topicDaemonID); peer != nil {
				hub.UnregisterIfCurrent(peer)
			}
		}
		cancel()
		<-done
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("connect")
	}
	launch, err := authority.Launch(t.Context(), f.sess, m, newUUID())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("retained host bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := shellruntime.ResolvePersistentIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: launch.RuntimeGeneration}, ManifestDigest: m.Digest, Root: identity.MemberRoot}
	if err = authority.Bind(t.Context(), launch, binding); err != nil {
		t.Fatal(err)
	}
	reader, err := daemon.NewLocalExplorer(root, m, binding, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	client.OnMessage("workspace_explorer", func(raw []byte) {
		var req protocol.WorkspaceExplorerRequest
		if json.Unmarshal(raw, &req) != nil {
			return
		}
		go func() {
			if e := authority.Current(ctx, launch, binding); e != nil {
				return
			}
			_ = client.Send(reader.Explore(ctx, req))
		}()
	})
	// Lose all process-local authority, while retaining host runtime state.
	oldHub := f.api.hub
	fresh := NewHub()
	fresh.desktopPool = f.pool
	fresh.desktopAuthenticate = f.api.authenticateDesktop
	handlerHub.Store(fresh)
	handler.Store(fresh.ServeDaemon(topicDaemonToken, f.pool))
	oldConnection := oldHub.GetDaemon(topicDaemonID)
	oldHub.UnregisterIfCurrent(oldConnection) // process loss cannot run old cleanup against the new server
	oldConnection.desktopClose()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("restart reconnect")
	}
	// Recovery must reject even an authenticated desktop's changed physical root.
	changedRoot := binding
	changedRoot.Root.Inode++
	if _, err = authority.Recover(t.Context(), launch, changedRoot); err == nil {
		t.Fatal("reconnect authorized a changed root")
	}
	// Recovery can still be queued behind the live writer's lifecycle lock.
	// The turn-end authority check must recover the exact persisted binding.
	if err = authority.Current(t.Context(), launch, binding); err != nil {
		t.Fatalf("turn end after server restart: %v", err)
	}
	fresh.GetDaemon(topicDaemonID).desktopClose()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("mid-turn reconnect")
	}
	if err = authority.Current(t.Context(), launch, binding); err != nil {
		t.Fatalf("turn end after mid-turn reconnect: %v", err)
	}
	if err = authority.Results(t.Context(), f.sess, binding, []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Pending: true}}); err != nil {
		t.Fatalf("turn results: %v", err)
	}
	copyAPI := NewAPI(fresh, f.pool, topicDaemonToken, nil, "")
	copyAPI.coreAuth = f.api.coreAuth
	copyAPI.coreURL = f.api.coreURL
	copyAPI.coreInternalKey = f.api.coreInternalKey
	files := http.NewServeMux()
	copyAPI.RegisterWorkspaceFileRoutes(files)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+f.sess+"/workspace?op=list&path=", nil)
	req.Header.Set("Authorization", "Bearer "+f.token(topicOwner, "human"))
	response := httptest.NewRecorder()
	files.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: response}, req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "README.md") {
		t.Fatalf("Files after reconnect: %d %s", response.Code, response.Body.String())
	}
	changed := binding
	changed.Root.Inode++
	if err = authority.Current(t.Context(), launch, changed); err == nil {
		t.Fatal("changed root was authorized after reconnect")
	}
	persistedGeneration, persistedBinding, err := db.GetWorkspaceRuntime(t.Context(), f.pool, f.sess)
	if err != nil || persistedGeneration != binding.Generation || persistedBinding == nil || *persistedBinding != binding {
		t.Fatal("recovery changed the runtime incarnation", err)
	}
	payload, _ := json.Marshal(map[string]string{"status": "idle"})
	if err = client.Send(protocol.AgentEvent{Type: "agent_event", SessionID: f.sess, ClientEventID: newUUID(), Kind: "status_changed", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err = authority.Current(t.Context(), launch, binding); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), f.pool, f.sess)
	if err != nil || row == nil || row.Status != "idle" {
		t.Fatalf("turn-end status: row=%+v err=%v", row, err)
	}
}
