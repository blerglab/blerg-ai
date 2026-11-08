package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInsightBuildingReworkUsesOnlyOwnedEarlierCardEvidence(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "stage-owner")
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "stage-owner", ParentSessionID: parent})
	foreign := mustSession(t, pool, daemon, "foreign-stage-owner")
	// Ten minutes past an hour, 50-110 minutes ago: the worker's 60 s never straddles an hourly
	// bucket (from now-1h it split 37 s / 23 s across two hours at hh:59 and failed).
	start := time.Now().UTC().Truncate(time.Hour).Add(-50 * time.Minute)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+interval '60 seconds' WHERE id=$1`, worker, start); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO session_card_links VALUES($1,'board-a',42,'pin',$2)`, parent, start); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(t.Context(), pool, worker, newUUID(t, pool), "status_changed", `{"status":"running"}`, start); err != nil {
		t.Fatal(err)
	}
	mark := `{"kind":"review","board_id":"board-a","card":42,"verdict":"changes","at":"` + start.Add(-time.Second).Format(time.RFC3339) + `"}`
	if _, _, err := db.AppendAgentEventAt(t.Context(), pool, foreign, newUUID(t, pool), "mark", mark, start.Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	read := func() activity.Timing {
		t.Helper()
		if err := db.RefreshInsightTiming(t.Context(), pool, worker); err != nil {
			t.Fatal(err)
		}
		raw, err := db.InsightActivityTimings(t.Context(), pool, "stage-owner", []string{worker})
		var d struct {
			Sessions []struct{ Timing activity.Timing }
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &d); err != nil || len(d.Sessions) != 1 {
			t.Fatalf("timing: %v", err)
		}
		return d.Sessions[0].Timing
	}
	first := read()
	if first.Hours[0].Detail["building.first"] != 60 || first.Hours[0].Detail["building.rework"] != 0 {
		t.Fatalf("foreign review changed stage: %+v", first.Hours)
	}
	if _, _, err := db.AppendAgentEventAt(t.Context(), pool, parent, newUUID(t, pool), "mark", mark, time.Now()); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.Hours[0].Detail["building.rework"] != 60 {
		t.Fatalf("owned review did not invalidate stage: %+v", after.Hours)
	}
	appendMark := func(author, payload string) {
		t.Helper()
		if _, _, err := db.AppendAgentEventAt(t.Context(), pool, author, newUUID(t, pool), "mark", payload, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	approval := `{"kind":"review","board_id":"board-a","card":42,"verdict":"approved","by":"codex","round":2,"at":"` + start.Format(time.RFC3339Nano) + `"}`
	appendMark(foreign, approval)
	if read().Hours[0].Detail["building.rework"] != 60 {
		t.Fatal("foreign approval resolved owned review")
	}
	appendMark(parent, approval)
	if got := read(); got.Hours[0].Detail["building.first"] != 60 {
		t.Fatalf("approved review stayed unresolved: %+v", got.Hours)
	}
	failure := `{"kind":"failure","board_id":"board-a","card":42,"type":"test","suite":"activity","severity":"high","at":"` + start.Add(-2*time.Second).Format(time.RFC3339Nano) + `"}`
	appendMark(parent, failure)
	if read().Hours[0].Detail["building.first"] != 60 {
		t.Fatal("approved review did not resolve earlier test failure")
	}
	rerun := `{"kind":"failure","board_id":"board-a","card":42,"type":"test","suite":"activity","rerun_passed":true,"at":"` + start.Add(-time.Second).Format(time.RFC3339Nano) + `"}`
	appendMark(foreign, rerun)
	if read().Hours[0].Detail["building.first"] != 60 {
		t.Fatal("foreign rerun changed approved stage")
	}
	appendMark(parent, rerun)
	if got := read(); got.Hours[0].Detail["building.first"] != 60 {
		t.Fatalf("passed rerun stayed unresolved: %+v", got.Hours)
	}
}

func TestInsightGateCaptureKeepsHistoricalSuiteAsGap(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	id := mustSession(t, pool, daemon, "gate-capture-owner")
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+interval '60 seconds' WHERE id=$1`, id, start); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) VALUES($1,1,gen_random_uuid(),'tool_call','{"tool":"Bash","call_id":"old","input":{"command":"go test ./internal/db"}}',$2),($1,2,gen_random_uuid(),'tool_result','{"call_id":"old","exit_code":0}',$2::timestamptz+interval '10 seconds')`, id, start); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		kind, payload string
		at            time.Time
	}{{"tool_call", `{"tool":"Bash","call_id":"new","input":{"command":"go test ./internal/db"}}`, start.Add(20 * time.Second)}, {"tool_result", `{"call_id":"new","exit_code":1}`, start.Add(30 * time.Second)}} {
		if _, _, err := db.AppendAgentEventAt(t.Context(), pool, id, newUUID(t, pool), e.kind, e.payload, e.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RefreshInsightTiming(t.Context(), pool, id); err != nil {
		t.Fatal(err)
	}
	raw, err := db.InsightActivityTimings(t.Context(), pool, "gate-capture-owner", []string{id})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Sessions []struct{ Timing activity.Timing }
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Sessions[0].Timing.GateRuns) != 2 {
		t.Fatalf("expected historical gap and new suite: %s", raw)
	}
	var old, captured float64
	for _, r := range d.Sessions[0].Timing.GateRuns {
		switch r.Suite {
		case "":
			old += r.Seconds
		case "./internal/db":
			captured += r.Seconds
		}
	}
	if old != 10 || captured != 10 {
		t.Fatalf("suite coverage: old %v captured %v", old, captured)
	}
}

func TestInsightActivityScopeOwnedDescendantsAndUnlinked(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	root := mustSession(t, pool, daemon, "scope-owner")
	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "scope-owner", ParentSessionID: root})
	foreign := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "other-owner", ParentSessionID: root})
	unlinked := mustSession(t, pool, daemon, "scope-owner")
	if _, err := pool.Exec(ctx, `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, root); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ids, refs, truncated, exists, err := db.InsightActivityScope(ctx, pool, "scope-owner", root, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || !exists || truncated || len(ids) != 2 || !ids[child] || ids[foreign] || len(refs) != 1 {
		t.Fatalf("scope: ids=%v refs=%v truncated=%v exists=%v err=%v", ids, refs, truncated, exists, err)
	}
	ids, _, _, _, err = db.InsightActivityScope(ctx, pool, "scope-owner", "", now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || !ids[unlinked] || ids[foreign] {
		t.Fatalf("unlinked ownership: %v %v", ids, err)
	}
	_, _, _, exists, err = db.InsightActivityScope(ctx, pool, "other-owner", root, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || exists {
		t.Fatalf("foreign workspace accepted: %v %v", exists, err)
	}
	raw, err := db.InsightActivityTimings(ctx, pool, "other-owner", []string{root, child, unlinked})
	if err != nil || string(raw) == "" {
		t.Fatalf("timings: %s %v", raw, err)
	}
	var d struct {
		Sessions []any `json:"sessions"`
	}
	if err = json.Unmarshal(raw, &d); err != nil || len(d.Sessions) != 0 {
		t.Fatalf("foreign timing leaked: %s %v", raw, err)
	}
}

func TestInsightActivityQuestions(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	root := mustSession(t, pool, daemon, "question-owner")
	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "question-owner", ParentSessionID: root})
	foreign := mustSession(t, pool, daemon, "other-owner")
	now := time.Now().UTC()
	for _, id := range []string{root, child, foreign} {
		if _, err := pool.Exec(ctx, `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, id); err != nil {
			t.Fatal(err)
		}
	}
	// The failure alert uses an ask-shaped message but is not a question.
	if _, err := pool.Exec(ctx, `WITH m AS (INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','secret failure','open') RETURNING id) INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id) SELECT $1,'turn:1',1,id FROM m`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO messages(session_id,kind,body,status,created_at,child_escalated,owner_only) VALUES
	 ($1,'ask','secret main question','answered',$4,false,false),
	 ($2,'ask','secret coordinator question','answered',$4,false,false),
	 ($2,'ask','secret escalated question','answered',$4,true,false),
	 ($2,'ask','secret person-only question','open',$4,false,true),
	 ($2,'ask','old question','answered',$4::timestamptz-interval '2 days',false,false),
	 ($3,'ask','foreign question','open',$4,false,false)`, root, child, foreign, now); err != nil {
		t.Fatal(err)
	}
	raw, err := db.InsightActivityQuestions(ctx, pool, "question-owner", root, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		SessionID     string   `json:"session_id"`
		Asked         int      `json:"asked"`
		ReachedPerson int      `json:"reached_person"`
		Cards         []string `json:"cards"`
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("questions: %s", raw)
	}
	for _, row := range rows {
		wantAsked, wantPerson := 1, 1
		if row.SessionID == child {
			wantAsked, wantPerson = 3, 2
		}
		if row.SessionID == foreign || row.Asked != wantAsked || row.ReachedPerson != wantPerson || len(row.Cards) != 1 || row.Cards[0] != "board-a#7" {
			t.Fatalf("question projection: %s", raw)
		}
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatal("question prose leaked")
	}
}

type insightQueryCounter struct{ Queries int }

func (c *insightQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.Queries++
	return ctx
}
func (c *insightQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestInsightActivityTimingBatchesAllSessions(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	a := mustSession(t, pool, daemon, "batch-owner")
	b := mustSession(t, pool, daemon, "batch-owner")
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$3::timestamptz,ended_at=$3::timestamptz+CASE WHEN id=$1 THEN interval '10 seconds' ELSE interval '20 seconds' END WHERE id IN ($1,$2)`, a, b, start); err != nil {
		t.Fatal(err)
	}
	event := func(id string, seq int, at time.Time, kind, payload, callID string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,ts,kind,payload,tool_call_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, seq, newUUID(t, pool), at, kind, payload, callID); err != nil {
			t.Fatal(err)
		}
	}
	event(a, 1, start, "status_changed", `{"status":"running"}`, "")
	event(a, 2, start.Add(time.Second), "tool_call", `{"tool":"Bash","call_id":"call-a","input":{"command":"go test ./..."}}`, "call-a")
	event(a, 3, start.Add(3*time.Second), "tool_result", `{"call_id":"call-a"}`, "call-a")
	event(b, 1, start, "status_changed", `{"status":"idle"}`, "")
	event(a, 4, start.Add(4*time.Second), "subagent_started", `{"id":"native-worker"}`, "")
	if _, err := pool.Exec(ctx, `UPDATE sessions SET title='Check migrations' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if n, err := db.BackfillInsightTimings(ctx, pool, "batch-owner"); err != nil || n != 2 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	counter := &insightQueryCounter{}
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	raw, err := db.InsightActivityTimings(ctx, traced, "batch-owner", []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Sessions []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			HasSubagents bool   `json:"has_subagents"`
			Timing       *struct {
				Seconds map[string]float64 `json:"seconds"`
			} `json:"timing"`
		} `json:"sessions"`
	}
	if err = json.Unmarshal(raw, &d); err != nil || len(d.Sessions) != 2 || d.Sessions[0].Timing == nil || d.Sessions[1].Timing == nil {
		t.Fatalf("batch: %s %v", raw, err)
	}
	for _, session := range d.Sessions {
		if session.ID == a && (session.Title != "Check migrations" || !session.HasSubagents) {
			t.Fatalf("worker attribution: %s", raw)
		}
		if session.ID == b && session.HasSubagents {
			t.Fatal("unobserved subagents inferred")
		}
		if session.ID == a && session.Timing.Seconds["tests"] != 2 {
			t.Fatalf("test timing: %v", session.Timing.Seconds)
		}
		if session.ID == b && session.Timing.Seconds["idle"] != 20 {
			t.Fatalf("idle timing: %+v", session)
		}
	}
	if counter.Queries != 4 {
		t.Fatalf("timing used %d queries; expected metadata, events, one cache write and three connection-setting queries", counter.Queries)
	}
	counter.Queries = 0
	started := time.Now()
	warm, err := db.InsightActivityTimings(ctx, traced, "batch-owner", []string{a, b})
	if err != nil || string(warm) != string(raw) || counter.Queries != 4 {
		t.Fatalf("warm cache: queries=%d err=%v", counter.Queries, err)
	}
	t.Logf("cached timing: %s, %d queries", time.Since(started), counter.Queries)
	event(a, 5, start.Add(5*time.Second), "mark", `{"kind":"failure","type":"test"}`, "")
	counter.Queries = 0
	_, err = db.InsightActivityTimings(ctx, traced, "batch-owner", []string{a, b})
	if err != nil || counter.Queries != 4 {
		t.Fatalf("invalidated report: %d %v", counter.Queries, err)
	}
}

func TestInsightChecklistWorkerLinks(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	main := mustSession(t, pool, daemon, "checklist-owner")
	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "checklist-owner", ParentSessionID: main})
	foreign := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "someone-else", ParentSessionID: main})
	if _, err := pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,'board-a',1,'pin',now())`, main); err != nil {
		t.Fatal(err)
	}
	payload := `{"delta":true,"updates":[{"text":"Implement #42","assignees":[{"kind":"child_session","id":"` + child + `"},{"kind":"child_session","id":"` + foreign + `"}]}]}`
	if _, err := pool.Exec(ctx, `SELECT link_card_checklist($1,$2::jsonb,now())`, main, payload); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_card_links WHERE session_id=$1 AND board_id='board-a' AND card_number=42 AND source='checklist'`, child).Scan(&count); err != nil || count != 1 {
		t.Fatalf("worker link: %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_card_links WHERE session_id=$1`, foreign).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign worker linked: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET title='Build #50' WHERE id IN ($1,$2)`, child, foreign); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_card_links WHERE session_id=$1 AND card_number=50 AND source='title'`, child).Scan(&count); err != nil || count != 1 {
		t.Fatalf("inherited title context: %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_card_links WHERE session_id=$1`, foreign).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign inherited title context: %d %v", count, err)
	}
	// A second board makes bare references ambiguous; never allocate across it.
	if _, err := pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,'board-b',1,'pin',now());`, main); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT link_card_checklist($1,$2::jsonb,now())`, main, strings.ReplaceAll(payload, "#42", "#43")); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_card_links WHERE session_id=$1 AND card_number=43`, child).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ambiguous board linked: %d %v", count, err)
	}
}

func TestInsightTimingCacheWriteFailureKeepsReport(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	id := mustSession(t, pool, daemon, "cache-write-owner")
	_, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_insight_cache() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic write failure'; END$$; CREATE TRIGGER reject_insight_cache BEFORE INSERT ON session_insight_timings FOR EACH ROW EXECUTE FUNCTION reject_insight_cache()`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.InsightActivityTimings(t.Context(), pool, "cache-write-owner", []string{id})
	if err != nil || !strings.Contains(string(raw), `"seconds"`) {
		t.Fatalf("cache failure lost report: %v", err)
	}
}

type cancelTimingRead struct{ cancel context.CancelFunc }
type timingQueryKey struct{}

func (c *cancelTimingRead) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, timingQueryKey{}, strings.HasPrefix(d.SQL, "SELECT e.session_id,CASE"))
}
func (c *cancelTimingRead) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if stream, _ := ctx.Value(timingQueryKey{}).(bool); stream {
		c.cancel()
	}
}

func TestInsightTimingInterruptedReadWarmsCompletedSessions(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	ids := []string{mustSession(t, pool, daemon, "partial-owner"), mustSession(t, pool, daemon, "partial-owner"), mustSession(t, pool, daemon, "partial-owner")}
	for _, id := range ids {
		if _, err := pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,gen_random_uuid(),'status_changed','{"status":"running"}')`, id); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = &cancelTimingRead{cancel: cancel}
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	raw, err := db.InsightActivityTimings(ctx, traced, "partial-owner", ids)
	if err != nil || !strings.Contains(string(raw), `"partial":true`) {
		t.Fatalf("partial report: %s %v", raw, err)
	}
	var warmed int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM session_insight_timings`).Scan(&warmed); err != nil || warmed < 1 {
		t.Fatalf("completed sessions did not warm: %d %v", warmed, err)
	}
}

func TestInsightTitleRelinksAfterParentPinWithoutChangingRank(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	main := mustSession(t, pool, daemon, "late-pin-owner")
	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "late-pin-owner", ParentSessionID: main})
	foreign := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "foreign", ParentSessionID: main})
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET title='Build #42',started_at=now()-interval '5 days' WHERE id IN($1,$2)`, child, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO session_card_links VALUES($1,'board-a',1,'pin',now())`, main); err != nil {
		t.Fatal(err)
	}
	var count int
	var rankedCorrectly bool
	if err := pool.QueryRow(t.Context(), `SELECT count(*),coalesce(bool_and(c.linked_at=s.started_at),false) FROM session_card_links c JOIN sessions s ON s.id=c.session_id WHERE c.session_id=$1 AND c.card_number=42`, child).Scan(&count, &rankedCorrectly); err != nil || count != 1 || !rankedCorrectly {
		t.Fatalf("late title link: %d %t %v", count, rankedCorrectly, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_card_links WHERE session_id=$1`, foreign).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign title linked: %d %v", count, err)
	}
}

func TestInsightActivityRanksOldMainRecentAlert(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	main := mustSession(t, pool, daemon, "alert-owner")
	recent := mustSession(t, pool, daemon, "alert-owner")
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=now()-interval '120 days' WHERE id=$1`, main); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,'board-a',1,'pin',now()-interval '120 days'),($2,'board-a',2,'pin',now()-interval '1 day')`, main, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `WITH m AS (INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','Fixture','answered') RETURNING id) INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id) SELECT $1,'turn:1',1,id FROM m`, main); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, refs, _, _, err := db.InsightActivityScope(ctx, pool, "alert-owner", "", now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || len(refs) != 2 || refs[0].Number != 1 {
		t.Fatalf("recent automatic failure did not rank its old Main's card first: %v %v", refs, err)
	}
}
