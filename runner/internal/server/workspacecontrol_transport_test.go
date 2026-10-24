package server

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

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// Exercise the production P2 adapter, real WebSocket serialization and durable
// server/DB operations together. The peer authenticates a fixture credential;
// the full ServeDaemon/Core credential suite separately covers enrollment.
func TestLocalWorkspaceTransportLostCompletionReceipt(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "transport-desktop")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2,runtime='docker' WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: topicOwner, LaunchGeneration: newUUID(), Digest: "transport-digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "members/one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.sess, m); err != nil {
		t.Fatal(err)
	}
	var lost atomic.Bool
	connected := make(chan struct{}, 8)
	var peers sync.WaitGroup
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		peers.Add(1)
		defer peers.Done()
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		var hello protocol.DaemonHello
		if err != nil || json.Unmarshal(raw, &hello) != nil || hello.Token != "fixture-desktop-credential" || hello.DesktopID != dc.ID {
			return
		}
		err = db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
			dc.DesktopGeneration = newUUID()
			return db.BindDesktopConnection(ctx, f.pool, *dc.DesktopBinding, dc.Name, dc.Mode, dc.ReposRoot, dc.DesktopGeneration)
		})
		if err != nil {
			return
		}
		connected <- struct{}{}
		for {
			_, raw, err = conn.ReadMessage()
			if err != nil {
				return
			}
			var req protocol.LocalWorkspaceControl
			if json.Unmarshal(raw, &req) != nil || req.Type != "local_workspace_control" {
				continue
			}
			var response protocol.LocalWorkspaceControlResult
			err = db.WithDesktopFence(t.Context(), f.pool, dc.ID, func(ctx context.Context) error {
				response = f.api.hub.localWorkspaceControl(ctx, f.pool, dc, req)
				return nil
			})
			if err != nil {
				return
			}
			if req.Operation == "final" && response.OK && lost.CompareAndSwap(false, true) {
				return
			}
			if conn.WriteJSON(response) != nil {
				return
			}
		}
	}))
	ctx, cancel := context.WithCancel(t.Context())
	client := daemon.NewWSClient(daemon.Config{ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http"), DaemonName: dc.Name, DesktopID: dc.ID, DaemonToken: "fixture-desktop-credential", ReconnectInitial: time.Millisecond, ReconnectMax: 5 * time.Millisecond, HeartbeatInterval: time.Hour})
	authority := daemon.NewWorkspaceAuthority(client)
	done := make(chan struct{})
	go func() { client.Run(ctx, func() []string { return []string{f.sess} }); close(done) }()
	defer func() { cancel(); srv.Close(); <-done; peers.Wait() }()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture link unavailable")
	}
	launch, err := authority.Launch(t.Context(), f.sess, m, newUUID())
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: launch.RuntimeGeneration}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 9}}
	if err = authority.Bind(t.Context(), launch, binding); err != nil {
		t.Fatal(err)
	}
	final := protocol.WorkspaceFinalization{Binding: binding, Message: protocol.SessionEnded{Type: "session_ended", SessionID: f.sess}, Results: []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Pending: true, SyncError: "retained fixture"}}}
	short, stop := context.WithTimeout(t.Context(), 300*time.Millisecond)
	_, err = authority.CommitFinal(short, launch, final)
	stop()
	if err == nil || !lost.Load() {
		t.Fatal("lost receipt was acknowledged")
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect unavailable")
	}
	receipt, err := authority.CommitFinal(t.Context(), launch, final)
	if err != nil || receipt.Digest != final.Digest() {
		t.Fatalf("durable reconciliation: %v", err)
	}
	if err = authority.Current(t.Context(), launch, binding); err == nil {
		t.Fatal("ordinary Current accepted ended session")
	}
	final.Message.ExitCode = 1
	if _, err = authority.CommitFinal(t.Context(), launch, final); err == nil {
		t.Fatal("conflicting reconnect outcome accepted")
	}
}
