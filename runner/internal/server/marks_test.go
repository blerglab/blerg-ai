package server

// The History feed's marks (marks.go) and strokes (topics.go's GET) against a real database.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func (fx *topicFx) mark(sid, token string, body any) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid+"/marks", bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (fx *topicFx) markEvents(sid string) []agent.MarkPayload {
	fx.t.Helper()
	rows, err := fx.pool.Query(context.Background(), `SELECT payload::text FROM agent_events WHERE session_id = $1 AND kind = 'mark' ORDER BY seq`, sid)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer rows.Close()
	var out []agent.MarkPayload
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			fx.t.Fatal(err)
		}
		var p agent.MarkPayload
		_ = json.Unmarshal([]byte(raw), &p)
		out = append(out, p)
	}
	return out
}

type strokeReply struct {
	Strokes []struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Source     string `json:"source"`
		By         string `json:"by"`
		RecordedAs string `json:"recorded_as"`
		Text       string `json:"text"`
		State      string `json:"state"`
		Outcome    string `json:"outcome"`
		TopicID    string `json:"topic_id"`
		Backdated  bool   `json:"backdated"`
		Jump       *struct {
			EventID    string `json:"event_id"`
			ProposalID string `json:"proposal_id"`
		} `json:"jump"`
		Commits []struct {
			Sha string `json:"sha"`
		} `json:"commits"`
		Card *struct {
			Number  int    `json:"number"`
			BoardID string `json:"board_id"`
		} `json:"card"`
	} `json:"strokes"`
}

func (fx *topicFx) strokes(sid string) strokeReply {
	fx.t.Helper()
	rec := fx.get(sid, fx.token(topicOwner, "human"), "")
	if rec.Code != http.StatusOK {
		fx.t.Fatalf("GET topics = %d %s", rec.Code, rec.Body.String())
	}
	var r strokeReply
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		fx.t.Fatal(err)
	}
	return r
}

// The session's own token marks; the mark is an agent event, fanned out, with its text cleaned.
func TestPostMarkWithTheSessionToken(t *testing.T) {
	fx := newTopicFx(t)
	code, body := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "decision", "by": "person", "text": "Keep <b>v1</b>\u202e API\nfor now", "commits": []string{"ABCDEF1"}, "card": 115, "board_id": "board-1"})
	if code != http.StatusCreated || body["recorded"] != float64(1) {
		t.Fatalf("mark = %d %v", code, body)
	}
	select {
	case msg := <-fx.events:
		if !strings.Contains(string(msg), `"kind":"mark"`) {
			t.Errorf("fan-out = %s", msg)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the mark was not fanned out")
	}
	got := fx.markEvents(fx.sess)
	if len(got) != 1 || got[0].Text != "Keep <b>v1</b> API for now" || got[0].By != "person" || got[0].PostedBy != "agent" ||
		got[0].Card != 115 || got[0].BoardID != "board-1" || len(got[0].Commits) != 1 || got[0].Commits[0].Sha != "abcdef1" || !strings.HasPrefix(got[0].ID, "mk_") {
		t.Errorf("stored = %+v", got)
	}
	s := fx.strokes(fx.sess).Strokes
	// The agent posted it: the agent's line, recorded as the person's ruling.
	if len(s) != 1 || s[0].Source != "mark" || s[0].Kind != "decision" || s[0].By != "agent" || s[0].RecordedAs != "person" || s[0].Jump == nil || s[0].Jump.EventID != s[0].ID || s[0].Card.Number != 115 {
		t.Errorf("strokes = %+v", s)
	}
	// Without --by the agent's mark is the agent's own.
	if code, _ := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "decision", "text": "Use the cache"}); code != http.StatusCreated {
		t.Fatal(code)
	}
	if got := fx.markEvents(fx.sess); got[1].By != "agent" {
		t.Errorf("default by = %+v", got[1])
	}
}

// Who may mark: the session's own token, the daemon token, the owner signed in. Not another session
// (a child marking its parent), not another person, not an agent token, not nobody.
func TestPostMarkCredentials(t *testing.T) {
	fx := newTopicFx(t)
	ctx := context.Background()
	child := newUUID()
	if err := db.InsertSession(ctx, fx.pool, child, topicDaemonID, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET kind = 'agent', parent_session_id = $2 WHERE id = $1`, child, fx.sess); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, fx.pool, child, topicOwner); err != nil {
		t.Fatal(err)
	}
	childTok, err := db.MintSessionToken(ctx, fx.pool, child, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"kind": "finding", "text": "x"}
	for _, c := range []struct {
		name  string
		sid   string
		token string
		want  int
	}{
		{"a child's token on its parent", fx.sess, childTok, http.StatusForbidden},
		{"another session's token", fx.sess, fx.otherTok, http.StatusForbidden},
		{"no credential", fx.sess, "", http.StatusUnauthorized},
		{"another person", fx.sess, fx.token(topicOther, "human"), http.StatusNotFound},
		{"an agent token", fx.sess, fx.token(topicOwner, "agent"), http.StatusForbidden},
		{"an ended session", fx.ended, fx.token(topicOwner, "human"), http.StatusConflict},
		{"the child's own token", child, childTok, http.StatusCreated},
		{"the daemon token", fx.sess, topicDaemonToken, http.StatusCreated},
		{"the owner", fx.sess, fx.token(topicOwner, "human"), http.StatusCreated},
	} {
		if code, b := fx.mark(c.sid, c.token, body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, b, c.want)
		}
	}
	got := fx.markEvents(fx.sess)
	if len(got) != 2 || got[1].PostedBy != "owner" || got[1].By != "person" {
		t.Errorf("parent's marks = %+v", got)
	}
}

// An import records each line once (sent twice, the second records nothing), back-dated: each line
// sits at its own time, jumps to the person's message before it, and draws no chat row of its own.
func TestPostMarkImport(t *testing.T) {
	fx := newTopicFx(t)
	fx.event(fx.sess, "user_message", agent.UserMessagePayload{Text: "Let's plan the release", Source: "chat"})
	past := time.Now().Add(time.Hour * -1).UTC()
	lines := []map[string]any{
		{"at": past.Format("2006-01-02T15:04Z"), "kind": "milestone", "text": "v1 cut", "commit": "1a2b3c4d"},
		{"at": past.Add(time.Minute).Format(time.RFC3339), "kind": "decision", "by": "owner", "text": "Ship on Friday", "card": 7},
		{"at": past.Add(2 * time.Minute).Format(time.RFC3339), "kind": "decision", "by": "agent", "text": "Use the old index"},
	}
	code, body := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": lines})
	if code != http.StatusCreated || body["recorded"] != float64(3) {
		t.Fatalf("import = %d %v", code, body)
	}
	code, body = fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": lines})
	if code != http.StatusCreated || body["recorded"] != float64(0) || body["duplicates"] != float64(3) {
		t.Fatalf("second import = %d %v", code, body)
	}
	if got := fx.markEvents(fx.sess); len(got) != 3 || got[1].By != "person" || got[0].At == "" {
		t.Errorf("stored = %+v", got)
	}
	s := fx.strokes(fx.sess).Strokes
	if len(s) != 3 || s[0].Text != "Use the old index" || s[0].By != "agent" || s[0].RecordedAs != "" || s[1].RecordedAs != "person" || s[2].Kind != "milestone" || !s[0].Backdated {
		t.Fatalf("strokes = %+v", s)
	}
	for _, x := range s {
		if x.Jump != nil {
			t.Errorf("a mark older than every message jumps to %+v", x.Jump)
		}
	}
	// A bad line refuses the whole import, naming it.
	if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": []map[string]any{{"kind": "decision", "text": "ok"}, {"kind": "nope", "text": "x"}}}); code != http.StatusBadRequest || !strings.HasPrefix(b["error"].(string), "mark 2:") {
		t.Errorf("bad line = %d %v", code, b)
	}
	if got := fx.markEvents(fx.sess); len(got) != 3 {
		t.Errorf("a refused import recorded %d", len(got)-3)
	}
}

// What Blerg already records becomes strokes: an approval, a proposal decided, a review decision, a
// topic done with commits, an ask answered and one closed without an answer.
func TestStrokesFromRecordedEventsOverHTTP(t *testing.T) {
	fx := newTopicFx(t)
	ctx := context.Background()
	if code, b := fx.post(fx.sess, fx.sessTok, topicStart("Ship it")); code != http.StatusCreated {
		t.Fatalf("start = %d %v", code, b)
	}
	fx.event(fx.sess, "delegation_request", map[string]any{"request_id": "d1", "engine": "codex", "title": "Review the diff"})
	fx.event(fx.sess, "delegation_decided", map[string]any{"request_id": "d1", "status": "denied"})
	fx.event(fx.sess, "proposal_request", map[string]any{"proposal_id": "p1", "kind": "board_file"})
	fx.event(fx.sess, "proposal_decided", map[string]any{"proposal_id": "p1", "kind": "board_file", "state": "rejected"})
	fx.event(fx.sess, "review_requested", map[string]any{"request_id": "r1", "artifact_name": "plan.md", "artifact_version": 1, "ask": "OK?", "state": "pending"})
	fx.event(fx.sess, "review_decided", map[string]any{"request_id": "r1", "state": "approved"})
	var open, closed string
	for _, id := range []*string{&open, &closed} {
		if err := fx.pool.QueryRow(ctx, `INSERT INTO messages (session_id, kind, body, status) VALUES ($1, 'ask', 'q', 'open') RETURNING id::text`, fx.sess).Scan(id); err != nil {
			t.Fatal(err)
		}
		fx.event(fx.sess, "ask", map[string]any{"body": "Question " + *id, "message_id": *id})
	}
	fx.event(fx.sess, "user_message", map[string]any{"text": "go", "source": "ask_answer", "message_id": open})
	if _, err := fx.pool.Exec(ctx, `UPDATE messages SET status = 'answered', answer = 'go' WHERE id = $1`, open); err != nil {
		t.Fatal(err)
	}
	if code, b := fx.post(fx.sess, fx.sessTok, map[string]any{"action": "done", "outcome": "Merged", "commits": []map[string]string{{"sha": "abc1234"}}}); code != http.StatusCreated {
		t.Fatalf("done = %d %v", code, b)
	}
	first := fx.get(fx.sess, fx.token(topicOwner, "human"), "")
	etag := first.Header().Get("ETag")
	if !strings.Contains(first.Body.String(), `"strokes_truncated":0`) {
		t.Errorf("the reply does not say how many strokes it left out: %.200s", first.Body.String())
	}
	got := map[string]string{}
	topicID := ""
	// A fold's items are strokes of their own: read them all, folds and items.
	for _, s := range fx.flatStrokes(fx.sess) {
		got[s.Source+":"+s.Text] = s.State + "|" + s.Outcome
	}
	if _, folded := got["ask:2 questions"]; folded {
		t.Errorf("questions never fold: %v", got)
	}
	for _, s := range fx.strokes(fx.sess).Strokes {
		if s.Source == "topic" {
			topicID = s.TopicID
			if s.Kind != "milestone" || len(s.Commits) != 1 {
				t.Errorf("topic done = %+v", s)
			}
		}
		if s.Source == "proposal" && (s.Jump == nil || s.Jump.ProposalID != "p1") {
			t.Errorf("proposal jump = %+v", s.Jump)
		}
	}
	want := map[string]string{
		"delegation:Start a Codex session: Review the diff": "declined|Denied",
		"proposal:File a board card":                        "declined|Rejected",
		"review:Review plan.md v1: OK?":                     "resolved|Approved",
		"ask:Question " + open:                              "resolved|go",
		"ask:Question " + closed:                            "open|",
		"topic:Merged":                                      "|",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if topicID == "" {
		t.Error("the strokes are not in their topic")
	}
	// Closing an ask without an answer writes no event; the ETag still moves and the stroke says so.
	if _, err := fx.pool.Exec(ctx, `UPDATE messages SET status = 'answered', answer = '' WHERE id = $1`, closed); err != nil {
		t.Fatal(err)
	}
	if rec := fx.get(fx.sess, fx.token(topicOwner, "human"), etag); rec.Code != http.StatusOK {
		t.Fatalf("after a close the reel answered %d", rec.Code)
	}
	for _, s := range fx.flatStrokes(fx.sess) {
		if s.Text == "Question "+closed && (s.State != "expired" || s.Outcome != "Closed without an answer") {
			t.Errorf("closed ask = %+v", s)
		}
	}
}

type flatStroke struct {
	Source  string       `json:"source"`
	Text    string       `json:"text"`
	State   string       `json:"state"`
	Outcome string       `json:"outcome"`
	Items   []flatStroke `json:"items"`
}

// flatStrokes is the feed with every fold's items listed after it.
func (fx *topicFx) flatStrokes(sid string) []flatStroke {
	fx.t.Helper()
	rec := fx.get(sid, fx.token(topicOwner, "human"), "")
	var r struct {
		Strokes []flatStroke `json:"strokes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		fx.t.Fatal(err)
	}
	var out []flatStroke
	for _, s := range r.Strokes {
		out = append(out, s)
		out = append(out, s.Items...)
	}
	return out
}

// The cap counts what the session would hold: an import sent again near the cap records nothing and
// is not refused; a request that would pass the cap keeps nothing.
func TestPostMarkCapCountsOnlyNewMarks(t *testing.T) {
	fx := newTopicFx(t)
	base := time.Now().Add(-48 * time.Hour).UTC()
	lines := func(from, n int) []map[string]any {
		var out []map[string]any
		for i := from; i < from+n; i++ {
			out = append(out, map[string]any{"at": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339), "kind": "finding", "text": fmt.Sprintf("line %d", i)})
		}
		return out
	}
	for _, chunk := range [][]map[string]any{lines(0, 200), lines(200, 200), lines(400, 100)} {
		if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": chunk}); code != http.StatusCreated {
			t.Fatalf("import = %d %v", code, b)
		}
	}
	// At the cap (500): the same lines again are all duplicates.
	if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": lines(300, 200)}); code != http.StatusCreated || b["duplicates"] != float64(200) {
		t.Fatalf("re-import at the cap = %d %v", code, b)
	}
	// One new line among duplicates passes the cap: refused, nothing kept.
	mixed := append(lines(450, 50), lines(500, 1)...)
	if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": mixed}); code != http.StatusConflict || !strings.Contains(b["error"].(string), "nothing was recorded") {
		t.Fatalf("over the cap = %d %v", code, b)
	}
	if got := fx.markEvents(fx.sess); len(got) != 500 {
		t.Errorf("held %d marks", len(got))
	}
}

// A body over the cap is a 413 that says so, not "not valid JSON".
func TestPostMarkTooLarge(t *testing.T) {
	fx := newTopicFx(t)
	code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "finding", "text": strings.Repeat("x", maxMarksBody+10)})
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(b["error"].(string), "larger than") {
		t.Fatalf("= %d %v", code, b)
	}
}

// The dedup key covers the chips: the same line with a card added is a new mark.
func TestPostMarkDedupCoversChips(t *testing.T) {
	fx := newTopicFx(t)
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	line := map[string]any{"at": at, "kind": "milestone", "text": "Merged"}
	withCard := map[string]any{"at": at, "kind": "milestone", "text": "Merged", "card": 7}
	withCommit := map[string]any{"at": at, "kind": "milestone", "text": "Merged", "commit": "abc1234"}
	for i, body := range []map[string]any{line, withCard, withCommit, line} {
		code, b := fx.mark(fx.sess, fx.sessTok, body)
		want := float64(1)
		if i == 3 {
			want = 0
		}
		if code != http.StatusCreated || b["recorded"] != want {
			t.Errorf("post %d = %d %v", i, code, b)
		}
	}
}
