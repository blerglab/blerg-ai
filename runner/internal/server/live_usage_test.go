package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func usageEvent(seq int64, kind string, payload any) db.AgentEventRow {
	raw, _ := json.Marshal(payload)
	return db.AgentEventRow{Seq: seq, Kind: kind, Payload: string(raw)}
}

func TestTurnUsagePicksTheRunningTurnOnlyWhenNewerAndActive(t *testing.T) {
	done := usageEvent(5, "turn_done", turnDonePayload("m", 10, 20, 30, 40))
	running := usageEvent(7, "usage_updated", map[string]any{"scope": "turn", "model": "m", "usage": map[string]int{"input_tokens": 1, "output_tokens": 2, "cache_read_tokens": 3, "cache_write_tokens": 4}})
	stale := running
	stale.Seq = 3

	turn, inFlight := turnUsage([]db.AgentEventRow{done, running}, true)
	if turn == nil || !turn.InFlight || turn.Tokens != 10 || inFlight == nil {
		t.Fatalf("running turn not chosen: %+v %+v", turn, inFlight)
	}
	turn, inFlight = turnUsage([]db.AgentEventRow{done, running}, false)
	if turn == nil || turn.InFlight || turn.Tokens != 100 || turn.CacheRead != 30 || inFlight != nil {
		t.Fatalf("an idle session reported a running turn: %+v", turn)
	}
	turn, _ = turnUsage([]db.AgentEventRow{stale, done}, true)
	if turn == nil || turn.InFlight || turn.Tokens != 100 {
		t.Fatalf("an older report replaced the finished turn: %+v", turn)
	}
	unreported := turnDonePayload("m", 0, 0, 0, 0)
	unreported["usage_reported"] = false
	if turn, _ := turnUsage([]db.AgentEventRow{usageEvent(9, "turn_done", unreported)}, false); turn != nil {
		t.Fatalf("an unreported turn became a zero: %+v", turn)
	}
}

func TestSummariseLiveUsageTotalsLikeInsights(t *testing.T) {
	rows := []db.SessionTokenRow{
		{Model: "m", Turns: 2, Input: 100, Output: 200, CacheRead: 300, CacheWrite: 400},
		{Subagent: true, Model: "m", Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4},
	}
	price := []db.ModelPrice{{Model: "m", InputPerMTok: 1, OutputPerMTok: 1, CacheReadPerMTok: 1, CacheWritePerMTok: 1}}
	u := summariseLiveUsage(rows, nil, nil, price)
	if u == nil || u.Total != 1010 || u.Input != 101 || u.CacheRead != 303 || u.CacheWrite != 404 || u.Output != 202 || u.Subagents != 10 || u.Turns != 2 || u.CostUSD == nil {
		t.Fatalf("totals: %+v", u)
	}
	if u := summariseLiveUsage(rows, nil, nil, nil); u.CostUSD != nil {
		t.Fatalf("an unpriced model got a cost: %+v", u)
	}
	if u := summariseLiveUsage(nil, nil, nil, nil); u != nil {
		t.Fatalf("no usage became a chip: %+v", u)
	}
}

// TestLiveUsageOnMainAndChildRowsForOwnerOnly: the live reply carries the session's own usage and
// each started session's (a Claude and a Codex child) broken down, for the owner; another viewer
// who may see the session gets neither, as Insights.
func TestLiveUsageOnMainAndChildRowsForOwnerOnly(t *testing.T) {
	fx := newDelegFx(t)
	claude, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	codex, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	for id, engine := range map[string]string{claude: "claude", codex: "codex"} {
		if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine=$2,status='running' WHERE id=$1`, id, engine); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", turnDonePayload("m-main", 100, 200, 300, 400))
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", turnDonePayload("m-main", 10, 20, 30, 0))
	sub := turnDonePayload("m-sub", 1, 2, 3, 4)
	sub["child_id"], sub["agent_type"], sub["summary"] = "toolu_1", "general-purpose", "done"
	seedEvent(t, fx.pool, fx.parent, now, "subagent_done", sub)
	seedEvent(t, fx.pool, claude, now, "turn_done", turnDonePayload("claude-model", 5, 6, 7, 8))
	seedEvent(t, fx.pool, codex, now, "turn_done", turnDonePayload("codex-model", 50, 60, 70, 0))

	type reply struct {
		Usage    *liveUsage `json:"usage"`
		Children []struct {
			ID     string     `json:"id"`
			Tokens *int64     `json:"tokens"`
			Usage  *liveUsage `json:"usage"`
		} `json:"children"`
	}
	read := func(viewer string) reply {
		w := childrenLiveGet(t, fx, viewer, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: live code %d", viewer, w.Code)
		}
		var r reply
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	own := read("owner-1")
	if own.Usage == nil || own.Usage.Total != 1070 || own.Usage.Input != 111 || own.Usage.CacheRead != 333 || own.Usage.Subagents != 10 || own.Usage.Turns != 2 {
		t.Fatalf("main usage: %+v", own.Usage)
	}
	if own.Usage.Turn == nil || own.Usage.Turn.Tokens != 60 || own.Usage.Turn.InFlight {
		t.Fatalf("main last turn: %+v", own.Usage.Turn)
	}
	want := map[string]int64{claude: 26, codex: 180}
	if len(own.Children) != 2 {
		t.Fatalf("children: %+v", own.Children)
	}
	for _, c := range own.Children {
		if c.Usage == nil || c.Tokens == nil || c.Usage.Total != want[c.ID] || *c.Tokens != want[c.ID] || c.Usage.Turn == nil || c.Usage.Turn.Tokens != want[c.ID] {
			t.Fatalf("child %s usage: %+v tokens %v", c.ID, c.Usage, c.Tokens)
		}
	}
	other := read("owner-3")
	if other.Usage != nil {
		t.Fatalf("another viewer saw the session's usage: %+v", other.Usage)
	}
	for _, c := range other.Children {
		if c.Usage != nil || c.Tokens != nil {
			t.Fatalf("another viewer saw a child's usage: %+v", c)
		}
	}
}

func TestDecodeLiveContextKeepsOnlyWhatTheEngineReported(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ev := func(ctx any) db.AgentEventRow {
		e := usageEvent(4, "turn_done", map[string]any{"stop_reason": "end_turn", "context": ctx})
		e.Ts = at
		return e
	}
	if c := decodeLiveContext(ev(map[string]any{"tokens": 124000, "window": 200000})); c == nil || c.Tokens != 124000 || c.Window != 200000 || c.CompactAt != 0 || c.Compacted || !c.At.Equal(at) {
		t.Fatalf("reading: %+v", c)
	}
	if c := decodeLiveContext(ev(map[string]any{"compacted": true, "compacted_from": 167000, "window": 200000})); c == nil || c.Tokens != 0 || !c.Compacted || c.CompactedFrom != 167000 {
		t.Fatalf("compaction without a reading: %+v", c)
	}
	if c := decodeLiveContext(ev(map[string]any{"tokens": 10, "window": 100, "compact_at": 500})); c == nil || c.CompactAt != 0 {
		t.Fatalf("a threshold past the window was kept: %+v", c)
	}
	for name, bad := range map[string]any{"negative": map[string]any{"tokens": -1, "window": 10}, "huge": map[string]any{"tokens": int64(1) << 50}, "empty": map[string]any{"window": 200000}, "not an object": "full"} {
		if c := decodeLiveContext(ev(bad)); c != nil {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := decodeLiveContext(usageEvent(4, "turn_done", turnDonePayload("m", 1, 2, 3, 4))); c != nil {
		t.Fatalf("a turn without a report got a reading: %+v", c)
	}
}

// TestLiveContextOnMainAndChildRowsForOwnerOnly (#191): the live reply carries the newest reported
// context reading of the session and of each started session (a later turn that reported none does
// not hide it), for the owner only; a session whose engine never reported one gets none.
func TestLiveContextOnMainAndChildRowsForOwnerOnly(t *testing.T) {
	fx := newDelegFx(t)
	claude, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	hermes, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	for id, engine := range map[string]string{claude: "claude", hermes: "hermes"} {
		if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine=$2,status='running' WHERE id=$1`, id, engine); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	withContext := func(p map[string]any, ctx map[string]any) map[string]any { p["context"] = ctx; return p }
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", withContext(turnDonePayload("m-main", 1, 2, 3, 4), map[string]any{"tokens": 50000, "window": 200000}))
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", withContext(turnDonePayload("m-main", 1, 2, 3, 4), map[string]any{"tokens": 180000, "window": 200000}))
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", map[string]any{"stop_reason": "interrupted", "model": "m-main"})
	seedEvent(t, fx.pool, claude, now, "turn_done", withContext(turnDonePayload("claude-model", 5, 6, 7, 8), map[string]any{"compacted": true, "compacted_from": 170000, "tokens": 30000, "window": 200000}))
	seedEvent(t, fx.pool, hermes, now, "turn_done", turnDonePayload("hermes-model", 5, 6, 7, 8))

	type reply struct {
		Usage    *liveUsage `json:"usage"`
		Children []struct {
			ID    string     `json:"id"`
			Usage *liveUsage `json:"usage"`
		} `json:"children"`
	}
	read := func(viewer string) reply {
		w := childrenLiveGet(t, fx, viewer, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: live code %d", viewer, w.Code)
		}
		var r reply
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	own := read("owner-1")
	if own.Usage == nil || own.Usage.Context == nil || own.Usage.Context.Tokens != 180000 || own.Usage.Context.Window != 200000 || own.Usage.Context.Compacted {
		t.Fatalf("main context: %+v", own.Usage)
	}
	for _, c := range own.Children {
		switch c.ID {
		case claude:
			if c.Usage == nil || c.Usage.Context == nil || !c.Usage.Context.Compacted || c.Usage.Context.CompactedFrom != 170000 || c.Usage.Context.Tokens != 30000 {
				t.Fatalf("claude child context: %+v", c.Usage)
			}
		case hermes:
			if c.Usage == nil || c.Usage.Context != nil {
				t.Fatalf("hermes child (no report) context: %+v", c.Usage)
			}
		}
	}
	other := read("owner-3")
	if other.Usage != nil {
		t.Fatalf("another viewer saw the session's context: %+v", other.Usage)
	}
	for _, c := range other.Children {
		if c.Usage != nil {
			t.Fatalf("another viewer saw a child's context: %+v", c)
		}
	}
}

// TestLiveContextDropsTheCountAfterALaterCompaction (#191 review): a compaction recorded after the
// newest turn that reported a reading (the turn then failed or was interrupted, so it reported
// none) leaves the compacted marker, not the obsolete count; one before that turn changes nothing.
func TestLiveContextDropsTheCountAfterALaterCompaction(t *testing.T) {
	fx := newDelegFx(t)
	now := time.Now()
	withContext := func(p map[string]any, ctx map[string]any) map[string]any { p["context"] = ctx; return p }
	read := func() *liveContext {
		w := childrenLiveGet(t, fx, "owner-1", "")
		if w.Code != http.StatusOK {
			t.Fatalf("live code %d", w.Code)
		}
		var r struct {
			Usage *liveUsage `json:"usage"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Usage == nil {
			t.Fatal("no usage")
		}
		return r.Usage.Context
	}
	seedEvent(t, fx.pool, fx.parent, now, "compaction", map[string]any{"trigger": "auto", "pre_tokens": 99000})
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", withContext(turnDonePayload("m", 1, 2, 3, 4), map[string]any{"tokens": 170000, "window": 200000}))
	if c := read(); c == nil || c.Tokens != 170000 || c.Compacted {
		t.Fatalf("an earlier compaction changed the reading: %+v", c)
	}
	seedEvent(t, fx.pool, fx.parent, now, "compaction", map[string]any{"trigger": "auto", "pre_tokens": 171000})
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", map[string]any{"stop_reason": "interrupted", "model": "m"})
	if c := read(); c == nil || c.Tokens != 0 || !c.Compacted || c.CompactedFrom != 171000 || c.Window != 200000 {
		t.Fatalf("the count from before the compaction survived: %+v", c)
	}
	seedEvent(t, fx.pool, fx.parent, now, "turn_done", withContext(turnDonePayload("m", 1, 2, 3, 4), map[string]any{"tokens": 40000, "window": 200000}))
	if c := read(); c == nil || c.Tokens != 40000 || c.Compacted {
		t.Fatalf("a new reading after the compaction: %+v", c)
	}
}
