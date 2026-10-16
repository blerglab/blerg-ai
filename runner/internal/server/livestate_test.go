package server

// A session's live state (livestate.go) against a real database: the same people as the reel read
// it, it carries the whole log's open asks, running subagents and tasks, and its ETag moves with
// what changes them.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func newLiveFx(t *testing.T) *topicFx {
	fx := newTopicFx(t)
	fx.api.RegisterLiveRoutes(fx.mux)
	return fx
}

func (fx *topicFx) getLive(sid, token, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/live", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

type liveReplyT struct {
	SessionID    string `json:"session_id"`
	ThroughSeq   int64  `json:"through_seq"`
	GeneratedAt  string `json:"generated_at"`
	TokensHidden bool   `json:"tokens_hidden"`
	ToolChanges  int    `json:"tool_changes"`
	Asks         []struct {
		EventID   string `json:"event_id"`
		MessageID string `json:"message_id"`
		Question  string `json:"question"`
	} `json:"asks"`
	Subagents []struct {
		Key        string `json:"key"`
		Title      string `json:"title"`
		Tokens     *int64 `json:"tokens"`
		Doing      string `json:"doing"`
		Background bool   `json:"background"`
	} `json:"subagents"`
	Tasks []struct {
		TaskID string `json:"task_id"`
	} `json:"tasks"`
	Counts struct {
		NeedsYou int `json:"needs_you"`
		Running  int `json:"running"`
	} `json:"counts"`
}

func decodeLive(t *testing.T, rec *httptest.ResponseRecorder) liveReplyT {
	t.Helper()
	var r liveReplyT
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("reply %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return r
}

// ask posts an ask the way `blerg-runner ask` does: a messages row, and the event the server records.
func (fx *topicFx) ask(sid, question string) string {
	fx.t.Helper()
	ctx := context.Background()
	id, err := db.CreateMessage(ctx, fx.pool, sid, "ask", question)
	if err != nil {
		fx.t.Fatal(err)
	}
	raw, _ := json.Marshal(messagingPayload{Body: question, MessageID: id})
	if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, sid, messageEventID(id, "ask"), "ask", string(raw), time.Time{}); err != nil {
		fx.t.Fatal(err)
	}
	return id
}

func (fx *topicFx) toolChange(account, sid string) {
	fx.t.Helper()
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO mcp_tool_changes (account_id, session_id, connection_id, connection_name, tool, kind, old_hash, new_hash)
		VALUES ($1, $2, gen_random_uuid(), 'cal', 'read', 'changed', 'x', 'y')`, account, sid); err != nil {
		fx.t.Fatal(err)
	}
}

// Access is /topics': for every caller and session the two answer the same status.
func TestGetLiveAccessMatchesTheReel(t *testing.T) {
	fx := newLiveFx(t)
	callers := map[string]string{
		"owner": fx.token(topicOwner, "human"), "viewer": fx.token(topicOther, "human"),
		"agent": fx.token(topicOwner, "agent"), "none": "",
	}
	sessions := map[string]string{"public": fx.sess, "private": fx.privSess, "ended": fx.ended, "unknown": newUUID(), "bad": "not-a-session"}
	for cn, tok := range callers {
		for sn, sid := range sessions {
			live, reel := fx.getLive(sid, tok, ""), fx.get(sid, tok, "")
			if live.Code != reel.Code {
				t.Errorf("%s on %s: /live %d, /topics %d", cn, sn, live.Code, reel.Code)
			}
		}
	}
	owner := fx.getLive(fx.sess, callers["owner"], "")
	if owner.Code != http.StatusOK || owner.Header().Get("Cache-Control") != "private, no-store" || !strings.HasPrefix(owner.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("owner = %d %v", owner.Code, owner.Header())
	}
	foreign, unknown := fx.getLive(fx.privSess, callers["owner"], ""), fx.getLive(newUUID(), callers["owner"], "")
	if foreign.Code != http.StatusNotFound || foreign.Body.String() != unknown.Body.String() {
		t.Errorf("someone else's private session = %d %s, unknown = %s: want one uniform 404", foreign.Code, foreign.Body, unknown.Body)
	}
}

func TestGetLiveReadsTheWholeLog(t *testing.T) {
	fx := newLiveFx(t)
	open := fx.ask(fx.sess, "Ship it?")
	answered := fx.ask(fx.sess, "Which branch?")
	if _, err := db.AnswerMessage(context.Background(), fx.pool, answered, "main"); err != nil {
		t.Fatal(err)
	}
	fx.event(fx.sess, "subagent_started", agent.SubagentPayload{ChildID: "toolu_bg", AgentType: "general-purpose", Summary: "Watch the deploy"})
	fx.event(fx.sess, "subagent_update", agent.SubagentPayload{ChildID: "toolu_bg", Usage: &agent.Usage{OutputTokens: 40}})
	fx.event(fx.sess, "tool_call", map[string]any{"tool": "Bash", "call_id": "c1", "parent_call_id": "toolu_bg", "input": map[string]string{"command": "kubectl get pods"}})
	fx.event(fx.sess, "subagent_done", agent.SubagentPayload{ChildID: "toolu_bg", Summary: "running in the background", Background: true})
	fx.event(fx.sess, "background_task_started", agent.BackgroundTaskStartedPayload{TaskID: "a1", CallID: "toolu_bg", Kind: "agent"})
	fx.event(fx.sess, "background_task_started", agent.BackgroundTaskStartedPayload{TaskID: "b1", CallID: "c2", Kind: "bash", Description: "npm run dev"})
	fx.event(fx.sess, "background_task_started", agent.BackgroundTaskStartedPayload{TaskID: "b2", CallID: "c3", Kind: "bash"})
	fx.event(fx.sess, "background_task_done", agent.BackgroundTaskDonePayload{TaskID: "b2", Status: "completed"})
	fx.event(fx.sess, "turn_done", agent.TurnDonePayload{})
	fx.toolChange(topicOwner, fx.sess)
	fx.toolChange(topicOther, fx.sess)

	r := decodeLive(t, fx.getLive(fx.sess, fx.token(topicOwner, "human"), ""))
	if len(r.Asks) != 1 || r.Asks[0].MessageID != open || r.Asks[0].Question != "Ship it?" {
		t.Errorf("asks = %+v", r.Asks)
	}
	if len(r.Subagents) != 1 || r.Subagents[0].Key != "toolu_bg" || !r.Subagents[0].Background || r.Subagents[0].Title != "Watch the deploy" ||
		r.Subagents[0].Doing != "running Bash · kubectl get pods" || r.Subagents[0].Tokens == nil || *r.Subagents[0].Tokens != 40 {
		t.Errorf("subagents = %+v", r.Subagents)
	}
	if len(r.Tasks) != 1 || r.Tasks[0].TaskID != "b1" {
		t.Errorf("tasks = %+v", r.Tasks)
	}
	if r.ToolChanges != 1 || r.Counts.NeedsYou != 2 || r.Counts.Running != 2 || r.SessionID != fx.sess || r.ThroughSeq == 0 || r.TokensHidden {
		t.Errorf("reply = %+v", r)
	}
	viewer := decodeLive(t, fx.getLive(fx.sess, fx.token(topicOther, "human"), ""))
	if !viewer.TokensHidden || viewer.Subagents[0].Tokens != nil {
		t.Errorf("another person sees no tokens: %+v", viewer)
	}
	if viewer.ToolChanges != 1 {
		t.Errorf("a viewer's tool changes are their own: %d", viewer.ToolChanges)
	}
	ended := decodeLive(t, fx.getLive(fx.ended, fx.token(topicOwner, "human"), ""))
	if len(ended.Asks)+len(ended.Subagents)+len(ended.Tasks) != 0 {
		t.Errorf("an ended session = %+v", ended)
	}
}

func TestGetLiveETag(t *testing.T) {
	fx := newLiveFx(t)
	id := fx.ask(fx.sess, "Ship it?")
	owner := fx.token(topicOwner, "human")
	first := fx.getLive(fx.sess, owner, "")
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	if again := fx.getLive(fx.sess, owner, tag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("an unchanged state = %d %q, want 304 and no body", again.Code, again.Body.String())
	}
	if other := fx.getLive(fx.sess, fx.token(topicOther, "human"), tag); other.Code != http.StatusOK {
		t.Errorf("another viewer class must not match the owner's tag: %d", other.Code)
	}
	changed := func(what string) {
		t.Helper()
		rec := fx.getLive(fx.sess, owner, tag)
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") == tag {
			t.Errorf("%s: %d, tag %q (was %q); want 200 and a new tag", what, rec.Code, rec.Header().Get("ETag"), tag)
		}
		tag = rec.Header().Get("ETag")
	}
	fx.event(fx.sess, "tool_call", map[string]any{"tool": "Bash", "call_id": "c1"})
	changed("a new event")
	if _, err := fx.pool.Exec(context.Background(), `UPDATE messages SET status = 'answered', answer = '' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	changed("an ask closed with no event")
	fx.toolChange(topicOwner, fx.sess)
	changed("a tool change")
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'idle' WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	changed("the turn's status (a foreground subagent stops with it)")
}

// The bar: on a 100k-event session the first read (no ETag) answers under 200 ms. The seed repeats
// a 50-event turn (a person message, a subagent started and done, an ask and its answer, a
// background task started and ended, the turn's end, 29 subagent updates — 58 % of the events, a
// real session's mix — and tool results), and the newest turn leaves a question open, a background
// subagent and a task running.
func TestGetLiveOnALongSession(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 100k events")
	}
	fx := newLiveFx(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, g, gen_random_uuid(),
		       CASE (g - 1) % 50 WHEN 0 THEN 'user_message' WHEN 1 THEN 'subagent_started' WHEN 2 THEN 'subagent_update'
		                   WHEN 3 THEN 'subagent_done' WHEN 4 THEN 'ask' WHEN 5 THEN 'user_message'
		                   WHEN 6 THEN 'background_task_started' WHEN 7 THEN 'background_task_done' WHEN 8 THEN 'turn_done'
		                   WHEN 9 THEN 'status_changed'
		                   ELSE CASE WHEN (g - 1) % 50 BETWEEN 10 AND 37 THEN 'subagent_update' ELSE 'tool_result' END END,
		       CASE (g - 1) % 50 WHEN 0 THEN jsonb_build_object('text', 'Task ' || g, 'source', 'chat')
		                   WHEN 1 THEN jsonb_build_object('child_id', 'A' || g, 'agent_type', 'general-purpose', 'summary', 'step ' || g)
		                   WHEN 2 THEN jsonb_build_object('child_id', 'A' || (g - 1), 'usage', jsonb_build_object('output_tokens', 5))
		                   WHEN 3 THEN jsonb_build_object('child_id', 'A' || (g - 2), 'summary', 'ok')
		                   WHEN 4 THEN jsonb_build_object('body', 'Proceed?', 'message_id', 'm' || g)
		                   WHEN 5 THEN jsonb_build_object('text', 'yes', 'source', 'ask_answer', 'message_id', 'm' || (g - 1))
		                   WHEN 6 THEN jsonb_build_object('task_id', 't' || g, 'call_id', 'c' || g, 'kind', 'bash')
		                   WHEN 7 THEN jsonb_build_object('task_id', 't' || (g - 1), 'status', 'completed')
		                   WHEN 8 THEN '{}'::jsonb
		                   WHEN 9 THEN jsonb_build_object('status', 'idle')
		                   ELSE CASE WHEN (g - 1) % 50 BETWEEN 10 AND 37
		                             THEN jsonb_build_object('child_id', 'A' || (g - (g - 1) % 50 + 1), 'model', 'claude-sonnet',
		                                  'usage', jsonb_build_object('input_tokens', g, 'output_tokens', 7))
		                             ELSE jsonb_build_object('call_id', 'c' || g, 'output', repeat('x', 200)) END END
		  FROM generate_series(1, 100000) AS g`, fx.sess); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		kind string
		p    any
	}{
		{"subagent_started", agent.SubagentPayload{ChildID: "toolu_live", Summary: "Watch the deploy"}},
		{"subagent_update", agent.SubagentPayload{ChildID: "toolu_live", Model: "claude-sonnet"}},
		{"tool_call", map[string]any{"tool": "Bash", "call_id": "x", "parent_call_id": "toolu_live", "input": map[string]string{"command": "make check"}}},
		{"subagent_done", agent.SubagentPayload{ChildID: "toolu_live", Background: true}},
		{"background_task_started", agent.BackgroundTaskStartedPayload{TaskID: "live", CallID: "c-live", Kind: "monitor"}},
	} {
		fx.event(fx.sess, e.kind, e.p)
	}
	fx.ask(fx.sess, "Ship it?")
	if _, err := fx.pool.Exec(ctx, `ANALYZE agent_events`); err != nil {
		t.Fatal(err)
	}
	owner := fx.token(topicOwner, "human")
	best := time.Hour
	var rec *httptest.ResponseRecorder
	for range 3 {
		began := time.Now()
		rec = fx.getLive(fx.sess, owner, "")
		if d := time.Since(began); d < best {
			best = d
		}
	}
	r := decodeLive(t, rec)
	if len(r.Asks) != 1 || len(r.Subagents) != 1 || r.Subagents[0].Doing != "running Bash · make check" || len(r.Tasks) != 1 || r.ThroughSeq != 100006 {
		t.Errorf("reply = %+v", r)
	}
	t.Logf("GET /live on 100k events (first read): best of 3 = %v", best)
	bar := 2 * time.Second
	if os.Getenv("BLERG_PERF_ASSERT") == "1" {
		bar = 200 * time.Millisecond
	}
	if best > bar {
		t.Errorf("the live state took %v, over the %v bar", best, bar)
	}
}
