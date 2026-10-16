package server

// A session's highlight reel (topics.go) against a real database: the agent marks work with its
// per-session token; a person who may see the session reads the reel.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	topicOwner       = "user-1"
	topicOther       = "user-2"
	topicDaemonToken = "daemon-tok-1234567890"
	topicDaemonID    = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0f"
)

type topicFx struct {
	t        *testing.T
	api      *API
	pool     *pgxpool.Pool
	mux      *http.ServeMux
	priv     ed25519.PrivateKey
	events   chan []byte // what the hub fans out to a browser watching sess
	sess     string      // public, started by topicOwner
	sessTok  string
	otherTok string // the messaging token of another session
	privSess string // private, started by topicOther
	ended    string // stopped, started by topicOwner
}

func newTopicFx(t *testing.T) *topicFx {
	t.Helper()
	pool := runnerContractPool(t)
	ctx := context.Background()
	const daemonID = topicDaemonID
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	api := NewAPI(hub, pool, topicDaemonToken, nil, "")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	mux := http.NewServeMux()
	api.RegisterTopicRoutes(mux)
	fx := &topicFx{t: t, api: api, pool: pool, mux: mux, priv: priv}
	mk := func(account, status string) (string, string) {
		id := newUUID()
		if err := db.InsertSession(ctx, pool, id, daemonID, status, "/repos/app", "app", "t", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE sessions SET kind = 'agent' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionSpawningAccount(ctx, pool, id, account); err != nil {
			t.Fatal(err)
		}
		tok, err := db.MintSessionToken(ctx, pool, id, []string{"message"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return id, tok
	}
	fx.sess, fx.sessTok = mk(topicOwner, "running")
	_, fx.otherTok = mk(topicOther, "running")
	fx.privSess, _ = mk(topicOther, "running")
	if err := api.MarkPrivate(ctx, fx.privSess); err != nil {
		t.Fatal(err)
	}
	fx.ended, _ = mk(topicOwner, "stopped")
	fx.events = make(chan []byte, 64)
	hub.Subscribe(fx.sess, "watcher", fx.events)
	return fx
}

func (fx *topicFx) token(account, kind string, caps ...string) string {
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{
		Sub: account, Aud: coreAuthAudience, Kind: kind, Sid: testSID,
		Caps: append([]string{coreAuthBrowserCap}, caps...), ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
}

func (fx *topicFx) post(sid, token string, body any) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid+"/topics", bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func topicStart(title string) map[string]string {
	return map[string]string{"action": "start", "title": title}
}

// topicEvents is what the session's transcript holds of kind topic, in order.
func (fx *topicFx) topicEvents(sid string) []agent.TopicPayload {
	fx.t.Helper()
	rows, err := fx.pool.Query(context.Background(), `SELECT payload::text FROM agent_events WHERE session_id = $1 AND kind = 'topic' ORDER BY seq`, sid)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer rows.Close()
	var out []agent.TopicPayload
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			fx.t.Fatal(err)
		}
		var p agent.TopicPayload
		_ = json.Unmarshal([]byte(raw), &p)
		out = append(out, p)
	}
	return out
}

func TestPostTopicStartAndDone(t *testing.T) {
	fx := newTopicFx(t)
	code, body := fx.post(fx.sess, fx.sessTok, topicStart("Build the History tab"))
	if code != http.StatusCreated || body["action"] != "start" || body["title"] != "Build the History tab" {
		t.Fatalf("start = %d %v", code, body)
	}
	if v, present := body["closed"]; !present || v != nil {
		t.Errorf("a start over nothing says closed: null, got %v (present %v)", v, present)
	}
	id, _ := body["id"].(string)
	if !strings.HasPrefix(id, "tp_") {
		t.Errorf("id = %q", id)
	}
	select {
	case msg := <-fx.events:
		if !strings.Contains(string(msg), `"kind":"topic"`) || !strings.Contains(string(msg), id) {
			t.Errorf("fan-out = %s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the topic event was not fanned out to the session's watchers")
	}
	code, done := fx.post(fx.sess, fx.sessTok, map[string]string{"action": "done", "outcome": "Shipped on main"})
	if code != http.StatusCreated || done["id"] != id || done["title"] != "Build the History tab" || done["action"] != "done" || done["outcome"] != "Shipped on main" {
		t.Errorf("done = %d %v", code, done)
	}
	if code, body := fx.post(fx.sess, fx.sessTok, map[string]string{"action": "done"}); code != http.StatusConflict || body["error"] != "no topic is open" {
		t.Errorf("done with nothing open = %d %v, want 409", code, body)
	}
	if got := fx.topicEvents(fx.sess); len(got) != 2 || got[0].Action != "start" || got[1].Action != "done" {
		t.Errorf("events = %+v", got)
	}
}

func TestPostTopicStartOverAnOpenTopicWritesOneEvent(t *testing.T) {
	fx := newTopicFx(t)
	_, a := fx.post(fx.sess, fx.sessTok, topicStart("Fix y"))
	code, b := fx.post(fx.sess, fx.sessTok, topicStart("Build x"))
	closed, _ := b["closed"].(map[string]any)
	if code != http.StatusCreated || closed["id"] != a["id"] || closed["title"] != "Fix y" {
		t.Fatalf("second start = %d %v; want it to name the topic it closed", code, b)
	}
	if got := fx.topicEvents(fx.sess); len(got) != 2 || got[0].Action != "start" || got[1].Action != "start" {
		t.Errorf("a start over an open topic is itself the close: %+v", got)
	}
}

func TestPostTopicCredentialsAndSessions(t *testing.T) {
	fx := newTopicFx(t)
	for _, c := range []struct {
		name  string
		sid   string
		token string
		body  any
		want  int
	}{
		{"another session's token", fx.sess, fx.otherTok, topicStart("x"), http.StatusForbidden},
		{"no credential", fx.sess, "", topicStart("x"), http.StatusUnauthorized},
		{"a person's token", fx.sess, fx.token(topicOwner, "human"), topicStart("x"), http.StatusUnauthorized},
		{"a malformed id", "not-a-session", topicDaemonToken, topicStart("x"), http.StatusNotFound},
		{"an unknown session", newUUID(), topicDaemonToken, topicStart("x"), http.StatusNotFound},
		{"an ended session", fx.ended, topicDaemonToken, topicStart("x"), http.StatusConflict},
		{"an unknown action", fx.sess, fx.sessTok, map[string]string{"action": "rename", "title": "x"}, http.StatusBadRequest},
		{"not JSON", fx.sess, fx.sessTok, "start", http.StatusBadRequest},
		{"the daemon token", fx.sess, topicDaemonToken, topicStart("x"), http.StatusCreated},
	} {
		if code, body := fx.post(c.sid, c.token, c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, body, c.want)
		}
	}
}

func TestPostTopicCleansText(t *testing.T) {
	fx := newTopicFx(t)
	if _, b := fx.post(fx.sess, fx.sessTok, topicStart("\u202eShip\u200b the\tthing\x07\n\nnow")); b["title"] != "Ship the thing now" {
		t.Errorf("cleaned title = %q", b["title"])
	}
	_, b := fx.post(fx.sess, fx.sessTok, topicStart(strings.Repeat("ab", 60)))
	if title, _ := b["title"].(string); utf8.RuneCountInString(title) != 80 || !strings.HasSuffix(title, "…") {
		t.Errorf("a long title = %q", title)
	}
	before := len(fx.topicEvents(fx.sess))
	if code, b := fx.post(fx.sess, fx.sessTok, topicStart("\u200b\u202e\x07 \t")); code != http.StatusBadRequest {
		t.Errorf("a title of controls only = %d %v, want 400", code, b)
	}
	if len(fx.topicEvents(fx.sess)) != before {
		t.Error("a refused title must record nothing")
	}
	_, d := fx.post(fx.sess, fx.sessTok, map[string]string{"action": "done", "outcome": strings.Repeat("é", 300)})
	if o, _ := d["outcome"].(string); utf8.RuneCountInString(o) != 200 {
		t.Errorf("a long outcome has %d runes, want 200", utf8.RuneCountInString(o))
	}
}

// A done names the commits the work landed: checked, lower-cased, deduplicated in order, echoed
// and recorded; refused on a start and when one is not a sha or there are too many.
func TestPostTopicCommits(t *testing.T) {
	fx := newTopicFx(t)
	commits := func(shas ...string) []map[string]string {
		out := []map[string]string{}
		for _, s := range shas {
			out = append(out, map[string]string{"sha": s})
		}
		return out
	}
	if code, b := fx.post(fx.sess, fx.sessTok, map[string]any{"action": "start", "title": "x", "commits": commits("1a2b3c4")}); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(b["error"]), "done") {
		t.Errorf("commits on a start = %d %v, want 400", code, b)
	}
	if len(fx.topicEvents(fx.sess)) != 0 {
		t.Fatal("a refused start must record nothing")
	}
	fx.post(fx.sess, fx.sessTok, topicStart("Build x"))
	for _, c := range []struct {
		name string
		body []map[string]string
		want string
	}{
		{"not hex", commits("1a2b3c4", "zzzzzzz"), "7 to 40 hex"},
		{"too short", commits("1a2b3c"), "7 to 40 hex"},
		{"too many", commits(func() []string {
			var s []string
			for i := range 51 {
				s = append(s, fmt.Sprintf("%07x", i))
			}
			return s
		}()...), "at most 50"},
	} {
		code, b := fx.post(fx.sess, fx.sessTok, map[string]any{"action": "done", "commits": c.body})
		if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(b["error"]), c.want) {
			t.Errorf("%s: %d %v, want 400 %q", c.name, code, b, c.want)
		}
	}
	if got := fx.topicEvents(fx.sess); len(got) != 1 {
		t.Fatalf("a refused done must record nothing and leave the topic open: %+v", got)
	}
	code, b := fx.post(fx.sess, fx.sessTok, map[string]any{"action": "done", "outcome": "Shipped",
		"commits": commits("9F8E7D6", "1a2b3c4", "9f8e7d6")})
	echoed, _ := json.Marshal(b["commits"])
	if code != http.StatusCreated || string(echoed) != `[{"sha":"9f8e7d6"},{"sha":"1a2b3c4"}]` {
		t.Fatalf("done with commits = %d %v", code, b)
	}
	got := fx.topicEvents(fx.sess)
	if len(got) != 2 || len(got[1].Commits) != 2 || got[1].Commits[0].Sha != "9f8e7d6" || got[1].Commits[1].Sha != "1a2b3c4" {
		t.Errorf("recorded = %+v", got)
	}
	// A done without commits keeps the payload as it was: no commits key.
	fx.post(fx.sess, fx.sessTok, topicStart("Build y"))
	_, plain := fx.post(fx.sess, fx.sessTok, map[string]string{"action": "done"})
	if _, present := plain["commits"]; present {
		t.Errorf("a done without commits echoes none: %v", plain)
	}
}

func TestPostTopicCap(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(context.Background(), `
		INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, g, gen_random_uuid(), 'topic', jsonb_build_object('id', 'tp_' || g, 'title', 't', 'action', 'start')
		  FROM generate_series(1, 200) AS g`, fx.sess); err != nil {
		t.Fatal(err)
	}
	code, body := fx.post(fx.sess, fx.sessTok, topicStart("one more"))
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(body["error"]), "200 topic events") {
		t.Errorf("past the cap = %d %v, want 409", code, body)
	}
}

// At the cap a start is refused, but the open topic can still be closed.
func TestPostTopicCapStillAllowsDone(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(context.Background(), `
		INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, g, gen_random_uuid(), 'topic', jsonb_build_object('id', 'tp_' || g, 'title', 't' || g, 'action', 'start')
		  FROM generate_series(1, 200) AS g`, fx.sess); err != nil {
		t.Fatal(err)
	}
	code, body := fx.post(fx.sess, fx.sessTok, map[string]string{"action": "done", "outcome": "finished"})
	if code != http.StatusCreated || body["id"] != "tp_200" || body["action"] != "done" {
		t.Errorf("done at the cap = %d %v, want 201 closing tp_200", code, body)
	}
	if code, body := fx.post(fx.sess, fx.sessTok, topicStart("one more")); code != http.StatusConflict || !strings.Contains(fmt.Sprint(body["error"]), "200 topic events") {
		t.Errorf("start past the cap = %d %v, want 409", code, body)
	}
}

// Two starts at once: the topic lock serialises them, so exactly one closes the other.
func TestPostTopicConcurrentStartsAreSerialised(t *testing.T) {
	fx := newTopicFx(t)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([]map[string]any, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], bodies[i] = fx.post(fx.sess, fx.sessTok, topicStart(fmt.Sprintf("T%d", i)))
		}(i)
	}
	wg.Wait()
	if codes[0] != http.StatusCreated || codes[1] != http.StatusCreated {
		t.Fatalf("codes = %v", codes)
	}
	first, second := bodies[0], bodies[1]
	if first["closed"] != nil {
		first, second = second, first
	}
	closed, _ := second["closed"].(map[string]any)
	if first["closed"] != nil || closed == nil || closed["id"] != first["id"] {
		t.Errorf("one start must close the other: %v / %v", first, second)
	}
	if got := fx.topicEvents(fx.sess); len(got) != 2 {
		t.Errorf("events = %+v", got)
	}
}

func (fx *topicFx) get(sid, token, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/topics", nil)
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

// event appends an event the way the daemon's ingest does (the next seq).
func (fx *topicFx) event(sid, kind string, payload any) {
	fx.t.Helper()
	raw, _ := json.Marshal(payload)
	if _, _, err := db.AppendAgentEventAt(context.Background(), fx.pool, sid, newUUID(), kind, string(raw), time.Time{}); err != nil {
		fx.t.Fatal(err)
	}
}

type reelReply struct {
	SessionID    string `json:"session_id"`
	ThroughSeq   int64  `json:"through_seq"`
	TokensHidden bool   `json:"tokens_hidden"`
	Unmarked     *struct {
		Agents    int               `json:"agents"`
		Documents []json.RawMessage `json:"documents"`
		Subagents []struct {
			Title string `json:"title"`
		} `json:"subagents"`
	} `json:"unmarked"`
	Topics []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Items  []struct {
			Type    string          `json:"type"`
			Status  string          `json:"status"`
			Usage   json.RawMessage `json:"usage"`
			Commits []struct {
				Sha string  `json:"sha"`
				URL *string `json:"url"`
			} `json:"commits"`
		} `json:"items"`
	} `json:"topics"`
}

func decodeReel(t *testing.T, rec *httptest.ResponseRecorder) reelReply {
	t.Helper()
	var r reelReply
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("reply %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return r
}

// seedReel: a marked topic with a finished subagent that reported its tokens.
func (fx *topicFx) seedReel() {
	if code, b := fx.post(fx.sess, fx.sessTok, topicStart("Build the History tab")); code != http.StatusCreated {
		fx.t.Fatalf("start = %d %v", code, b)
	}
	fx.event(fx.sess, "subagent_started", map[string]any{"child_id": "toolu_1", "agent_type": "general-purpose", "summary": "Implement task 3"})
	fx.event(fx.sess, "subagent_done", map[string]any{"child_id": "toolu_1", "summary": "ok", "usage": map[string]int{"output_tokens": 9}})
}

func TestGetTopicsAccessAndTokens(t *testing.T) {
	fx := newTopicFx(t)
	fx.seedReel()
	owner := fx.get(fx.sess, fx.token(topicOwner, "human"), "")
	if owner.Code != http.StatusOK || owner.Header().Get("Cache-Control") != "private, no-store" || !strings.HasPrefix(owner.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("owner = %d %v", owner.Code, owner.Header())
	}
	r := decodeReel(t, owner)
	if r.SessionID != fx.sess || r.TokensHidden || len(r.Topics) != 1 || r.Topics[0].Title != "Build the History tab" {
		t.Fatalf("owner reply = %+v", r)
	}
	if u := string(r.Topics[0].Items[1].Usage); u == "null" || u == "" {
		t.Errorf("the owner sees the subagent's tokens, got %s", u)
	}
	viewer := decodeReel(t, fx.get(fx.sess, fx.token(topicOther, "human"), ""))
	if !viewer.TokensHidden || string(viewer.Topics[0].Items[1].Usage) != "null" {
		t.Errorf("another person sees no tokens: %+v", viewer)
	}
	admin := decodeReel(t, fx.get(fx.sess, fx.token(topicOther, "human", insightsAdminCap), ""))
	if admin.TokensHidden {
		t.Error("an administrator sees tokens")
	}
	foreign := fx.get(fx.privSess, fx.token(topicOwner, "human"), "")
	unknown := fx.get(newUUID(), fx.token(topicOwner, "human"), "")
	if foreign.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || foreign.Body.String() != unknown.Body.String() {
		t.Errorf("someone else's private session = %d %s, unknown = %d %s: want one uniform 404", foreign.Code, foreign.Body, unknown.Code, unknown.Body)
	}
	if code := fx.get(fx.privSess, fx.token(topicOther, "human"), "").Code; code != http.StatusOK {
		t.Errorf("the owner of a private session = %d, want 200", code)
	}
	if code := fx.get(fx.sess, fx.token(topicOwner, "agent"), "").Code; code != http.StatusForbidden {
		t.Errorf("an agent token = %d, want 403", code)
	}
	if code := fx.get(fx.sess, "", "").Code; code != http.StatusUnauthorized {
		t.Errorf("no credential = %d, want 401", code)
	}
}

// R7: work outside every marker window is one unmarked summary, never a topic; untitled is gone.
func TestGetTopicsUnmarkedWork(t *testing.T) {
	fx := newTopicFx(t)
	owner := fx.token(topicOwner, "human")
	fx.event(fx.sess, "user_message", map[string]any{"text": "Fix the drawer", "source": "chat"})
	fx.event(fx.sess, "subagent_started", map[string]any{"child_id": "toolu_0", "agent_type": "Explore", "summary": "Survey"})
	rec := fx.get(fx.sess, owner, "")
	r := decodeReel(t, rec)
	if len(r.Topics) != 0 || r.Unmarked == nil || r.Unmarked.Agents != 1 || len(r.Unmarked.Subagents) != 1 || r.Unmarked.Subagents[0].Title != "Survey" {
		t.Fatalf("no marker: want no topic and one unmarked agent, got %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"untitled"`) || !strings.Contains(rec.Body.String(), `"topics":[]`) {
		t.Errorf("the reply has topics [] and no untitled: %s", rec.Body)
	}
	fx.event(fx.sess, "turn_done", map[string]any{"stop_reason": "end_turn"})
	fx.seedReel()
	if r := decodeReel(t, fx.get(fx.sess, owner, "")); len(r.Topics) != 1 || r.Unmarked == nil || r.Unmarked.Agents != 1 {
		t.Errorf("the marked work is a topic, the earlier turn's work stays unmarked: %+v", r)
	}
	fx.event(fx.privSess, "topic", map[string]any{"id": "tp_1", "title": "Build x", "action": "start"})
	fx.event(fx.privSess, "subagent_started", map[string]any{"child_id": "toolu_1", "summary": "x"})
	if rec := fx.get(fx.privSess, fx.token(topicOther, "human"), ""); !strings.Contains(rec.Body.String(), `"unmarked":null`) {
		t.Errorf("nothing outside a window: unmarked is null, got %s", rec.Body)
	}
}

func TestTopicsETagIgnoresTheTurnStatus(t *testing.T) {
	at := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	tag := func(status string) string { return topicsETag(7, 1, at, status, nil, "", true) }
	if tag("running") != tag("idle") || tag("idle") != tag("waiting") {
		t.Errorf("a turn's status must not bust the tag: %s %s %s", tag("running"), tag("idle"), tag("waiting"))
	}
	if tag("stopped") == tag("idle") {
		t.Errorf("a terminal status must bust the tag: %s", tag("stopped"))
	}
}

func TestGetTopicsETag(t *testing.T) {
	fx := newTopicFx(t)
	fx.seedReel()
	owner := fx.token(topicOwner, "human")
	first := fx.get(fx.sess, owner, "")
	tag := first.Header().Get("ETag")
	if tag == "" || !strings.HasSuffix(tag, `.t"`) {
		t.Fatalf("ETag = %q", tag)
	}
	if again := fx.get(fx.sess, owner, tag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("an unchanged reel = %d %q, want 304 and no body", again.Code, again.Body.String())
	}
	if other := fx.get(fx.sess, fx.token(topicOther, "human"), tag); other.Code != http.StatusOK {
		t.Errorf("another viewer class must not match the owner's tag: %d", other.Code)
	}
	changed := func(what string) {
		t.Helper()
		rec := fx.get(fx.sess, owner, tag)
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") == tag {
			t.Errorf("%s: %d, tag %q (was %q); want 200 and a new tag", what, rec.Code, rec.Header().Get("ETag"), tag)
		}
		tag = rec.Header().Get("ETag")
	}
	fx.event(fx.sess, "tool_call", map[string]any{"tool": "Bash", "call_id": "c1"})
	changed("a new event")
	if _, err := db.InsertArtifact(context.Background(), fx.pool, db.ArtifactRow{ID: "art-topics-1", SessionID: fx.sess, Name: "a.md", Size: 1, ContentType: "text/markdown", SHA256: "x"}, 50); err != nil {
		t.Fatal(err)
	}
	changed("a new file")
	if _, err := db.DeleteArtifact(context.Background(), fx.pool, fx.sess, "art-topics-1"); err != nil {
		t.Fatal(err)
	}
	changed("a deleted file")
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET status = 'stopped', ended_at = now() WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	rec := fx.get(fx.sess, owner, tag)
	if rec.Code != http.StatusOK {
		t.Fatalf("the session's end must bust the tag: %d", rec.Code)
	}
	if r := decodeReel(t, rec); r.Topics[0].Status != "ended" {
		t.Errorf("after the end the open topic is ended: %+v", r.Topics[0])
	}
}

// A done's commits link to the session's repository: the clone URL it ran with, else the origin its
// daemon reports for its folder; neither known, the url is null. Learning the repository busts the tag.
func TestGetTopicsCommitLinks(t *testing.T) {
	fx := newTopicFx(t)
	owner := fx.token(topicOwner, "human")
	fx.post(fx.sess, fx.sessTok, topicStart("Build x"))
	if code, b := fx.post(fx.sess, fx.sessTok, map[string]any{"action": "done", "commits": []map[string]string{{"sha": "1a2b3c4"}}}); code != http.StatusCreated {
		t.Fatalf("done = %d %v", code, b)
	}
	url := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		r := decodeReel(t, rec)
		items := r.Topics[0].Items
		done := items[len(items)-1]
		if done.Type != "done" || len(done.Commits) != 1 || done.Commits[0].Sha != "1a2b3c4" {
			t.Fatalf("done row = %+v", done)
		}
		if done.Commits[0].URL == nil {
			return "<null>"
		}
		return *done.Commits[0].URL
	}
	first := fx.get(fx.sess, owner, "")
	if got := url(first); got != "<null>" {
		t.Errorf("no repository known: url = %q, want null", got)
	}
	tag := first.Header().Get("ETag")

	dc := &DaemonConn{ID: topicDaemonID}
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "gitlab", FullName: "example-group/app"}}, nil)
	fx.api.hub.Register(dc)
	rec := fx.get(fx.sess, owner, tag)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == tag {
		t.Fatalf("learning the repository must bust the tag: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if got := url(rec); got != "https://gitlab.com/example-group/app/-/commit/1a2b3c4" {
		t.Errorf("the daemon's origin: url = %q", got)
	}

	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET git_url = 'https://github.com/example-org/example-repo.git' WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if got := url(fx.get(fx.sess, owner, "")); got != "https://github.com/example-org/example-repo/commit/1a2b3c4" {
		t.Errorf("the clone URL comes first: url = %q", got)
	}
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET git_url = 'https://git.unknown.example/o/r.git' WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	fx.api.hub.UnregisterIfCurrent(dc)
	if got := url(fx.get(fx.sess, owner, "")); got != "<null>" {
		t.Errorf("a host no provider knows: url = %q, want null", got)
	}
}

// The bar of spec §5: a 100k-event session builds in under 200 ms. The seed repeats a 50-event turn
// (a person message, a topic marker, a subagent started and done, the turn's end, idle, tool results)
// starting at seq 1, so each of the 2000 turns is one marked topic.
func TestGetTopicsOnALongSession(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 100k events")
	}
	fx := newTopicFx(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, g, gen_random_uuid(),
		       CASE (g - 1) % 50 WHEN 0 THEN 'user_message' WHEN 1 THEN 'topic' WHEN 2 THEN 'subagent_started'
		                   WHEN 3 THEN 'subagent_done' WHEN 4 THEN 'turn_done' WHEN 5 THEN 'status_changed' ELSE 'tool_result' END,
		       CASE (g - 1) % 50 WHEN 0 THEN jsonb_build_object('text', 'Task ' || g, 'source', 'chat')
		                   WHEN 1 THEN jsonb_build_object('id', 'tp_' || g, 'title', 'Task ' || (g - 1), 'action', 'start')
		                   WHEN 2 THEN jsonb_build_object('child_id', 'A' || g, 'agent_type', 'general-purpose', 'summary', 'step ' || g)
		                   WHEN 3 THEN jsonb_build_object('child_id', 'A' || (g - 1), 'summary', 'ok', 'usage', jsonb_build_object('output_tokens', 5))
		                   WHEN 4 THEN '{}'::jsonb
		                   WHEN 5 THEN jsonb_build_object('status', 'idle')
		                   ELSE jsonb_build_object('call_id', 'c' || g, 'output', repeat('x', 200)) END
		  FROM generate_series(1, 100000) AS g`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `ANALYZE agent_events`); err != nil {
		t.Fatal(err)
	}
	owner := fx.token(topicOwner, "human")
	best := time.Hour
	var rec *httptest.ResponseRecorder
	for range 3 {
		began := time.Now()
		rec = fx.get(fx.sess, owner, "")
		if d := time.Since(began); d < best {
			best = d
		}
	}
	r := decodeReel(t, rec)
	if len(r.Topics) != 2000 || r.ThroughSeq != 100000 || r.Unmarked != nil {
		t.Errorf("topics = %d, through %d, unmarked %+v; want 2000, 100000 and none", len(r.Topics), r.ThroughSeq, r.Unmarked)
	}
	t.Logf("GET /topics on 100k events: best of 3 = %v", best)
	// The shared CI database is too noisy for the 200 ms bar, so it is opt-in; the 2 s ceiling only
	// catches a plan regression (TestReelQueryUsesThePartialIndex is the deterministic check).
	bar := 2 * time.Second
	if os.Getenv("BLERG_PERF_ASSERT") == "1" {
		bar = 200 * time.Millisecond
	}
	if best > bar {
		t.Errorf("building the reel took %v, over the %v bar", best, bar)
	}
}
