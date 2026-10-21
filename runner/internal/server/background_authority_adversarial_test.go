package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBackgroundSupersededSocketStaysStaleAfterReplacementDisconnects(t *testing.T) {
	for _, lifetime := range []string{"00000000-0000-4000-8000-00000000da01", ""} {
		t.Run("lifetime="+lifetime, func(t *testing.T) {
			backgroundSupersededSocketStaysStale(t, lifetime)
		})
	}
}

func backgroundSupersededSocketStaysStale(t *testing.T, lifetime string) {
	t.Helper()
	fx := newLiveFx(t)
	old := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: lifetime, send: make(chan []byte, 8)}
	old.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(old)
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, old, protocol.AgentEvent{SessionID: fx.sess, ClientEventID: newUUID(), Kind: backgroundStartedKind, Payload: json.RawMessage(`{"task_id":"alive","call_id":"original"}`)})
	<-old.send
	replacement := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: "00000000-0000-4000-8000-00000000da02", send: make(chan []byte, 8)}
	replacement.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(replacement)
	if !fx.api.hub.UnregisterIfCurrent(replacement) {
		t.Fatal("replacement did not disconnect")
	}
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, old, protocol.AgentEvent{SessionID: fx.sess, ClientEventID: newUUID(), Kind: backgroundDoneKind, Payload: json.RawMessage(`{"task_id":"alive","call_id":"original"}`)})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	if row.BackgroundTasks != 1 || len(old.send) != 0 {
		t.Fatalf("superseded socket regained authority after new connection left: count=%d ack=%d", row.BackgroundTasks, len(old.send))
	}
}

func TestBackgroundClientCannotChooseOriginOrSyntheticTarget(t *testing.T) {
	raw, valid := backgroundTaskPayload(json.RawMessage(`{"task_id":"task","daemon_lifetime":"injected-old-process","start_event_id":"injected-target","call_id":"call"}`), "authenticated-process")
	var payload map[string]any
	if !valid || json.Unmarshal(raw, &payload) != nil {
		t.Fatal("valid background payload refused")
	}
	if payload["daemon_lifetime"] != "authenticated-process" || payload["start_event_id"] != nil || payload["call_id"] != "call" {
		t.Fatalf("untrusted origin or synthetic target survived sanitization: %+v", payload)
	}
	raw, valid = backgroundTaskPayload(json.RawMessage(`{"task_id":"task","daemon_lifetime":"injected-old-process","start_event_id":"injected-target"}`), "")
	payload = nil
	if !valid || json.Unmarshal(raw, &payload) != nil {
		t.Fatal("legacy background payload refused")
	}
	if payload["daemon_lifetime"] != nil || payload["start_event_id"] != nil {
		t.Fatalf("legacy client forged origin or synthetic target: %s", raw)
	}
	for _, origin := range []string{"", "authenticated-process"} {
		raw, valid = backgroundTaskPayload(json.RawMessage(`{"task_id":"task","DAEMON_LIFETIME":"injected-old-process","Start_Event_Id":"injected-target","ORIGIN_START_SEQ":999}`), origin)
		payload = nil
		if !valid || json.Unmarshal(raw, &payload) != nil {
			t.Fatal("valid background payload with reserved aliases refused")
		}
		for key, value := range payload {
			if strings.EqualFold(key, "start_event_id") || strings.EqualFold(key, "origin_start_seq") || (strings.EqualFold(key, "daemon_lifetime") && (key != "daemon_lifetime" || value != origin || origin == "")) {
				t.Fatalf("reserved alias bypassed provenance sanitization: %s", raw)
			}
		}
	}
}

func TestBackgroundReconcilePodInventoryCannotCloseSiblingSession(t *testing.T) {
	fx := newLiveFx(t)
	for _, sid := range []string{fx.sess, fx.privSess} {
		if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), fx.pool, sid, newUUID(), backgroundStartedKind, `{"task_id":"same-task","daemon_lifetime":"old-process"}`, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	lifetime := "00000000-0000-4000-8000-00000000da03"
	dc := &DaemonConn{ID: topicDaemonID, BoundSessionID: fx.sess, BackgroundTaskLifetime: lifetime, send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(dc)
	reconcileBackgroundTaskInventory(t.Context(), fx.api.hub, fx.pool, dc, &protocol.BackgroundTaskInventory{Lifetime: lifetime, Sessions: map[string][]string{fx.privSess: {}}})
	for sid, want := range map[string]int{fx.sess: 0, fx.privSess: 1} {
		row, err := db.GetSession(t.Context(), fx.pool, sid)
		if err != nil || row.BackgroundTasks != want {
			t.Fatalf("pod reconciliation escaped bound session: id=%s row=%+v err=%v want=%d", sid, row, err, want)
		}
	}
}

func TestBackgroundReconcileMalformedOrChangedLifetimeIsUnknown(t *testing.T) {
	fx := newLiveFx(t)
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), fx.pool, fx.sess, newUUID(), backgroundStartedKind, `{"task_id":"alive","daemon_lifetime":"old-process"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	lifetime := "00000000-0000-4000-8000-00000000da04"
	dc := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: lifetime, send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(dc)
	for _, inventory := range []*protocol.BackgroundTaskInventory{
		nil,
		{Lifetime: lifetime},
		{Lifetime: lifetime, Sessions: map[string][]string{fx.sess: nil}},
		{Lifetime: "00000000-0000-4000-8000-00000000da05", Sessions: map[string][]string{}},
	} {
		reconcileBackgroundTaskInventory(t.Context(), fx.api.hub, fx.pool, dc, inventory)
	}
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.BackgroundTasks != 1 {
		t.Fatalf("unknown inventory closed a task: row=%+v err=%v", row, err)
	}
}

func TestBackgroundDaemonCannotCompleteForeignDaemonTask(t *testing.T) {
	fx := newLiveFx(t)
	foreignDaemon, foreignSession := newUUID(), newUUID()
	if err := db.UpsertDaemon(t.Context(), fx.pool, foreignDaemon, "foreign-fixture", "local", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(t.Context(), fx.pool, foreignSession, foreignDaemon, "idle", "/fixture", "fixture", "Fixture", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), fx.pool, foreignSession, newUUID(), backgroundStartedKind, `{"task_id":"alive"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: "00000000-0000-4000-8000-00000000da06", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(dc)
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{SessionID: foreignSession, ClientEventID: newUUID(), Kind: backgroundDoneKind, Payload: json.RawMessage(`{"task_id":"alive"}`)})
	row, err := db.GetSession(t.Context(), fx.pool, foreignSession)
	if err != nil || row.BackgroundTasks != 1 {
		t.Fatalf("daemon completed foreign work: row=%+v err=%v", row, err)
	}
	var completions int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='background_task_done'`, foreignSession).Scan(&completions); err != nil || completions != 0 {
		t.Fatalf("foreign completion was persisted: count=%d err=%v", completions, err)
	}
}

func TestBackgroundStaleHeartbeatCannotStopReplacementSession(t *testing.T) {
	fx := newLiveFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=now()-interval '10 minutes' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	old := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: "00000000-0000-4000-8000-00000000da07", send: make(chan []byte, 8)}
	old.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(old)
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, old, protocol.AgentEvent{SessionID: fx.sess, ClientEventID: newUUID(), Kind: backgroundStartedKind, Payload: json.RawMessage(`{"task_id":"alive"}`)})
	<-old.send
	replacement := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: "00000000-0000-4000-8000-00000000da08", send: make(chan []byte, 8)}
	replacement.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	fx.api.hub.Register(replacement)
	raw := []byte(`{"type":"daemon_heartbeat","active_sessions":[],"background_task_inventory":{"lifetime":"00000000-0000-4000-8000-00000000da07","sessions":{}}}`)
	handleDaemonFrame(t.Context(), fx.api.hub, old, fx.pool, raw, envelope{Type: "daemon_heartbeat"})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.BackgroundTasks != 1 || row.Status != "running" {
		t.Fatalf("stale heartbeat stopped replacement session or cleared its task: row=%+v err=%v", row, err)
	}
}
