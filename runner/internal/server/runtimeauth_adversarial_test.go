package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestRuntimeCannotRebindProjectThroughOwnStartedFrame(t *testing.T) {
	api, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	const sid = "00000000-0000-4000-8000-000000000903"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "starting", "/repos/own", "own-project", "", ""); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "fixture-runtime", Mode: "runner", Token: runtime}); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(protocol.SessionStarted{Type: "session_started", SessionID: sid, Repo: "foreign-project", ProjectPath: "/repos/foreign"}); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sid, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	processed := false
	for time.Now().Before(deadline) {
		s, err := db.GetSession(ctx, pool, sid)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status == "idle" {
			processed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !processed {
		t.Fatal("runtime frames were not processed")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/agent/memories", strings.NewReader(`{"project":"foreign-project","name":"fixture","content":"unauthorized foreign write"}`))
	r.Header.Set("Authorization", "Bearer "+runtime)
	w := httptest.NewRecorder()
	api.HandlePostMemory(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("own session_started changed project authority: foreign memory write status=%d", w.Code)
	}
	s, err := db.GetSession(ctx, pool, sid)
	if err != nil || s.Repo != "own-project" || s.ProjectPath != "/repos/own" {
		t.Fatalf("untrusted frame replaced server project binding: %+v err=%v", s, err)
	}
}

func TestHostDaemonSelectorsNeverChooseSessionPods(t *testing.T) {
	hub := NewHub()
	pod := &DaemonConn{ID: "00000000-0000-4000-8000-000000000001", Mode: "runner", Name: "runner-fixture", RuntimeTokenID: "fixture-current-launch", BoundSessionID: "fixture-session"}
	pod.SetCheckedOutRepos([]string{"victim-repository"})
	pod.SetSandboxAvailable(true)
	hub.Register(pod)
	for name, chosen := range map[string]*DaemonConn{"repository": hub.DaemonForRepo("victim-repository"), "scratch": hub.DaemonForScratch("daemon"), "assist": hub.AnyDaemon()} {
		if chosen != nil {
			t.Errorf("%s selector granted pod another session's host role", name)
		}
	}
}

func TestRuntimeReplacementFencesOutboundWithoutOldInboundFrame(t *testing.T) {
	_, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	const sid = "00000000-0000-4000-8000-000000000904"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "starting", "/repos/own", "own-project", "", ""); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.ValidateRuntimeToken(ctx, pool, runtime)
	if err != nil {
		t.Fatal(err)
	}
	did, err := db.RuntimeDaemonID(ctx, pool, sid, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub.ServeDaemon(spawnTokenDaemonTok, pool))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: "fixture-runtime", Token: runtime}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for hub.GetDaemon(did) == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.GetDaemon(did) == nil {
		t.Fatal("runtime did not register")
	}
	hub.SetSessionOwner(sid, did)
	if _, _, err = db.MintRuntimeLaunch(ctx, pool, sid, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	const marker = "fixture-new-launch-private-user-input"
	forwardToSessionDaemon(hub, &BrowserConn{ID: "fixture-browser"}, sid, map[string]string{"type": "send_input", "session_id": sid, "data": marker})
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("revoked runtime socket did not close")
			}
			break
		}
		if strings.Contains(string(raw), marker) {
			t.Fatal("revoked socket received new private input before sending any further inbound frame")
		}
	}
}
