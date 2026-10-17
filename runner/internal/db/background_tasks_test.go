package db_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
)

func TestBackgroundTaskAppendCountAtomicAndDuplicateTaskID(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	for i, id := range []string{"00000000-0000-4000-8000-00000000be01", "00000000-0000-4000-8000-00000000be02"} {
		_, fresh, count, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, id, "background_task_started", `{"task_id":"same","daemon_lifetime":"old"}`, time.Time{})
		if err != nil || !fresh || count != 1 {
			t.Fatalf("start%d fresh%v count%d err%v", i, fresh, count, err)
		}
	}
	_, fresh, count, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be02", "background_task_started", `{"task_id":"same","daemon_lifetime":"old"}`, time.Time{})
	if err != nil || fresh || count != 1 {
		t.Fatalf("duplicate fresh%v count%d err%v", fresh, count, err)
	}
	_, fresh, count, err = db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be03", "background_task_done", `{"task_id":"same","daemon_lifetime":"old"}`, time.Time{})
	if err != nil || !fresh || count != 0 {
		t.Fatalf("done fresh%v count%d err%v", fresh, count, err)
	}
}

func TestBackgroundTasksReconcileRestartsAndLiveInventory(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
	starts := []struct{ id, task, life string }{{"00000000-0000-4000-8000-00000000be11", "first", "first-life"}, {"00000000-0000-4000-8000-00000000be12", "second", "second-life"}, {"00000000-0000-4000-8000-00000000be13", "continuing", "first-life"}, {"00000000-0000-4000-8000-00000000be14", "current", "current-life"}}
	for _, start := range starts {
		payload := fmt.Sprintf(`{"task_id":%q,"call_id":%q,"daemon_lifetime":%q}`, start.task, start.task, start.life)
		if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, start.id, "background_task_started", payload, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	// An old peer's missing inventory or epoch never means no tasks.
	for _, tc := range []struct {
		life      string
		inventory map[string][]string
	}{{"", map[string][]string{}}, {"current-life", nil}} {
		changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, daemon, "", tc.life, tc.inventory)
		if err != nil || len(changes) != 0 {
			t.Fatalf("old peer changed state %+v %v", changes, err)
		}
	}
	changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, daemon, "", "current-life", map[string][]string{sid: {"continuing"}})
	if err != nil || len(changes) != 1 || len(changes[0].Events) != 3 || changes[0].Count != 2 {
		t.Fatalf("restart changes %+v %v", changes, err)
	}
	stopped, rebound := 0, 0
	for _, event := range changes[0].Events {
		if event.Kind == "background_task_started" {
			var p struct {
				TaskID      string `json:"task_id"`
				Lifetime    string `json:"daemon_lifetime"`
				StartedAt   string `json:"started_at"`
				OriginalSeq int64  `json:"origin_start_seq"`
			}
			if json.Unmarshal([]byte(event.Payload), &p) != nil || p.TaskID != "continuing" || p.Lifetime != "current-life" || p.StartedAt == "" || p.OriginalSeq != 3 {
				t.Fatalf("bad rebind %+v payload%+v", event, p)
			}
			rebound++
			continue
		}
		var p map[string]string
		if err := json.Unmarshal([]byte(event.Payload), &p); err != nil || p["start_event_id"] == "" || p["status"] != "stopped" || p["summary"] != "the daemon restarted" {
			t.Fatalf("stopped row %+v payload%v err%v", event, p, err)
		}
		stopped++
	}
	if stopped != 2 || rebound != 1 {
		t.Fatalf("stopped%d rebound%d", stopped, rebound)
	}

	again, err := db.ReconcileBackgroundTasks(t.Context(), pool, daemon, "", "current-life", map[string][]string{sid: {"continuing"}})
	if err != nil || len(again) != 0 {
		t.Fatalf("repeat %+v %v", again, err)
	}
	// A second restart closes both prior-lifetime tasks; repeating it produces no duplicate done.
	next, err := db.ReconcileBackgroundTasks(t.Context(), pool, daemon, "", "next-life", map[string][]string{})
	if err != nil || len(next) != 1 || len(next[0].Events) != 2 || next[0].Count != 0 {
		t.Fatalf("second restart %+v %v", next, err)
	}
	repeat, err := db.ReconcileBackgroundTasks(t.Context(), pool, daemon, "", "next-life", map[string][]string{})
	if err != nil || len(repeat) != 0 {
		t.Fatalf("second repeat %+v %v", repeat, err)
	}
	// Redelivered start event cannot resurrect its task after the synthetic completion.
	_, fresh, count, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, starts[0].id, "background_task_started", `{"task_id":"first","daemon_lifetime":"first-life"}`, time.Time{})
	if err != nil || fresh || count != 0 {
		t.Fatalf("start replay fresh%v count%d err%v", fresh, count, err)
	}
}

func TestBackgroundTaskReservedTargetRejected(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	_, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be21", "background_task_done", `{"task_id":"x","start_event_id":"forged"}`, time.Time{})
	if !errors.Is(err, db.ErrInvalidBackgroundTaskEvent) {
		t.Fatalf("reserved target accepted: %v", err)
	}
	rows, err := db.ListAgentEvents(t.Context(), pool, sid, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rejected event persisted %+v %v", rows, err)
	}
}

func TestBackgroundTaskAppendRejectsForeignDaemonUnderLock(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	_, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be31", "background_task_started", `{"task_id":"x"}`, time.Time{}, "00000000-0000-4000-8000-00000000beff")
	if !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatalf("foreign daemon accepted: %v", err)
	}
	rows, err := db.ListAgentEvents(t.Context(), pool, sid, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("foreign event persisted %+v %v", rows, err)
	}
	row, err := db.GetSession(t.Context(), pool, sid)
	if err != nil || row.BackgroundTasks != 0 {
		t.Fatalf("foreign writer changed count %+v %v", row, err)
	}
}

func TestBackgroundAgentReconcileClosesItsSubagentCard(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	startID := "00000000-0000-4000-8000-00000000be41"
	if _, _, err := db.AppendSubagentEventAt(t.Context(), pool, sid, startID, "subagent_started", `{"child_id":"child","summary":"Background work"}`, time.Time{}, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be42", "background_task_started", `{"task_id":"agent-task","call_id":"child","kind":"agent","daemon_lifetime":"old"}`, time.Time{}, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendSubagentEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be43", "subagent_done", `{"child_id":"child","background":true,"summary":"Still working"}`, time.Time{}, owner); err != nil {
		t.Fatal(err)
	}
	changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "new", map[string][]string{})
	if err != nil || len(changes) != 1 || len(changes[0].Events) != 2 || changes[0].Count != 0 {
		t.Fatalf("agent reconciliation %+v %v", changes, err)
	}
	done := changes[0].Events[1]
	var p map[string]any
	if json.Unmarshal([]byte(done.Payload), &p) != nil || done.Kind != "subagent_done" || p["child_id"] != "child" || p["start_event_id"] != startID || p["background"] != false {
		t.Fatalf("companion %+v payload%v", done, p)
	}
	rows, err := db.ListLiveEvents(t.Context(), pool, sid, done.Seq)
	if err != nil {
		t.Fatal(err)
	}
	var events []livestate.Event
	for _, row := range rows {
		events = append(events, livestate.Event{ID: row.ClientEventID, Seq: row.Seq, Kind: row.Kind, Payload: json.RawMessage(row.Payload), Ts: row.Ts})
	}
	state := livestate.Build(events, livestate.Options{Now: time.Now(), Status: "running"})
	if len(state.Tasks) != 0 || len(state.Subagents) != 0 {
		t.Fatalf("stopped Agent remained in Running Now: %+v", state)
	}
}

func TestBackgroundTaskReservedOriginRejected(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	_, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000be51", "background_task_started", `{"task_id":"x","origin_start_seq":1}`, time.Time{})
	if !errors.Is(err, db.ErrInvalidBackgroundTaskEvent) {
		t.Fatalf("reserved origin accepted: %v", err)
	}
}
