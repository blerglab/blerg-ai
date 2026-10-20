package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestRuntimeLaunchAuthority(t *testing.T) {
	api, hub, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000901"
	other := "00000000-0000-4000-8000-000000000902"
	for _, id := range []string{sid, other} {
		if err := db.InsertClusterSession(ctx, pool, id, host.ID, "starting", "/repos/fixture", "fixture", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	runtime, message, err := db.MintRuntimeLaunch(ctx, pool, sid, "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ValidateRuntimeToken(ctx, pool, message); err == nil {
		t.Fatal("messaging credential acquired runtime authority")
	}
	row, err := db.ValidateRuntimeToken(ctx, pool, runtime)
	if err != nil {
		t.Fatal(err)
	}
	ownDaemon, err := db.RuntimeDaemonID(ctx, pool, sid, row.ID)
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
	// A stolen pod capability cannot choose a host's name, mode, or active set.
	if err = conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: host.Name, Mode: "local", ReposRoot: "/host", Token: runtime, ActiveSessions: []string{sid, other}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for hub.GetDaemon(ownDaemon) == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	dc := hub.GetDaemon(ownDaemon)
	if dc == nil {
		t.Fatal("runtime not registered")
	}
	if dc.Name != "runner-"+sid || dc.Mode != "runner" || dc.BoundSessionID != sid || dc.RuntimeTokenID != row.ID {
		t.Fatal("caller controlled runtime identity")
	}
	if hub.GetDaemon(host.ID) != host {
		t.Fatal("pod displaced host daemon")
	}
	// A foreign event cannot change another session; the next own event proves
	// that the read pump has processed the preceding frame.
	if err = conn.WriteJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: other, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sid, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		s, e := db.GetSession(ctx, pool, sid)
		if e != nil {
			t.Fatal(e)
		}
		if s.Status == "idle" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	own, err := db.GetSession(ctx, pool, sid)
	if err != nil || own.Status != "idle" {
		t.Fatal("own session frame was not processed")
	}
	foreign, err := db.GetSession(ctx, pool, other)
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Status != "starting" {
		t.Fatal("foreign session mutated")
	}
	request := func(token, path string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.SetPathValue("id", strings.TrimPrefix(path, "/api/sessions/"))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.HandleGetAgentEvents(w, r)
		return w.Code
	}
	if got := request(runtime, "/api/sessions/"+sid); got != 200 {
		t.Fatalf("own transcript %d", got)
	}
	if got := request(runtime, "/api/sessions/"+other); got != 401 {
		t.Fatalf("foreign transcript %d", got)
	}
	// It is not a browser/admin or upload capability.
	r := httptest.NewRequest(http.MethodPost, "/api/agent-config", strings.NewReader("fixture"))
	r.Header.Set("Authorization", "Bearer "+runtime)
	w := httptest.NewRecorder()
	api.HandlePostAgentConfig(w, r)
	if w.Code != 401 {
		t.Fatalf("config mutation %d", w.Code)
	}
	// Heartbeats extend only current, unexpired tokens, never revoked authority.
	_, err = pool.Exec(ctx, `UPDATE board_tokens SET expires_at=now()+interval '1 hour' WHERE session_id=$1`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ValidateRuntimeBinding(ctx, pool, sid, row.ID, true); err != nil {
		t.Fatal(err)
	}
	mr, err := db.ValidateBoardToken(ctx, pool, message)
	if err != nil || time.Until(mr.ExpiresAt) < 23*time.Hour {
		t.Fatal("messaging token not extended")
	}
	// A duplicate spawn against a live Job cannot revoke current authority.
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.historyPool = pool
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: sid, Repo: "fixture"}); err == nil {
		t.Fatal("duplicate live launch allowed")
	}
	if _, err := db.ValidateRuntimeToken(ctx, pool, runtime); err != nil {
		t.Fatal("duplicate start revoked live runtime")
	}
	if len(f.createdSecrets) != 0 {
		t.Fatal("duplicate start wrote Secret")
	}
	next, nextMessage, err := db.MintRuntimeLaunch(ctx, pool, sid, "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if next == runtime || nextMessage == message {
		t.Fatal("launch credentials reused")
	}
	if _, err = db.ValidateRuntimeToken(ctx, pool, runtime); err == nil {
		t.Fatal("replaced runtime accepted")
	}
	if _, err = db.ValidateBoardToken(ctx, pool, message); err == nil {
		t.Fatal("replaced messaging accepted")
	}
	if got := request(runtime, "/api/sessions/"+sid); got != 401 {
		t.Fatal("old runtime retained HTTP authority")
	}
	// An existing socket is fenced too, even before its next heartbeat.
	// Revocation may close the socket before or during this write. In either
	// case require an actual peer close, not merely a read deadline below.
	_ = conn.WriteJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sid, Status: "running"})
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		if _, _, err = conn.ReadMessage(); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("revoked runtime socket did not close")
			}
			break
		}
	}
	s, err := db.GetSession(ctx, pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status == "running" {
		t.Fatal("replaced socket mutated session")
	}
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='stopped' WHERE id=$1`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ValidateRuntimeToken(ctx, pool, next); err == nil {
		t.Fatal("stopped runtime accepted")
	}
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='idle' WHERE id=$1`, sid)
	if err != nil {
		t.Fatal(err)
	}
	nextRow, err := db.ValidateRuntimeToken(ctx, pool, next)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE board_tokens SET expires_at=now()-interval '1 second' WHERE id=$1`, nextRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ValidateRuntimeBinding(ctx, pool, sid, nextRow.ID, true); err == nil {
		t.Fatal("heartbeat revived expired capability")
	}
	if _, err = db.ValidateBoardToken(ctx, pool, next); err == nil {
		t.Fatal("expired token renewed")
	}
	_, err = pool.Exec(ctx, `UPDATE board_tokens SET expires_at=now()+interval '1 hour',revoked_at=now() WHERE id=$1`, nextRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ValidateRuntimeBinding(ctx, pool, sid, nextRow.ID, true); err == nil {
		t.Fatal("heartbeat revived revoked capability")
	}
}

func TestRuntimeJobHasNoMasterOrForeignProvider(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "hermes"} {
		t.Run(engine, func(t *testing.T) {
			f := &fakeK8s{}
			jm := newTestJobManager(t, f)
			if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "fixture-session", Repo: "fixture", Engine: engine, ExtraEnv: map[string]string{"BLERG_RUNNER_DAEMON_TOKEN": "fixture-master", "ANTHROPIC_API_KEY": "fixture-anthropic", "CODEX_AUTH_JSON": "fixture-codex", "HERMES_ENV_CONTENTS": "fixture-hermes"}}); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(f.created)
			if strings.Contains(string(raw), "fixture-master") || strings.Contains(string(raw), "test-runtime-capability") {
				t.Fatal("literal authority in Job")
			}
			var data map[string]any
			for _, secret := range f.createdSecrets {
				if d, ok := secret["stringData"].(map[string]any); ok {
					data = d
				}
			}
			if data == nil {
				t.Fatal("missing launch Secret")
			}
			decode := func(k string) string { v, _ := data[k].(string); return v }
			if decode("BLERG_RUNNER_DAEMON_TOKEN") != "test-runtime-capability" {
				t.Fatal("master selected instead of scoped runtime")
			}
			for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CODEX_AUTH_JSON", "HERMES_ENV_CONTENTS"} {
				if foreignEngineCredential(key, engine) && strings.Contains(string(raw), `"name":"`+key+`"`) {
					t.Fatalf("foreign provider %s injected", key)
				}
			}
		})
	}
}
