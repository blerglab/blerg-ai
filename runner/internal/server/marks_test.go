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
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

func (fx *topicFx) mark(sid, token string, body any, key ...string) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid+"/marks", bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if len(key) > 0 {
		req.Header.Set("Idempotency-Key", key[0])
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
		ClaimedBy   string `json:"claimed_by"`
		FailureType string `json:"type"`
		RerunPassed bool   `json:"rerun_passed"`
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Source      string `json:"source"`
		By          string `json:"by"`
		RecordedAs  string `json:"recorded_as"`
		Text        string `json:"text"`
		State       string `json:"state"`
		Outcome     string `json:"outcome"`
		TopicID     string `json:"topic_id"`
		Backdated   bool   `json:"backdated"`
		Jump        *struct {
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
	// The agent posted it, preserving the person's ownership of the ruling.
	if len(s) != 1 || s[0].Source != "mark" || s[0].Kind != "decision" || s[0].By != "person" || s[0].RecordedAs != "person" || s[0].Jump == nil || s[0].Jump.EventID != s[0].ID || s[0].Card.Number != 115 {
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
	// Main must be able to record beyond the old 500-mark cap.
	if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": lines(500, 1)}); code != http.StatusCreated {
		t.Fatalf("long-lived Main = %d %v", code, b)
	}
	// Seed the remaining capacity without thousands of HTTP requests.
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload)
 SELECT $1, -n, gen_random_uuid(), 'mark', '{"kind":"finding","text":"seed"}'::jsonb
 FROM generate_series(1, $2::int) n`, fx.sess, topics.MaxMarks-501); err != nil {
		t.Fatal(err)
	}
	// At the cap: the same lines again are all duplicates.
	if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": lines(300, 200)}); code != http.StatusCreated || b["duplicates"] != float64(200) {
		t.Fatalf("re-import at the cap = %d %v", code, b)
	}
	// One new line among duplicates passes the cap: refused, nothing kept.
	mixed := append(lines(450, 50), lines(501, 1)...)
	if code, b := fx.mark(fx.sess, fx.sessTok, map[string]any{"marks": mixed}); code != http.StatusConflict || !strings.Contains(b["error"].(string), "nothing was recorded") {
		t.Fatalf("over the cap = %d %v", code, b)
	}
	if got := fx.markEvents(fx.sess); len(got) != topics.MaxMarks {
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

// Authorization precedes even the first body read, including malformed/oversized requests.
type countedMarkBody struct{ reads int }

func (b *countedMarkBody) Read([]byte) (int, error) {
	b.reads++
	return 0, fmt.Errorf("body must remain unread")
}
func TestPostMarkChecksOwnershipBeforeReadingBody(t *testing.T) {
	fx := newTopicFx(t)
	body := &countedMarkBody{}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+fx.sess+"/marks", body)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOther, "human"))
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || body.reads != 0 {
		t.Fatalf("status=%d reads=%d; want private 404 with unread body", rec.Code, body.reads)
	}
}

func TestPostLiveMarkRetryIsIdempotent(t *testing.T) {
	fx := newTopicFx(t)
	post := func(key, text string) (int, marksReply) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+fx.sess+"/marks", strings.NewReader(`{"kind":"milestone","text":"`+text+`"}`))
		req.Header.Set("Authorization", "Bearer "+fx.sessTok)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		var reply marksReply
		_ = json.Unmarshal(rec.Body.Bytes(), &reply)
		return rec.Code, reply
	}
	code, first := post("lost-reply", "Merged")
	if code != http.StatusCreated || first.Recorded != 1 {
		t.Fatalf("first=%d %+v", code, first)
	}
	code, retry := post("lost-reply", "Merged")
	if code != http.StatusCreated || retry.Recorded != 0 || retry.Duplicates != 1 || retry.Marks[0].ID != first.Marks[0].ID {
		t.Fatalf("retry=%d %+v first=%+v", code, retry, first)
	}
	if code, _ := post("lost-reply", "Deployed"); code != http.StatusConflict {
		t.Fatalf("changed request=%d", code)
	}
	if code, next := post("new-mark", "Merged"); code != http.StatusCreated || next.Recorded != 1 {
		t.Fatalf("distinct mark=%d %+v", code, next)
	}
	if got := len(fx.markEvents(fx.sess)); got != 2 {
		t.Fatalf("stored=%d", got)
	}
}

func TestPostMarkKeyedImportKeepsContentDeduplication(t *testing.T) {
	fx := newTopicFx(t)
	body := map[string]any{"kind": "finding", "text": "Imported", "at": "2026-10-21T12:00:00Z"}
	for i, key := range []string{"first-import", "second-import"} {
		code, reply := fx.mark(fx.sess, fx.sessTok, body, key)
		if code != http.StatusCreated || reply["recorded"] != float64(1-i) {
			t.Fatalf("%s = %d %v", key, code, reply)
		}
	}
}

func TestReviewMarkFromWorkerIsStoredAndShown(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, fx.ended); err != nil {
		t.Fatal(err)
	}
	code, body := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "review", "text": "Review complete", "by": "codex", "board_id": "board-a", "card": 7, "verdict": "changes", "findings": []string{"high", "high"}, "target": "abcdef1", "round": 2})
	if code != 201 {
		t.Fatalf("review: %d %v", code, body)
	}
	got := fx.markEvents(fx.sess)
	if len(got) != 1 || got[0].Verdict != "changes" || got[0].Round != 2 || len(got[0].Findings) != 2 {
		t.Fatalf("stored: %+v", got)
	}
	strokes := fx.strokes(fx.sess)
	if len(strokes.Strokes) != 1 || strokes.Strokes[0].Kind != "review" || strokes.Strokes[0].By != "codex" {
		t.Fatalf("history: %+v", strokes)
	}
}

func TestFailureMarkFromWorkerIsStoredAndShown(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, fx.ended); err != nil {
		t.Fatal(err)
	}
	code, body := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "failure", "text": "Gate failed", "board_id": "board-a", "card": 7, "type": "gate", "severity": "high", "suite": "db", "rerun_passed": true})
	if code != 201 {
		t.Fatalf("failure: %d %v", code, body)
	}
	got := fx.markEvents(fx.sess)
	if len(got) != 1 || got[0].FailureType != "gate" || got[0].Severity != "high" || got[0].Suite != "db" || !got[0].RerunPassed {
		t.Fatalf("stored: %+v", got)
	}
	strokes := fx.strokes(fx.sess)
	if len(strokes.Strokes) != 1 || strokes.Strokes[0].FailureType != "gate" || !strokes.Strokes[0].RerunPassed {
		t.Fatalf("history: %+v", strokes)
	}
}

func TestStructuredMarksRequireCardContext(t *testing.T) {
	fx := newTopicFx(t)
	for _, kind := range []string{"review", "failure"} {
		body := map[string]any{"kind": kind, "text": "Unrelated card", "board_id": "unrelated", "card": 7, "by": "codex"}
		if kind == "review" {
			body["verdict"] = "approved"
			body["round"] = 1
			body["target"] = "abcdef1"
		} else {
			body["type"] = "gate"
			body["severity"] = "high"
			body["suite"] = "unit"
		}
		code, reply := fx.mark(fx.sess, fx.sessTok, body)
		if code != http.StatusForbidden {
			t.Errorf("%s unrelated card = %d %v", kind, code, reply)
		}
	}
	if len(fx.markEvents(fx.sess)) != 0 {
		t.Fatal("unauthorized marks were stored")
	}
}

func TestFailurePersonAttributionRemainsAnAgentClaim(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, fx.sess); err != nil {
		t.Fatal(err)
	}
	code, reply := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "failure", "text": "Reported by person", "by": "person", "board_id": "board-a", "card": 7, "type": "test", "severity": "low", "suite": "unit"})
	if code != 201 {
		t.Fatalf("failure = %d %v", code, reply)
	}
	got := fx.markEvents(fx.sess)
	if len(got) != 1 || got[0].PostedBy != "agent" || got[0].By != "agent" || got[0].ClaimedBy != "person" {
		t.Fatalf("claim became authority: %+v", got)
	}
}

func TestStructuredMarksRejectAnotherOwnersAncestor(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.privSess); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',7,'pin',now())`, fx.privSess); err != nil {
		t.Fatal(err)
	}
	code, reply := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "failure", "text": "Unrelated ancestor", "board_id": "board-a", "card": 7, "type": "gate", "severity": "high", "suite": "unit"})
	if code != 403 {
		t.Fatalf("another owner's ancestor authorized mark: %d %v", code, reply)
	}
}

func TestStructuredMarksAllowBoardGrantAndKeepPersonReviewAsClaim(t *testing.T) {
	fx := newTopicFx(t)
	grant := db.MCPGrant{SessionID: fx.sess, ConnectionID: newUUID(), Name: "Board", AccountID: topicOwner, Tools: map[string]db.MCPGrantTool{}, ProofKind: "session_id", ProofValue: fx.sess, TokenHash: []byte("structured-mark-fixture"), Builtin: "board", BuiltinRef: "board-a"}
	if err := db.InsertMCPGrants(context.Background(), fx.pool, []db.MCPGrant{grant}); err != nil {
		t.Fatal(err)
	}
	code, reply := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "review", "text": "Claimed person review", "by": "person", "board_id": "board-a", "card": 7, "verdict": "approved", "round": 1, "target": "abcdef1"})
	if code != 201 {
		t.Fatalf("board grant review: %d %v", code, reply)
	}
	got := fx.markEvents(fx.sess)
	if len(got) != 1 || got[0].PostedBy != "agent" || got[0].By != "agent" || got[0].ClaimedBy != "person" {
		t.Fatalf("review claim became authority: %+v", got)
	}
	strokes := fx.strokes(fx.sess)
	if len(strokes.Strokes) != 1 || strokes.Strokes[0].By != "agent" || strokes.Strokes[0].ClaimedBy != "person" || strokes.Strokes[0].RecordedAs != "" {
		t.Fatalf("history claim became authority: %+v", strokes)
	}
}

func TestStructuredMarksRespectLoweredLaunchBoard(t *testing.T) {
	fx := newTopicFx(t)
	grant := db.MCPGrant{SessionID: fx.sess, ConnectionID: newUUID(), Name: "Board", AccountID: topicOwner, Tools: map[string]db.MCPGrantTool{}, ProofKind: "session_id", ProofValue: fx.sess, TokenHash: []byte("lowered-mark-fixture"), Builtin: "board", BuiltinRef: "board-a"}
	if err := db.InsertMCPGrants(context.Background(), fx.pool, []db.MCPGrant{grant}); err != nil {
		t.Fatal(err)
	}
	_, err := db.InsertProposalGrant(context.Background(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", Level: db.BoardLevelPropose, ProofSessionID: fx.sess, CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	code, reply := fx.mark(fx.sess, fx.sessTok, map[string]any{"kind": "failure", "text": "Lowered board", "board_id": "board-a", "card": 7, "type": "gate", "severity": "high", "suite": "unit"})
	if code != 403 {
		t.Fatalf("lowered board authorized mark: %d %v", code, reply)
	}
}

func TestMarkCapacityWarning(t *testing.T) {
	for _, tc := range []struct {
		before, after int
		want          bool
	}{
		{0, 1, false}, {39999, 40000, true}, {40000, 40001, false},
		{44999, 45000, true}, {47499, 47500, true}, {39999, 47600, true},
		{50000, 50000, false},
	} {
		text := markCapacityWarning(tc.before, tc.after)
		if (text != "") != tc.want {
			t.Errorf("%d → %d: %q", tc.before, tc.after, text)
		}
		if tc.want && !strings.Contains(text, "50000") {
			t.Errorf("missing limit: %q", text)
		}
	}
	if !reservedMessageSource(markCapacitySource) || answersAsk(markCapacitySource) {
		t.Fatal("capacity warning must be a protected notice, never an answer")
	}
}

// Exercise the actual mark route and delivery transport at the first warning threshold.
func TestPostMarkCapacityNotice(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	mux := http.NewServeMux()
	fx.api.RegisterMarkRoutes(mux)
	client := &topicFx{t: t, mux: mux}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload)
 SELECT $1, -n, gen_random_uuid(), 'mark', '{"kind":"finding","text":"seed"}'::jsonb
 FROM generate_series(1, 39999) n`, fx.parent); err != nil {
		t.Fatal(err)
	}
	if code, b := client.mark(fx.parent, fx.parentTok, map[string]any{"kind": "finding", "text": "threshold"}); code != http.StatusCreated {
		t.Fatalf("%d %v", code, b)
	}
	note := fx.parentNote(fx.parent, time.Second)
	if note.Source != markCapacitySource || !strings.Contains(note.Text, "10000 remaining") {
		t.Fatalf("notice = %+v", note)
	}
	if code, b := client.mark(fx.parent, fx.parentTok, map[string]any{"kind": "finding", "text": "after threshold"}); code != http.StatusCreated {
		t.Fatalf("%d %v", code, b)
	}
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("repeated warning: %s", raw)
	default:
	}
}

func TestHistoryPagesTwentyThousandMarks(t *testing.T) {
	fx := newTopicFx(t)
	var pending string
	if err := fx.pool.QueryRow(t.Context(), `INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','Still waiting','open') RETURNING id::text`, fx.sess).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	fx.event(fx.sess, "ask", map[string]any{"message_id": pending, "body": "Still waiting"})
	fx.event(fx.sess, "review_requested", map[string]any{"request_id": "old-review", "artifact_name": "old.md"})
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts)
 SELECT $1,n+2,gen_random_uuid(),'mark',jsonb_build_object('kind','finding','text','mark '||n),now()+n*interval '1 second'
 FROM generate_series(1,20000) n`, fx.sess); err != nil {
		t.Fatal(err)
	}
	read := func(cursor, etag string) (topicsReply, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/topics?cursor="+cursor, nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %s: %d %.200s", cursor, rec.Code, rec.Body.String())
		}
		var reply topicsReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply, rec.Header().Get("ETag")
	}
	first, tag := read("", "")
	if first.NextCursor == "" || len(first.Strokes) > 301 {
		t.Fatalf("initial page: cursor=%q rows=%d", first.NextCursor, len(first.Strokes))
	}
	if first.Strokes[0].Text != "mark 20000" {
		t.Fatalf("newest = %+v", first.Strokes[0])
	}
	fx.event(fx.sess, "mark", map[string]any{"kind": "finding", "text": "Recorded after the snapshot"})
	fx.event(fx.sess, "review_decided", map[string]any{"request_id": "old-review", "state": "approved"})
	second, _ := read(first.NextCursor, tag) // A different page must not answer 304.
	if second.ThroughSeq != first.ThroughSeq {
		t.Fatal("new events shifted the page snapshot")
	}
	for _, st := range second.Strokes {
		if st.Text == "Recorded after the snapshot" {
			t.Fatal("new mark leaked into an older snapshot")
		}
	}
	firstMarks := map[string]bool{}
	for _, st := range first.Strokes {
		if st.Source == "mark" {
			firstMarks[st.ID] = true
		}
	}
	for _, st := range second.Strokes {
		if st.Source == "mark" && firstMarks[st.ID] {
			t.Fatal("duplicate mark across pages")
		}
	}
	oldest, oldestTag := read(fmt.Sprintf("%d:301", first.ThroughSeq), "")
	resolved, waiting := false, false
	for _, st := range oldest.Strokes {
		if st.Source == "review" && st.Text == "Review old.md" {
			resolved = st.State == "resolved"
		}
		if st.Source == "ask" && st.Text == "Still waiting" {
			waiting = st.State == "open"
		}
	}
	if !resolved || !waiting {
		t.Fatalf("cross-page lifecycle: resolved=%v pending=%v", resolved, waiting)
	}
	waiting = false
	for _, st := range first.Strokes {
		if st.Text == "Still waiting" && st.State == "open" {
			waiting = true
		}
	}
	if !waiting {
		t.Fatal("old pending ask disappeared from the initial page")
	}
	if oldest.NextCursor != "" {
		t.Fatalf("unexpected older cursor: %s", oldest.NextCursor)
	}
	fx.event(fx.sess, "review_decided", map[string]any{"request_id": "old-review", "state": "changes_requested"})
	refreshed, _ := read(fmt.Sprintf("%d:301", first.ThroughSeq), oldestTag)
	declined := false
	for _, st := range refreshed.Strokes {
		if st.Source == "review" && st.State == "declined" {
			declined = true
		}
	}
	if !declined {
		t.Fatal("older-page ETag hid a newer review decision")
	}

}

func TestHistoryRejectsInvalidPageCursor(t *testing.T) {
	fx := newTopicFx(t)
	fx.event(fx.sess, "mark", map[string]any{"kind": "finding", "text": "first"})
	for _, cursor := range []string{"junk", "1:0", "2:1", "1:2", "-1:1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.sess+"/topics?cursor="+cursor, nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: got %d", cursor, rec.Code)
		}
	}
}
