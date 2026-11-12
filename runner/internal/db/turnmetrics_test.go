package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"strings"
	"testing"
	"time"
)

func TestSessionTurnsUnknownSession(t *testing.T) {
	pool := connect(t)
	turns, err := db.SessionTurns(context.Background(), pool, "00000000-0000-4000-8000-000000000001", time.Now())
	if err != nil || len(turns) != 0 {
		t.Fatalf("turns=%+v err=%v", turns, err)
	}
}

func TestSessionTurnsStoreCountersWithoutPayloads(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	for i, e := range []struct {
		sec           int
		kind, payload string
	}{
		{0, "status_changed", `{"status":"running"}`},
		{2, "tool_call", `{"tool":"shell","call_id":"a","input":{"cmd":"go test ./... # private-payload"}}`},
		{8, "tool_result", `{"call_id":"a","output":"private-output"}`},
		{10, "turn_done", `{"stop_reason":"error","usage":{"input_tokens":17,"output_tokens":4}}`},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,ts,kind,payload) VALUES($1,$2,$3,$4,$5,$6)`, sid, i+1, newUUID(t, pool), at.Add(time.Duration(e.sec)*time.Second), e.kind, []byte(e.payload))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Launch time precedes the fixture's synthetic turn.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2 WHERE id=$1`, sid, at); err != nil {
		t.Fatal(err)
	}
	turns, err := db.SessionTurns(ctx, pool, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Timing.Seconds["tests"] != 6 || turns[0].Tokens.Input != 17 || turns[0].Outcome != "failed" {
		t.Fatalf("turns %+v", turns)
	}
	for range 2 {
		if err = db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var raw string
	if err = pool.QueryRow(ctx, `SELECT count(*),min(metrics::text) FROM session_turn_metrics WHERE session_id=$1`, sid).Scan(&count, &raw); err != nil {
		t.Fatal(err)
	}
	if count != 1 || strings.Contains(raw, "private-") {
		t.Fatalf("stored %d: %s", count, raw)
	}
}

func TestMetricRunHeartbeatPreservesProgressAndStart(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	run := activity.Run{ID: "external-gate", Suite: "runner-unit", Commit: "abcdef1", State: "running", Progress: 2, StartedAt: time.Now().UTC().Add(-time.Hour)}
	if err := db.RegisterMetricRun(ctx, pool, sid, run); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspace_metric_runs SET data=jsonb_set(data,'{progress_at}',to_jsonb('2026-01-01T00:00:00Z'::text)) WHERE session_id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	run.StartedAt = time.Now().UTC()
	if err := db.RegisterMetricRun(ctx, pool, sid, run); err != nil {
		t.Fatal(err)
	}
	metrics, err := db.WorkspaceMetrics(ctx, pool, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Runs) != 1 || metrics.Runs[0].ProgressAt.Year() != 2026 || metrics.Runs[0].ProgressAt.Month() != 1 || metrics.Runs[0].StartedAt.After(time.Now().Add(-30*time.Minute)) {
		t.Fatalf("runs: %+v", metrics.Runs)
	}
	run.Progress++
	if err := db.RegisterMetricRun(ctx, pool, sid, run); err != nil {
		t.Fatal(err)
	}
	metrics, err = db.WorkspaceMetrics(ctx, pool, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Runs[0].ProgressAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatal("progress did not advance")
	}
}

func TestMetricRunProgressCannotMoveBackward(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	run := activity.Run{ID: "gate", Suite: "runner-unit", Commit: "abcdef1", State: "running", Progress: 10}
	if err := db.RegisterMetricRun(ctx, pool, sid, run); err != nil {
		t.Fatal(err)
	}
	run.Progress = 2
	if err := db.RegisterMetricRun(ctx, pool, sid, run); err != nil {
		t.Fatal(err)
	}
	metrics, err := db.WorkspaceMetrics(ctx, pool, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Runs[0].Progress != 10 {
		t.Fatalf("progress moved backward: %+v", metrics.Runs[0])
	}
}

func TestStoredTurnReadsDoNotReplayHistory(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	at := time.Now().UTC().Add(-time.Minute)
	stored := activity.Turn{Seq: 7, StartedAt: at, EndedAt: at.Add(10 * time.Second), DurationSeconds: 10, Outcome: "completed", Timing: activity.Timing{Seconds: map[string]float64{"tests": 7, "model": 3}, Phases: map[string]float64{"gates": 7}}, ToolCounts: map[string]int{"tests": 1}}
	raw, _ := json.Marshal(stored)
	if _, err := pool.Exec(ctx, `INSERT INTO session_turn_metrics(session_id,turn_seq,metrics) VALUES($1,7,$2)`, sid, raw); err != nil {
		t.Fatal(err)
	}
	got, err := db.SessionTurns(ctx, pool, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Seq != 7 || got[0].Timing.Phases["gates"] != 7 {
		t.Fatalf("stored row ignored: %+v", got)
	}
}

func TestSavingTurnDoesNotRewriteEarlierRows(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	if _, err := pool.Exec(ctx, `CREATE TABLE metric_updates(n int);CREATE FUNCTION count_metric_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO metric_updates VALUES(1); RETURN NEW; END; $$; CREATE TRIGGER count_metric_update AFTER UPDATE ON session_turn_metrics FOR EACH ROW EXECUTE FUNCTION count_metric_update()`); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"status_changed", "turn_done"} {
		payload := `{"status":"running"}`
		if i == 1 {
			payload = `{"stop_reason":"end_turn"}`
		}
		if _, _, err := db.AppendAgentEvent(ctx, pool, sid, newUUID(t, pool), kind, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
		t.Fatal(err)
	}
	var updates int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metric_updates`).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("rewrote immutable turn %d times", updates)
	}
}

func TestMetricRunRegistrationCapAndLease(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	for i := 0; i < 32; i++ {
		if err := db.RegisterMetricRun(ctx, pool, sid, activity.Run{ID: fmt.Sprintf("run-%d", i), Suite: "unit", Commit: "abc", State: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RegisterMetricRun(ctx, pool, sid, activity.Run{ID: "overflow", Suite: "unit", Commit: "abc", State: "running"}); err == nil {
		t.Fatal("unbounded registration")
	}
	if _, err := pool.Exec(ctx, `UPDATE workspace_metric_runs SET updated_at=now()-interval '2 hours' WHERE session_id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	w, err := db.WorkspaceMetrics(ctx, pool, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range w.Runs {
		if r.State == "running" {
			t.Fatal("abandoned run remains running")
		}
	}
}

func TestWorkspaceMetricsUsesStoredRowsForLargeTree(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,started_at,spawning_account_id,parent_session_id,kind,project_path,repo) SELECT gen_random_uuid(),s.daemon_id,'running',s.started_at,s.spawning_account_id,s.id,'agent',s.project_path,s.repo FROM sessions s CROSS JOIN generate_series(1,500) WHERE s.id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_turn_metrics(session_id,turn_seq,metrics) SELECT id,2,'{"turn_seq":2,"duration_seconds":100,"timing":{"phases":{"gates":70}}}'::jsonb FROM sessions WHERE parent_session_id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	w, err := db.WorkspaceMetrics(budget, pool, sid, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Workers) != 501 {
		t.Fatalf("workers=%d", len(w.Workers))
	}
	for _, v := range w.Workers {
		if v.SessionID != sid && (v.TurnCount != 1 || v.GateFraction != .7) {
			t.Fatalf("worker=%+v", v)
		}
	}
	t.Logf("500 stored descendants: %s", time.Since(start))
}

func TestTurnMetricMigrationAfterAnEarlierNumber(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET filename='117_turn_metrics.sql' WHERE filename='118_turn_metrics.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var current bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename='118_turn_metrics.sql')`).Scan(&current); err != nil || !current {
		t.Fatalf("migration not recorded: %v", err)
	}
}

func TestStoredTurnReadIsNotLimitedToRecentDetail(t *testing.T) {
	pool := connect(t)
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	if _, err := pool.Exec(t.Context(), `INSERT INTO session_turn_metrics(session_id,turn_seq,metrics) SELECT $1,n,jsonb_build_object('turn_seq',n,'duration_seconds',10) FROM generate_series(1,150) n`, sid); err != nil {
		t.Fatal(err)
	}
	turn, err := db.StoredTurnMetric(t.Context(), pool, sid, 1)
	if err != nil || turn.Seq != 1 || turn.DurationSeconds != 10 {
		t.Fatalf("old turn: %+v %v", turn, err)
	}
}

func TestIdleTurnIsFinalizedOnceByLateDone(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	at := time.Now().Add(-time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2 WHERE id=$1`, sid, at); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{`{"status":"running"}`, `{"status":"idle"}`} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,ts,kind,payload) VALUES($1,$2,gen_random_uuid(),$3,'status_changed',$4)`, sid, i+1, at.Add(time.Duration(i)*4*time.Second), []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,ts,kind,payload) VALUES($1,3,gen_random_uuid(),$2,'turn_done','{"stop_reason":"interrupted","usage":{"input_tokens":17}}')`, sid, at.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
		t.Fatal(err)
	}
	turns, err := db.SessionTurns(ctx, pool, sid, time.Now())
	if err != nil || len(turns) != 1 || turns[0].Seq != 3 || turns[0].DurationSeconds != 4 || turns[0].Outcome != "stopped" || turns[0].Tokens == nil || turns[0].Tokens.Input != 17 {
		t.Fatalf("finalized turn: %+v %v", turns, err)
	}
}

func TestSavingATurnRecoversTheTurnBeforeItThatWasMissed(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	at := time.Now().Add(-time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2 WHERE id=$1`, sid, at); err != nil {
		t.Fatal(err)
	}
	seq := 0
	turn := func() {
		t.Helper()
		for _, e := range [][2]string{{"status_changed", `{"status":"running"}`}, {"turn_done", `{"stop_reason":"end_turn"}`}} {
			seq++
			if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,ts,kind,payload) VALUES($1,$2,gen_random_uuid(),$3,$4,$5)`, sid, seq, at.Add(time.Duration(seq)*time.Minute), e[0], []byte(e[1])); err != nil {
				t.Fatal(err)
			}
		}
	}
	turn()
	if err := db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn() // This turn's save never ran.
	turn()
	if err := db.SaveTurnMetrics(ctx, pool, sid, time.Now()); err != nil {
		t.Fatal(err)
	}
	var stored []int64
	rows, err := pool.Query(ctx, `SELECT turn_seq FROM session_turn_metrics WHERE session_id=$1 ORDER BY turn_seq`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, n)
	}
	if fmt.Sprint(stored) != "[2 4 6]" {
		t.Fatalf("stored turns %v", stored)
	}
}

func TestTranscriptTailSkipsProcessTelemetry(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	for i, kind := range []string{"assistant_text", "assistant_text", "process_activity", "process_activity", "process_activity"} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,ts,kind,payload) VALUES($1,$2,gen_random_uuid(),now(),$3,'{}')`, sid, i+1, kind); err != nil {
			t.Fatal(err)
		}
	}
	rows, more, err := db.ListAgentEventsBefore(ctx, pool, sid, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("tail %+v more %v", rows, more)
	}
	forward, err := db.ListAgentEvents(ctx, pool, sid, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward) != 5 {
		t.Fatalf("forward replay must stay contiguous: %d rows", len(forward))
	}
}
