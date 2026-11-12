package db_test

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"math"
	"strings"
	"testing"
	"time"
)

func TestCardDataPinsWorkersWaitingAndOwnerIsolation(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	sid := mustSession(t, pool, daemon, "owner-a")
	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-a", ParentSessionID: sid})
	other := mustSession(t, pool, daemon, "owner-b")
	event := func(s string, seq int, kind, payload string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,$2,$3,$4,$5::jsonb)`, s, seq, newUUID(t, pool), kind, payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	event(sid, 1, "ticket", `{"action":"pin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#7"}}`)
	event(sid, 2, "ticket", `{"action":"unpin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#7"}}`)
	event(child, 1, "subagent_started", `{"child_id":"worker-a","model":"model-a","agent_type":"reviewer"}`)
	event(other, 1, "mark", `{"kind":"review","card":7,"board_id":"board-a","by":"hermes","verdict":"rejected"}`)
	now := time.Now().UTC().Truncate(time.Second)
	for _, v := range []struct {
		s      string
		a, b   int
		noWait bool
	}{{sid, 20, 10, false}, {child, 15, 5, false}, {sid, 40, 30, true}} {
		_, err := pool.Exec(ctx, `INSERT INTO messages(session_id,kind,body,status,created_at,answered_at,no_wait,owner_only) VALUES($1,'ask','Question','answered',$2,$3,$4,true)`, v.s, now.Add(-time.Duration(v.a)*time.Second), now.Add(-time.Duration(v.b)*time.Second), v.noWait)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := db.CardData(ctx, pool, "owner-a", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Sessions []json.RawMessage `json:"sessions"`
		Reviews  []json.RawMessage `json:"reviews"`
		Waiting  struct {
			Seconds float64 `json:"seconds"`
		} `json:"waiting_on_person"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 2 || len(got.Reviews) != 0 || got.Waiting.Seconds != 15 {
		t.Fatalf("data: %s", raw)
	}

	if _, err := pool.Exec(ctx, `UPDATE messages SET owner_only=false WHERE session_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	unknownRaw, err := db.CardData(ctx, pool, "owner-a", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var recipient struct {
		Waiting struct {
			Seconds   *float64 `json:"seconds"`
			Ambiguous bool     `json:"attribution_ambiguous"`
		} `json:"waiting_on_person"`
	}
	if err := json.Unmarshal(unknownRaw, &recipient); err != nil {
		t.Fatal(err)
	}
	if recipient.Waiting.Seconds != nil || !recipient.Waiting.Ambiguous {
		t.Fatal("an ask a parent worker can answer was claimed as person-only waiting")
	}
	if _, err := pool.Exec(ctx, `UPDATE messages SET owner_only=true WHERE session_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	event(child, 2, "ticket", `{"action":"pin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#8"}}`)
	raw, err = db.CardData(ctx, pool, "owner-a", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &got)
	if len(got.Sessions) != 1 || got.Waiting.Seconds != 10 {
		t.Fatalf("a worker explicitly linked to another card inherited this one: %s", raw)
	}

	event(sid, 3, "ticket", `{"action":"pin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#9"}}`)
	raw, err = db.CardData(ctx, pool, "owner-a", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var ambiguous struct {
		Waiting struct {
			Seconds   *float64 `json:"seconds"`
			Ambiguous bool     `json:"attribution_ambiguous"`
		} `json:"waiting_on_person"`
	}
	if err := json.Unmarshal(raw, &ambiguous); err != nil {
		t.Fatal(err)
	}
	if ambiguous.Waiting.Seconds != nil || !ambiguous.Waiting.Ambiguous {
		t.Fatalf("multi-card channel waiting was claimed as card time: %s", raw)
	}

	raw, err = db.CardData(ctx, pool, "owner-b", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &got)
	if len(got.Sessions) != 1 || len(got.Reviews) != 1 || got.Waiting.Seconds != 0 {
		t.Fatalf("owner-b: %s", raw)
	}
}

func TestLocalTicketsCarryEstimates(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()
	size, risk, complexity := "M", "high", "low"
	row, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "Estimate", Repos: []string{"repo-a"}, Size: &size, Risk: &risk, Complexity: &complexity})
	if err != nil {
		t.Fatal(err)
	}
	if row.Risk == nil || *row.Risk != "high" || row.RiskFirstSetAt == nil || row.SizeFirstSetAt == nil || row.ComplexityFirstSetAt == nil {
		t.Fatalf("estimates: %+v", row)
	}
	rows, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Risk: &risk, Complexity: &complexity})
	if err != nil || len(rows) != 1 {
		t.Fatalf("filtered: %v %v", rows, err)
	}
	otherRisk := "low"
	rows, _, err = db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Risk: &otherRisk})
	if err != nil || len(rows) != 0 {
		t.Fatalf("mismatched risk filter was ignored: %v %v", rows, err)
	}

}

func TestCardTitleLinksRetainUnambiguousBoardContext(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	sid := mustSession(t, pool, daemon, "owner-a")
	if _, err := pool.Exec(ctx, `UPDATE sessions SET title='Work #7 #8, not #9x or /#10' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,$2,'ticket','{"action":"pin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#7"}}')`, sid, newUUID(t, pool)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET title='Renamed' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	var numbers []int
	if err := pool.QueryRow(ctx, `SELECT array_agg(card_number ORDER BY card_number) FROM session_card_links WHERE session_id=$1 AND source='title'`, sid).Scan(&numbers); err != nil {
		t.Fatal(err)
	}
	if len(numbers) != 2 || numbers[0] != 7 || numbers[1] != 8 {
		t.Fatalf("title refs: %v", numbers)
	}
}

func TestCardDataEngineDefaultsMatchSessionSemantics(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sid := mustSession(t, pool, mustDaemon(t, pool), "owner-a")
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,$2,'ticket','{"action":"pin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#7"}}')`, sid, newUUID(t, pool)); err != nil {
		t.Fatal(err)
	}
	raw, err := db.CardData(ctx, pool, "owner-a", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Sessions []struct {
			Engine string `json:"engine"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Engine != "claude" {
		t.Fatalf("empty stored engine is the documented Claude default: %s", raw)
	}
}

func TestCardDataFailuresTimingAndAutomaticAlerts(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	sid := mustSession(t, pool, daemon, "owner-a")
	other := mustSession(t, pool, daemon, "owner-b")
	insert := func(s string, n int, kind, payload string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,$2,$3,$4,$5::jsonb)`, s, n, newUUID(t, pool), kind, payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(sid, 1, "ticket", `{"action":"pin","ticket":{"source":"blerg-board","board_id":"board-a","key":"#9"}}`)
	insert(sid, 2, "mark", `{"kind":"failure","board_id":"board-a","card":9,"type":"gate","severity":"medium","suite":"db","rerun_passed":false}`)
	insert(sid, 3, "mark", `{"kind":"failure","board_id":"board-a","card":9,"type":"gate","severity":"medium","suite":"db","rerun_passed":true}`)
	insert(sid, 4, "tool_call", `{"call_id":"hidden","tool":"Bash","input":{"command":"printf hidden-command"}}`)
	insert(sid, 5, "tool_result", `{"call_id":"hidden","output":"hidden-output"}`)
	insert(other, 1, "mark", `{"kind":"failure","board_id":"board-a","card":9,"type":"test","severity":"high","suite":"private"}`)
	var message string
	if err := pool.QueryRow(ctx, `INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','Worker failed','pending') RETURNING id`, sid).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id) VALUES($1,'test-alert',1,$2)`, sid, message); err != nil {
		t.Fatal(err)
	}
	raw, err := db.CardData(ctx, pool, "owner-a", "board-a", 9)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Failures []json.RawMessage `json:"failures"`
		Reruns   []json.RawMessage `json:"reruns"`
		Sessions []struct {
			Timing json.RawMessage `json:"timing"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Failures) != 3 || len(got.Reruns) != 1 || len(got.Sessions) != 1 || len(got.Sessions[0].Timing) == 0 {
		t.Fatalf("missing or leaked data: %s", raw)
	}
	for _, secret := range []string{"_timing_events", "hidden-command", "hidden-output", "private"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("timing leaked %s", secret)
		}
	}
	var waiting struct {
		Waiting struct {
			Seconds float64 `json:"seconds"`
		} `json:"waiting_on_person"`
	}
	if err := json.Unmarshal(raw, &waiting); err != nil {
		t.Fatal(err)
	}
	if waiting.Waiting.Seconds != 0 {
		t.Fatal("automatic failure alert counted as a person wait")
	}

}

func TestCardDataBoundsLargeSessionAndLabelsSharedAttribution(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	sid := mustSession(t, pool, daemon, "bounded-owner")
	_, err := pool.Exec(ctx, `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now()),($1,'board-a',8,'pin',now())`, sid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) SELECT $1,n,gen_random_uuid(),'turn_done',jsonb_build_object('usage_by_model',jsonb_build_object('example-model',jsonb_build_object('input_tokens',n))) FROM generate_series(1,60000) n`, sid)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.CardData(ctx, pool, "bounded-owner", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Sessions []struct {
			ModelEvents []json.RawMessage `json:"model_events"`
			Truncated   bool              `json:"timing_events_truncated"`
			Shared      bool              `json:"shared_with_other_cards"`
			Timing      json.RawMessage   `json:"timing"`
		} `json:"sessions"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 1 {
		t.Fatal("missing session")
	}
	s := data.Sessions[0]
	if len(s.ModelEvents) > 100 || !s.Truncated || !s.Shared || string(s.Timing) == "null" || len(raw) > 100000 {
		t.Fatalf("unbounded or misleading read: bytes=%d models=%d truncated=%v shared=%v timing=%s", len(raw), len(s.ModelEvents), s.Truncated, s.Shared, s.Timing)
	}
}

func TestCardDataReviewWorkerPhaseAndCachedToolIdentity(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "review-owner")
	sid := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "review-owner", ParentSessionID: parent})
	start := time.Now().UTC().Add(-12 * time.Second)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2,ended_at=$3 WHERE id=$1`, sid, start, start.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	for i, e := range []struct {
		at            int
		kind, payload string
	}{{0, "status_changed", `{"status":"running"}`}, {1, "tool_call", `{"tool":"Read","call_id":"review-read","input":{}}`}, {2, "tool_result", `{"call_id":"review-read","content":"large output"}`}, {10, "mark", `{"kind":"review","board_id":"board-a","card":7,"by":"codex","verdict":"approved"}`}} {
		if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) VALUES($1,$2,gen_random_uuid(),$3,$4::jsonb,$5)`, sid, i+1, e.kind, e.payload, start.Add(time.Duration(e.at)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var cached string
	if err := pool.QueryRow(ctx, `SELECT tool_call_id FROM agent_events WHERE session_id=$1 AND kind='tool_result'`, sid).Scan(&cached); err != nil || cached != "review-read" {
		t.Fatalf("cached tool identity: %q %v", cached, err)
	}
	raw, err := db.CardData(ctx, pool, "review-owner", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Sessions []struct {
			Timing struct {
				Phases map[string]float64 `json:"phases"`
			} `json:"timing"`
		} `json:"sessions"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 1 || data.Sessions[0].Timing.Phases["reviewing"] != 10 {
		t.Fatalf("review worker phase: %s", raw)
	}
}

func TestLongSessionTimingIsNotDropped(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	sid := mustSession(t, pool, daemon, "long-owner")
	start := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Second)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET started_at=$2,ended_at=$3 WHERE id=$1`, sid, start, start.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) SELECT $1,n,gen_random_uuid(),'turn_done','{}'::jsonb,$2::timestamptz+n*interval '1 millisecond' FROM generate_series(1,2001) n`, sid, start); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) VALUES
    ($1,2002,gen_random_uuid(),'tool_call','{"tool":"Bash","call_id":"long-test","input":{"command":"go test ./..."}}'::jsonb,$2::timestamptz+interval '3 seconds'),
    ($1,2003,gen_random_uuid(),'tool_result','{"call_id":"long-test","content":"result"}'::jsonb,$2::timestamptz+interval '5 seconds')`, sid, start); err != nil {
		t.Fatal(err)
	}
	raw, err := db.CardData(ctx, pool, "long-owner", "board-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Sessions []struct {
			Timing *struct {
				Seconds map[string]float64 `json:"seconds"`
			} `json:"timing"`
		} `json:"sessions"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 1 || data.Sessions[0].Timing == nil {
		t.Fatalf("long session lost complete timing: %s", raw)
	}
	var total float64
	for _, seconds := range data.Sessions[0].Timing.Seconds {
		total += seconds
	}
	if math.Abs(total-10) > 1e-9 || math.Abs(data.Sessions[0].Timing.Seconds["idle"]-7.999) > 1e-9 || data.Sessions[0].Timing.Seconds["tests"] != 2 {
		t.Fatalf("full history was not included: %+v", data.Sessions[0].Timing)
	}
}
