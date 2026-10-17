package server

// The checklist and the pinned tickets (worklist.go) against a real database: the agent writes with
// its session token, the events carry the full state and the change, and /live agrees with them.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newWorkFx(t *testing.T) *topicFx {
	fx := newLiveFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	return fx
}

func (fx *topicFx) postWork(path, sid, token string, body any) (int, map[string]any) {
	var raw []byte
	if s, ok := body.(string); ok {
		raw = []byte(s)
	} else {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid+"/"+path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (fx *topicFx) kindPayloads(sid, kind string) []map[string]any {
	fx.t.Helper()
	rows, err := fx.pool.Query(context.Background(), `SELECT payload::text FROM agent_events WHERE session_id = $1 AND kind = $2 ORDER BY seq`, sid, kind)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			fx.t.Fatal(err)
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(raw), &p)
		out = append(out, p)
	}
	return out
}

func itemsLine(v any) string {
	list, _ := v.([]any)
	var parts []string
	for _, it := range list {
		m, _ := it.(map[string]any)
		mark := "☐"
		if m["done"] == true {
			mark = "☑"
		}
		parts = append(parts, fmt.Sprintf("%s%v", mark, m["text"]))
	}
	return strings.Join(parts, " ")
}

func TestPostChecklistRecordsTheFullListAndTheChange(t *testing.T) {
	fx := newWorkFx(t)
	code, body := fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "set", "items": []string{"Read", "Write tests", "Ship"}})
	if code != http.StatusCreated || itemsLine(body["items"]) != "☐Read ☐Write tests ☐Ship" {
		t.Fatalf("set = %d %v", code, body)
	}
	select {
	case msg := <-fx.events:
		if !strings.Contains(string(msg), `"kind":"checklist"`) {
			t.Errorf("fan-out = %s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the checklist event was not fanned out")
	}
	code, body = fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "tick", "text": "Write tests"})
	changed, _ := body["changed"].(map[string]any)
	if code != http.StatusCreated || itemsLine(body["items"]) != "☐Read ☑Write tests ☐Ship" || changed["index"] != float64(2) || changed["done"] != true {
		t.Fatalf("tick = %d %v", code, body)
	}
	if code, body = fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "tick", "index": 9}); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "no item 9") {
		t.Fatalf("tick 9 = %d %v", code, body)
	}
	if code, body = fx.postWork("checklist", fx.sess, fx.sessTok, `{"op":"set","items":[1]}`); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "items must be a list of strings") {
		t.Fatalf("non-string = %d %v", code, body)
	}
	got := fx.kindPayloads(fx.sess, "checklist")
	if len(got) != 2 || got[0]["op"] != "set" || got[1]["op"] != "tick" || got[1]["changed"] == nil {
		t.Fatalf("events = %v", got)
	}
	// /live shows the newest list.
	r := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	var live struct {
		Checklist *struct {
			Items   []map[string]any `json:"items"`
			EventID string           `json:"event_id"`
		} `json:"checklist"`
		Tickets []any `json:"tickets"`
		Ticket  any   `json:"ticket"`
		Plan    any   `json:"plan"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &live); err != nil || live.Checklist == nil || len(live.Checklist.Items) != 3 || live.Checklist.Items[1]["done"] != true || live.Checklist.EventID == "" {
		t.Fatalf("live = %s", r.Body.String())
	}
	if live.Tickets == nil || len(live.Tickets) != 0 || live.Ticket != nil || live.Plan != nil {
		t.Fatalf("live = %s", r.Body.String())
	}
	// A clear empties the section.
	fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "clear"})
	r = fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	if !strings.Contains(r.Body.String(), `"checklist":null`) {
		t.Fatalf("after clear = %s", r.Body.String())
	}
}

func TestPostChecklistCredentialsEndedAndCap(t *testing.T) {
	fx := newWorkFx(t)
	set := map[string]any{"op": "set", "items": []string{"a"}}
	for name, c := range map[string]struct {
		sid, tok string
		want     int
	}{
		"another session's token": {fx.sess, fx.otherTok, http.StatusForbidden},
		"no credential":           {fx.sess, "", http.StatusUnauthorized},
		"a person's token":        {fx.sess, fx.token(topicOwner, "human"), http.StatusUnauthorized},
		"the daemon token":        {fx.sess, topicDaemonToken, http.StatusCreated},
	} {
		if code, body := fx.postWork("checklist", c.sid, c.tok, set); code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, body, c.want)
		}
	}
	if code, body := fx.postWork("checklist", fx.ended, topicDaemonToken, set); code != http.StatusConflict || !strings.Contains(fmt.Sprint(body["error"]), "ended") {
		t.Errorf("ended: %d %v", code, body)
	}
	if code, _ := fx.postWork("checklist", fx.sess, fx.sessTok, `{"op":"set","items":["`+strings.Repeat("a", 70<<10)+`"]}`); code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body: %d", code)
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, 1000 + g, gen_random_uuid(), 'checklist', '{"items":[],"op":"clear"}' FROM generate_series(1, 499) g`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if code, body := fx.postWork("checklist", fx.sess, fx.sessTok, set); code != http.StatusTooManyRequests {
		t.Errorf("past the cap: %d %v", code, body)
	}
}

// Two ticks at once both land: the read and the append are one step under the session's lock.
func TestPostChecklistConcurrentTicksLoseNothing(t *testing.T) {
	fx := newWorkFx(t)
	items := make([]string, 8)
	for i := range items {
		items[i] = fmt.Sprintf("item %d", i+1)
	}
	fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "set", "items": items})
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "tick", "index": n})
		}(i + 1)
	}
	wg.Wait()
	got := fx.kindPayloads(fx.sess, "checklist")
	if last := itemsLine(got[len(got)-1]["items"]); strings.Contains(last, "☐") {
		t.Fatalf("a tick was lost: %s", last)
	}
}

func jiraTicket(status, title string) map[string]any {
	return map[string]any{"source": "jira", "key": "ABC-1", "url": "https://jira.example.test/browse/ABC-1", "title": title,
		"status": status, "description": "<script>alert(1)</script>\n**bold**", "labels": []string{"ui"}, "acceptance": []string{"Saves"}}
}

func TestPostTicketsPinUpdateUnpin(t *testing.T) {
	fx := newWorkFx(t)
	code, body := fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin", "ticket": jiraTicket("Open", "Crash on save")})
	if code != http.StatusCreated || body["action"] != "pin" || body["previous"] != nil {
		t.Fatalf("pin = %d %v", code, body)
	}
	code, body = fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin", "ticket": map[string]any{"source": "bugzilla", "key": "ABC-1", "title": "Same key, other tracker"}})
	if code != http.StatusCreated || body["action"] != "pin" {
		t.Fatalf("second tracker = %d %v", code, body)
	}
	code, body = fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "pin", "ticket": jiraTicket("In Progress", "Crash on save")})
	prev, _ := body["previous"].(map[string]any)
	if code != http.StatusCreated || body["action"] != "update" || prev["status"] != "Open" || prev["title"] != "Crash on save" {
		t.Fatalf("re-pin = %d %v", code, body)
	}
	pinned, _ := body["pinned"].([]any)
	if len(pinned) != 2 || pinned[0].(map[string]any)["source"] != "jira" {
		t.Fatalf("pinned = %v", pinned)
	}
	// /live: the current one in full, both as summaries.
	r := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	var live struct {
		Tickets []map[string]any `json:"tickets"`
		Ticket  map[string]any   `json:"ticket"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &live)
	if len(live.Tickets) != 2 || live.Ticket["key"] != "ABC-1" || live.Ticket["source"] != "jira" || live.Ticket["status"] != "In Progress" ||
		!strings.Contains(fmt.Sprint(live.Ticket["description"]), "<script>") || live.Ticket["event_id"] == "" {
		t.Fatalf("live = %s", r.Body.String())
	}
	// A key two trackers share needs its source.
	if code, body = fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin", "key": "ABC-1"}); code != http.StatusConflict || !strings.Contains(fmt.Sprint(body["error"]), "source") {
		t.Fatalf("ambiguous unpin = %d %v", code, body)
	}
	code, body = fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin"})
	tk, _ := body["ticket"].(map[string]any)
	if code != http.StatusCreated || body["action"] != "unpin" || tk["source"] != "jira" || tk["description"] != nil {
		t.Fatalf("unpin current = %d %v", code, body)
	}
	code, body = fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin", "source": "bugzilla", "key": "ABC-1"})
	if code != http.StatusCreated || len(body["pinned"].([]any)) != 0 {
		t.Fatalf("unpin by id = %d %v", code, body)
	}
	if code, body = fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin"}); code != http.StatusConflict {
		t.Fatalf("nothing pinned = %d %v", code, body)
	}
	r = fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	if !strings.Contains(r.Body.String(), `"tickets":[]`) || !strings.Contains(r.Body.String(), `"ticket":null`) {
		t.Fatalf("live after = %s", r.Body.String())
	}
}

func TestPostTicketsRefusals(t *testing.T) {
	fx := newWorkFx(t)
	for name, c := range map[string]struct {
		body any
		want int
		says string
	}{
		"javascript url":  {map[string]any{"action": "pin", "ticket": map[string]any{"source": "x", "key": "A", "title": "t", "url": "javascript:alert(1)"}}, http.StatusBadRequest, "url"},
		"number key":      {`{"action":"pin","ticket":{"source":"x","key":5,"title":"t"}}`, http.StatusBadRequest, "key must be a string"},
		"no ticket":       {map[string]any{"action": "pin"}, http.StatusBadRequest, "object"},
		"bad action":      {map[string]any{"action": "stick"}, http.StatusBadRequest, "action"},
		"not json":        {`{`, http.StatusBadRequest, "JSON"},
		"too large":       {`{"action":"pin","ticket":{"description":"` + strings.Repeat("a", 300<<10) + `"}}`, http.StatusRequestEntityTooLarge, "KiB"},
		"deeply nested":   {`{"action":"pin","ticket":{"source":"x","key":"A","title":"t","extra":` + strings.Repeat("[", 5000) + strings.Repeat("]", 5000) + `}}`, http.StatusCreated, ""},
		"action a number": {`{"action":1}`, http.StatusBadRequest, "action must be a string"},
	} {
		code, body := fx.postWork("tickets", fx.sess, fx.sessTok, c.body)
		if code != c.want || !strings.Contains(fmt.Sprint(body["error"]), c.says) {
			t.Errorf("%s: %d %v, want %d %q", name, code, body, c.want, c.says)
		}
	}
	// At most five at once.
	fx2 := newWorkFx(t)
	for i := 0; i < 5; i++ {
		if code, body := fx2.postWork("tickets", fx2.sess, fx2.sessTok, map[string]any{"action": "pin", "ticket": map[string]any{"source": "github", "key": fmt.Sprint(i), "title": "t"}}); code != http.StatusCreated {
			t.Fatalf("pin %d: %d %v", i, code, body)
		}
	}
	if code, body := fx2.postWork("tickets", fx2.sess, fx2.sessTok, map[string]any{"action": "pin", "ticket": map[string]any{"source": "github", "key": "6", "title": "t"}}); code != http.StatusConflict {
		t.Fatalf("sixth: %d %v", code, body)
	}
	if code, _ := fx2.postWork("tickets", fx2.ended, topicDaemonToken, map[string]any{"action": "unpin"}); code != http.StatusConflict {
		t.Fatalf("ended: %d", code)
	}
}

// The plan is the main agent's Claude Code task tools, folded from the whole log.
func TestLiveCarriesTheEnginePlan(t *testing.T) {
	fx := newWorkFx(t)
	ctx := context.Background()
	for i, p := range []struct{ kind, payload string }{
		{"tool_call", `{"tool":"TaskCreate","call_id":"c1","input":{"subject":"Write tests","activeForm":"Writing tests"}}`},
		{"tool_result", `{"call_id":"c1","output":"Task #1 created successfully: Write tests"}`},
		{"tool_call", `{"tool":"TaskCreate","call_id":"c2","input":{"subject":"Ship","activeForm":"Shipping"}}`},
		{"tool_result", `{"call_id":"c2","output":"Task #2 created successfully: Ship"}`},
		{"tool_call", `{"tool":"TaskUpdate","call_id":"u1","input":{"taskId":"1","status":"in_progress"}}`},
		{"tool_call", `{"tool":"TaskCreate","call_id":"s1","parent_call_id":"agent-1","input":{"subject":"A subagent's"}}`},
	} {
		if _, err := fx.pool.Exec(ctx, `INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload) VALUES ($1, $2, gen_random_uuid(), $3, $4)`,
			fx.sess, i+1, p.kind, p.payload); err != nil {
			t.Fatal(err)
		}
	}
	r := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	var live struct {
		Plan *struct {
			Items []map[string]any `json:"items"`
		} `json:"plan"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &live)
	if live.Plan == nil || len(live.Plan.Items) != 2 || live.Plan.Items[0]["status"] != "in_progress" || live.Plan.Items[0]["active"] != "Writing tests" {
		t.Fatalf("live = %s", r.Body.String())
	}
}

func TestChecklistCommitsAreRecordedAndLive(t *testing.T) {
	fx := newWorkFx(t)
	fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "set", "items": []string{"Fix", "Ship"}})
	code, body := fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "tick", "index": 1, "commits": []string{"ABCDEF1", "abcdef1"}})
	changed, _ := body["changed"].(map[string]any)
	if code != http.StatusCreated || fmt.Sprint(changed["commits"]) != "[abcdef1]" {
		t.Fatalf("tick = %d %v", code, body)
	}
	if code, body = fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "tick", "index": 2, "commits": []string{"zz"}}); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body["error"]), "7 to 40 hex") {
		t.Fatalf("bad sha = %d %v", code, body)
	}
	fx.postWork("checklist", fx.sess, fx.sessTok, map[string]any{"op": "tick", "index": 1, "commits": []string{"1234567"}})
	r := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	var live struct {
		Checklist *struct {
			Items []struct {
				Commits []string `json:"commits"`
			} `json:"items"`
		} `json:"checklist"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &live); err != nil || live.Checklist == nil || fmt.Sprint(live.Checklist.Items[0].Commits) != "[abcdef1 1234567]" || len(live.Checklist.Items[1].Commits) != 0 {
		t.Fatalf("live = %s", r.Body.String())
	}
}
