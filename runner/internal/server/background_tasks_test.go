package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The session row counts the background tasks running from the background_task_* events as they
// are recorded (a re-sent event counts once), browsers are told the new count each time, the
// sessions list carries it while the session is live, and an end or a revive puts it back to 0.
// Requires TEST_DATABASE_URL.
func TestBackgroundTasksCountedOnIngest(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	daemonID := "00000000-0000-0000-0000-0000000000b1"
	sessionID := "00000000-0000-0000-0000-0000000000b2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "daemon", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle", "/w/proj", "proj", "T", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	hub := NewHub()
	browser := &BrowserConn{ID: "b1", send: make(chan []byte, 16)}
	hub.RegisterBrowser(browser)
	dc := &DaemonConn{ID: daemonID, send: make(chan []byte, 16)}
	ingest := func(id, kind string) {
		taskID := "t"
		if strings.HasSuffix(id, "000000000002") {
			taskID = "second"
		}
		HandleAgentEvent(ctx, hub, pool, dc, protocol.AgentEvent{
			Type: "agent_event", SessionID: sessionID, ClientEventID: id, Kind: kind,
			Payload: json.RawMessage(`{"task_id":"` + taskID + `","call_id":"c","kind":"bash"}`),
		})
	}
	count := func() int {
		t.Helper()
		row, err := db.GetSession(ctx, pool, sessionID)
		if err != nil || row == nil {
			t.Fatalf("GetSession: %v", err)
		}
		return row.BackgroundTasks
	}
	told := func() []int {
		var out []int
		for {
			select {
			case raw := <-browser.send:
				if !strings.Contains(string(raw), `"session_background_changed"`) {
					continue
				}
				var m protocol.SessionBackgroundChanged
				_ = json.Unmarshal(raw, &m)
				if m.SessionID != sessionID {
					t.Errorf("broadcast for %q", m.SessionID)
				}
				out = append(out, m.BackgroundTasks)
			default:
				return out
			}
		}
	}

	ingest("00000000-0000-4000-8000-000000000001", "background_task_started")
	ingest("00000000-0000-4000-8000-000000000002", "background_task_started") // a distinct second task
	ingest("00000000-0000-4000-8000-000000000002", "background_task_started") // re-sent
	ingest("00000000-0000-4000-8000-000000000003", "background_task_done")
	if n := count(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	if got := told(); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("browsers were told %v, want [1 2 1]", got)
	}
	row, _ := db.GetSession(ctx, pool, sessionID)
	if info := sessionRowToInfo(*row, ""); info.BackgroundTasks != 1 {
		t.Fatalf("live session's info carries %d, want 1", info.BackgroundTasks)
	}
	// Repeated completions of the first task leave the distinct second task running.
	ingest("00000000-0000-4000-8000-000000000004", "background_task_done")
	ingest("00000000-0000-4000-8000-000000000005", "background_task_done")
	if n := count(); n != 1 {
		t.Fatalf("duplicate completion changed live second task: count = %d, want 1", n)
	}

	// An end clears it.
	ingest("00000000-0000-4000-8000-000000000006", "background_task_started")
	now := time.Now()
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "stopped", &now); err != nil {
		t.Fatalf("UpdateSessionStatus: %v", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("after the end count = %d, want 0", n)
	}
	// So does a revive; and an ended session's info never carries a count.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET background_tasks = 3 WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetSession(ctx, pool, sessionID)
	if info := sessionRowToInfo(*row, ""); info.BackgroundTasks != 0 {
		t.Fatalf("ended session's info carries %d, want 0", info.BackgroundTasks)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "starting", "/w/proj", "proj", "T", ""); err != nil {
		t.Fatalf("InsertSession (revive): %v", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("after a revive count = %d, want 0", n)
	}
}
