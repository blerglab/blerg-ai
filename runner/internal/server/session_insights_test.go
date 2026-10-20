package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const sessionInsightsDaemon = "00000000-0000-4000-8000-0000000000db"

type sessionInsightsFixture struct {
	api  *API
	pool *pgxpool.Pool
	mint func(sub, kind string, caps ...string) string
}

func newSessionInsightsFixture(t *testing.T) *sessionInsightsFixture {
	t.Helper()
	pool := runnerContractPool(t)
	api := NewAPI(NewHub(), pool, "daemon-tok-1234567890", nil, "")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	if err := db.UpsertDaemon(context.Background(), pool, sessionInsightsDaemon, "d", "local", ""); err != nil {
		t.Fatal(err)
	}
	return &sessionInsightsFixture{api: api, pool: pool, mint: func(sub, kind string, caps ...string) string {
		return mintRunnerToken(t, priv, "core-1", identity.Claims{
			Sub: sub, Aud: coreAuthAudience, Kind: kind, Sid: "sid-" + sub,
			Caps: caps, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		})
	}}
}

func (f *sessionInsightsFixture) get(t *testing.T, id, token string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/insights", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/insights", f.api.HandleGetSessionInsights)
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *sessionInsightsFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestSessionInsightsCountsOneSession(t *testing.T) {
	f := newSessionInsightsFixture(t)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	end := start.Add(30 * time.Minute)
	id := seedSession(t, f.pool, sessionInsightsDaemon, "carol", "cluster", "stopped", false, start, &end)
	other := seedSession(t, f.pool, sessionInsightsDaemon, "carol", "cluster", "idle", false, start, nil)
	at := func(n int) time.Time { return start.Add(time.Duration(n) * time.Minute) }

	// Time: running 1→6 and 8→11 (8 min), waiting 6→8 (2 min), idle 11→30, the end (19 min).
	for _, st := range []struct {
		at     int
		status string
	}{{1, "running"}, {6, "waiting"}, {8, "running"}, {11, "idle"}} {
		seedEvent(t, f.pool, id, at(st.at), "status_changed", map[string]string{"status": st.status, "reason": "turn"})
	}
	// Messages: a chat message and an answer to an ask are a person's; a system one is not, nor one sent
	// through the runner contract (it may come from another agent).
	seedEvent(t, f.pool, id, at(1), "user_message", map[string]string{"text": "go", "source": "chat"})
	seedEvent(t, f.pool, id, at(7), "user_message", map[string]string{"text": "yes", "source": "ask_answer"})
	seedEvent(t, f.pool, id, at(9), "user_message", map[string]string{"text": "<task-notification>", "source": "system"})
	seedEvent(t, f.pool, id, at(9), "user_message", map[string]string{"text": "from another agent", "source": "runner"})
	// Nor is what a board, a parent session or a cron wrote, or a decision Blerg relayed.
	for _, src := range []string{"blerg-board", "session", "cron", "review", "proposal"} {
		seedEvent(t, f.pool, id, at(9), "user_message", map[string]string{"text": "not the person", "source": src})
	}
	// Tool calls: three of the main agent's, two of a subagent's (they carry parent_call_id).
	for _, tool := range []string{"Bash", "Bash", "Read"} {
		seedEvent(t, f.pool, id, at(2), "tool_call", map[string]any{"tool": tool, "call_id": newUUID(), "input": map[string]any{}})
	}
	for range 2 {
		seedEvent(t, f.pool, id, at(3), "tool_call", map[string]any{"tool": "Bash", "call_id": newUUID(), "input": map[string]any{}, "parent_call_id": "toolu_1"})
	}
	// Turns under m-main; one subagent with usage under m-sub; one background launch with no usage.
	seedEvent(t, f.pool, id, at(5), "turn_done", turnDonePayload("m-main", 100, 200, 300, 400))
	seedEvent(t, f.pool, id, at(10), "turn_done", turnDonePayload("m-main", 100, 200, 300, 400))
	seedEvent(t, f.pool, id, at(2), "subagent_started", map[string]any{"child_id": "toolu_1", "agent_type": "general-purpose", "model": ""})
	done := turnDonePayload("m-sub", 10, 20, 30, 40)
	done["child_id"], done["agent_type"], done["summary"] = "toolu_1", "general-purpose", "done"
	seedEvent(t, f.pool, id, at(4), "subagent_done", done)
	seedEvent(t, f.pool, id, at(4), "subagent_started", map[string]any{"child_id": "toolu_2", "agent_type": "Explore", "model": "haiku"})
	seedEvent(t, f.pool, id, at(4), "subagent_done", map[string]any{"child_id": "toolu_2", "agent_type": "Explore", "model": "m-haiku", "summary": "running in the background", "background": true})
	// Another session's events are not this one's.
	seedEvent(t, f.pool, other, at(5), "turn_done", turnDonePayload("m-main", 5000, 5000, 0, 0))
	seedEvent(t, f.pool, other, at(5), "tool_call", map[string]any{"tool": "Edit", "call_id": newUUID()})

	// Files: one the agent published counts, one a person uploaded does not.
	f.exec(t, `INSERT INTO session_artifacts (id, session_id, name, size, content_type, sha256, origin) VALUES ($1, $2, 'a.txt', 1, 'text/plain', 'x', 'agent'), ($3, $2, 'b.txt', 1, 'text/plain', 'x', 'user')`,
		"art-1", id, "art-2")
	for _, state := range []string{"pending", "done", "executing", "rejected", "expired"} {
		f.exec(t, `INSERT INTO mcp_proposals (account_id, session_id, connection_id, connection_name, url_snapshot, tool, tool_hash, arguments, state)
			VALUES ('carol', $1, gen_random_uuid(), 'c', 'https://example.invalid', 't', 'h', '{}', $2)`, id, state)
	}
	for _, c := range []struct{ mode, outcome string }{{"allow", "ok"}, {"allow", "ok"}, {"propose", "refused"}, {"list", "ok"}} {
		f.exec(t, `INSERT INTO mcp_call_log (session_id, connection_id, tool, mode, outcome) VALUES ($1, gen_random_uuid(), 't', $2, $3)`, id, c.mode, c.outcome)
	}
	if err := db.UpsertModelPrice(context.Background(), f.pool, db.ModelPrice{Model: "m-main", InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}); err != nil {
		t.Fatal(err)
	}

	code, body := f.get(t, id, f.mint("carol", "human", "session.start"))
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	var r sessionInsightsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.SessionID != id || r.Title != "title-carol" || r.Runtime != "cluster" || r.Kind != "agent" || r.EndedAt == nil {
		t.Errorf("header = %+v", r)
	}
	near(t, "lifetime", r.LifetimeSeconds, 1800)
	near(t, "running", r.RunningSeconds, 480)
	near(t, "waiting", r.WaitingSeconds, 120)
	near(t, "idle", r.IdleSeconds, 1140)
	if r.Turns != 2 || r.ToolCalls != 3 || r.MessagesFromPerson != 2 {
		t.Errorf("turns/tool calls/messages = %d/%d/%d, want 2/3/2", r.Turns, r.ToolCalls, r.MessagesFromPerson)
	}
	if len(r.ToolCallsByTool) != 2 || r.ToolCallsByTool[0] != (countRow{"Bash", 2}) || r.ToolCallsByTool[1] != (countRow{"Read", 1}) {
		t.Errorf("by tool = %+v", r.ToolCallsByTool)
	}
	s := r.Subagents
	if s.Count != 2 || s.ToolCalls != 2 {
		t.Errorf("subagents count/tool calls = %d/%d, want 2/2", s.Count, s.ToolCalls)
	}
	if len(s.ByType) != 2 || len(s.ByModel) != 2 {
		t.Errorf("subagents by type %+v, by model %+v", s.ByType, s.ByModel)
	}
	for _, m := range s.ByModel {
		if m.Key != "m-sub" && m.Key != "m-haiku" {
			t.Errorf("subagent model %q: the model the API reported should win over the alias", m.Key)
		}
	}
	if r.Tokens == nil || r.TokensHidden {
		t.Fatalf("the owner gets no tokens: %s", body)
	}
	tk := r.Tokens
	if tk.Main.Turns != 2 || tk.Main.Input != 200 || tk.Main.Output != 400 || tk.Main.CacheRead != 600 || tk.Main.CacheWrite != 800 {
		t.Errorf("main = %+v", tk.Main)
	}
	if tk.Subagents.Turns != 0 || tk.Subagents.Input != 10 || tk.Subagents.Output != 20 || tk.Subagents.CacheRead != 30 || tk.Subagents.CacheWrite != 40 {
		t.Errorf("subagents = %+v", tk.Subagents)
	}
	if tk.Totals.Turns != 2 || tk.Totals.Input != 210 || tk.Totals.Output != 420 {
		t.Errorf("totals = %+v", tk.Totals)
	}
	if len(tk.ByModel) != 2 || tk.ByModel[0].Model != "m-main" || !tk.ByModel[0].Priced || tk.ByModel[1].Model != "m-sub" || tk.ByModel[1].Priced {
		t.Errorf("by model = %+v", tk.ByModel)
	}
	if len(tk.UnpricedModels) != 1 || tk.UnpricedModels[0] != "m-sub" {
		t.Errorf("unpriced = %v", tk.UnpricedModels)
	}
	mainCost := 200*3/1e6 + 400*15/1e6 + 600*0.3/1e6 + 800*3.75/1e6
	if tk.Main.CostUSD == nil || tk.Totals.CostUSD == nil || tk.Subagents.CostUSD != nil {
		t.Fatalf("costs main/total/sub = %v/%v/%v", tk.Main.CostUSD, tk.Totals.CostUSD, tk.Subagents.CostUSD)
	}
	near(t, "main cost", *tk.Main.CostUSD, mainCost)
	near(t, "total cost", *tk.Totals.CostUSD, mainCost)
	near(t, "cache hit rate", tk.CacheHitRate, 630.0/(210+630+840))
	if r.Artifacts != 1 {
		t.Errorf("artifacts = %d, want 1 (a person's upload is not counted)", r.Artifacts)
	}
	if r.Proposals != (sessionProposals{Pending: 1, Approved: 2, Declined: 1, Expired: 1}) {
		t.Errorf("proposals = %+v", r.Proposals)
	}
	if r.MCPCalls.Total != 3 || len(r.MCPCalls.ByOutcome) != 2 || r.MCPCalls.ByOutcome[0] != (countRow{"ok", 2}) {
		t.Errorf("mcp calls = %+v (a tool listing is not a call)", r.MCPCalls)
	}
}

func TestSessionInsightsOfAnEmptyTerminalSession(t *testing.T) {
	f := newSessionInsightsFixture(t)
	start := time.Now().UTC().Add(-10 * time.Minute)
	id := seedSession(t, f.pool, sessionInsightsDaemon, "carol", "daemon", "running", false, start, nil)
	f.exec(t, `UPDATE sessions SET kind = 'tmux' WHERE id = $1`, id)
	code, body := f.get(t, id, f.mint("carol", "human", "session.start"))
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	var r sessionInsightsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != "tmux" || r.EndedAt != nil || r.LifetimeSeconds < 590 || r.Turns != 0 || r.Tokens == nil || r.Tokens.Totals.CostUSD != nil {
		t.Errorf("terminal session = %+v", r)
	}
	// every list is a list, never null, so the page can map over it
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	if raw["tool_calls_by_tool"] == nil || raw["subagents"].(map[string]any)["by_type"] == nil ||
		raw["tokens"].(map[string]any)["by_model"] == nil || raw["mcp_calls"].(map[string]any)["by_outcome"] == nil {
		t.Errorf("a list is null: %s", body)
	}
}

func TestSessionInsightsAreTheVisibleSessionsOnly(t *testing.T) {
	f := newSessionInsightsFixture(t)
	start := time.Now().UTC().Add(-time.Hour)
	public := seedSession(t, f.pool, sessionInsightsDaemon, "bob", "cluster", "idle", false, start, nil)
	private := seedSession(t, f.pool, sessionInsightsDaemon, "bob", "cluster", "idle", true, start, nil)
	alice := f.mint("alice", "human", "session.start")
	if code, _ := f.get(t, public, alice); code != http.StatusOK {
		t.Errorf("another person's public session = %d, want 200", code)
	}
	unknown, _ := f.get(t, newUUID(), alice)
	foreign, body := f.get(t, private, alice)
	if foreign != http.StatusNotFound || unknown != http.StatusNotFound {
		t.Errorf("someone else's private session = %d, unknown = %d, want 404 for both", foreign, unknown)
	}
	unknownBody := func() string { _, b := f.get(t, newUUID(), alice); return string(b) }()
	if string(body) != unknownBody {
		t.Errorf("a private session answers %s, an unknown one %s: they must not differ", body, unknownBody)
	}
	if code, _ := f.get(t, "not-a-uuid", alice); code != http.StatusNotFound {
		t.Errorf("malformed id = %d, want 404", code)
	}
	if code, _ := f.get(t, private, f.mint("bob", "human", "session.start")); code != http.StatusOK {
		t.Errorf("the owner of a private session = %d, want 200", code)
	}
	if code, _ := f.get(t, private, f.mint("bob", "agent", "session.start")); code != http.StatusForbidden {
		t.Errorf("an agent token = %d, want 403", code)
	}
	if code, _ := f.get(t, public, ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", code)
	}
}

// Tokens and cost follow the Insights page's rule: the session's owner and an administrator see them;
// another person who can see the session gets every other figure and tokens_hidden.
func TestSessionInsightsTokensAreTheOwnersAndAdministrators(t *testing.T) {
	f := newSessionInsightsFixture(t)
	start := time.Now().UTC().Add(-time.Hour)
	id := seedSession(t, f.pool, sessionInsightsDaemon, "carol", "cluster", "idle", false, start, nil)
	seedEvent(t, f.pool, id, start.Add(time.Minute), "turn_done", turnDonePayload("m-main", 100, 200, 300, 400))
	seedEvent(t, f.pool, id, start.Add(time.Minute), "tool_call", map[string]any{"tool": "Bash", "call_id": newUUID()})
	read := func(token string) (sessionInsightsResponse, map[string]any) {
		t.Helper()
		code, body := f.get(t, id, token)
		if code != http.StatusOK {
			t.Fatalf("GET = %d %s", code, body)
		}
		var r sessionInsightsResponse
		var raw map[string]any
		if json.Unmarshal(body, &r) != nil || json.Unmarshal(body, &raw) != nil {
			t.Fatalf("decode %s", body)
		}
		return r, raw
	}
	for name, token := range map[string]string{
		"owner":         f.mint("carol", "human", "session.start"),
		"administrator": f.mint("root", "human", "session.start", insightsAdminCap),
	} {
		r, _ := read(token)
		if r.TokensHidden || r.Tokens == nil || r.Tokens.Totals.Input != 100 {
			t.Errorf("%s: tokens = %+v, hidden = %v; want them shown", name, r.Tokens, r.TokensHidden)
		}
	}
	r, raw := read(f.mint("dave", "human", "session.start"))
	if !r.TokensHidden || r.Tokens != nil {
		t.Errorf("another member: tokens = %+v, hidden = %v; want them hidden", r.Tokens, r.TokensHidden)
	}
	if v, ok := raw["tokens"]; !ok || v != nil {
		t.Errorf("another member: tokens = %v, want null", v)
	}
	if r.Turns != 1 || r.ToolCalls != 1 {
		t.Errorf("another member: turns/tool calls = %d/%d, want the other figures kept", r.Turns, r.ToolCalls)
	}
}

func TestSessionInsightsAreNotCached(t *testing.T) {
	f := newSessionInsightsFixture(t)
	id := seedSession(t, f.pool, sessionInsightsDaemon, "carol", "cluster", "idle", false, time.Now().UTC().Add(-time.Hour), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/insights", nil)
	req.Header.Set("Authorization", "Bearer "+f.mint("carol", "human", "session.start"))
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/insights", f.api.HandleGetSessionInsights)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
