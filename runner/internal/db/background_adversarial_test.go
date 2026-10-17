package db_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestBackgroundReconcileConcurrentCompletionAndRepeatedRestart(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000ca01", "background_task_started", `{"task_id":"old","call_id":"original","daemon_lifetime":"old-process"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000ca02", "background_task_done", `{"task_id":"old","call_id":"original","daemon_lifetime":"old-process"}`, time.Time{})
		errs <- err
	})
	wg.Go(func() {
		_, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "new-process", map[string][]string{})
		errs <- err
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, lifetime := range []string{"new-process", "third-process"} {
		changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", lifetime, map[string][]string{})
		if err != nil || len(changes) != 0 {
			t.Fatalf("already closed task reconciled again: %+v %v", changes, err)
		}
	}
	var count, synthetic int
	if err := pool.QueryRow(t.Context(), `SELECT background_tasks FROM sessions WHERE id=$1`, sid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='background_task_done' AND payload ? 'start_event_id'`, sid).Scan(&synthetic); err != nil {
		t.Fatal(err)
	}
	if count != 0 || synthetic > 1 {
		t.Fatalf("racing completion diverged: count=%d synthetic=%d", count, synthetic)
	}
}

func TestBackgroundReconcileStaleEmptyInventoryPreservesCurrentLifetime(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	for i, lifetime := range []string{"current-process", "old-process"} {
		payload := fmt.Sprintf(`{"task_id":"task-%d","daemon_lifetime":%q}`, i, lifetime)
		if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), "background_task_started", payload, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "current-process", map[string][]string{})
	if err != nil || len(changes) != 1 || changes[0].Count != 1 || len(changes[0].Events) != 1 {
		t.Fatalf("stale inventory closed new task or missed old one: %+v %v", changes, err)
	}
}

func TestBackgroundAtomicMutationsRollBackWhenCounterWriteFails(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cb01", "background_task_started", `{"task_id":"live"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_background_count() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture counter failure'; END $$;
CREATE TRIGGER reject_background_count BEFORE UPDATE OF background_tasks ON sessions FOR EACH ROW EXECUTE FUNCTION reject_background_count()`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cb02", "background_task_done", `{"task_id":"live"}`, time.Time{}); err == nil {
		t.Fatal("counter failure accepted background completion")
	}
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "new-process", map[string][]string{}); err == nil {
		t.Fatal("counter failure accepted synthetic orphan completion")
	}
	var events, count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1`, sid).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT background_tasks FROM sessions WHERE id=$1`, sid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if events != 1 || count != 1 {
		t.Fatalf("failed transaction left partial state: events=%d count=%d", events, count)
	}
}

func TestBackgroundOrphanCannotFinishNewForegroundChild(t *testing.T) {
	backgroundOrphanCannotFinishNewForegroundChild(t, false)
}

func TestBackgroundReboundOrphanCannotFinishNewForegroundChild(t *testing.T) {
	backgroundOrphanCannotFinishNewForegroundChild(t, true)
}

func backgroundOrphanCannotFinishNewForegroundChild(t *testing.T, rebind bool) {
	t.Helper()
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendSubagentEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cc01", "subagent_started", `{"child_id":"reused-child","summary":"Old child"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cc02", "background_task_started", `{"task_id":"native-agent-task","call_id":"reused-child","kind":"agent","daemon_lifetime":"old-process"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendSubagentEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cc03", "subagent_started", `{"child_id":"reused-child","summary":"New foreground child"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if rebind {
		if _, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "intermediate-process", map[string][]string{sid: {"native-agent-task"}}); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "new-process", map[string][]string{})
	if err != nil || len(changes) != 1 || len(changes[0].Events) != 1 || changes[0].Events[0].Kind != "background_task_done" {
		t.Fatalf("old background task ended a newer foreground child: %+v %v", changes, err)
	}
	var completions int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='subagent_done'`, sid).Scan(&completions); err != nil || completions != 0 {
		t.Fatalf("new foreground child received orphan completion: count=%d err=%v", completions, err)
	}
}

func TestBackgroundContinuingTaskCompletesUnderReplacementLifetime(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cd01", "background_task_started", `{"task_id":"continuing","call_id":"original","daemon_lifetime":"old-process"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "replacement-process", map[string][]string{sid: {"continuing"}}); err != nil {
		t.Fatal(err)
	}
	_, _, count, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000cd02", "background_task_done", `{"task_id":"continuing","call_id":"original","daemon_lifetime":"replacement-process","status":"completed"}`, time.Time{})
	if err != nil || count != 0 {
		t.Fatalf("replacement could not report genuine completion: count=%d err=%v", count, err)
	}
	changes, err := db.ReconcileBackgroundTasks(t.Context(), pool, owner, "", "replacement-process", map[string][]string{})
	if err != nil || len(changes) != 0 {
		t.Fatalf("genuine completion later mislabeled as stopped: %+v %v", changes, err)
	}
}
