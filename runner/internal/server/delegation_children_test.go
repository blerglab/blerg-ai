package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func childRoute(parent, child, suffix string) string {
	return "/api/sessions/" + parent + "/children/" + child + suffix
}

func TestChildRoutesOwnChildrenOnly(t *testing.T) {
	fx := newDelegFx(t)
	mine, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(mine, fx.dc.ID)
	other, otherToken := fx.session(sessOpts{owner: "owner-1"})
	theirs := fx.childOf(other)
	unrelated, _ := fx.session(sessOpts{owner: "owner-1"})
	reassigned, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner-2' WHERE id=$1`, reassigned); err != nil {
		t.Fatal(err)
	}
	fx.hub.notePrivate(reassigned, "owner-2")
	routes := []struct {
		method, suffix string
		body           any
	}{
		{"GET", "", nil}, {"POST", "/message", map[string]any{"text": "hi"}},
		{"POST", "/model", map[string]any{"effort": "high"}}, {"POST", "/interrupt", nil},
		{"POST", "/stop", nil}, {"POST", "/discard", nil}, {"GET", "/result", nil}, {"GET", "/events", nil},
		{"POST", "/uploads", "raw"}, {"GET", "/artifacts", nil}, {"GET", "/artifacts/" + newUUID() + "/download", nil},
	}
	for _, route := range routes {
		for _, target := range []string{theirs, unrelated, reassigned, fx.parent, newUUID(), "bad-id"} {
			code, out, _ := fx.do(route.method, childRoute(fx.parent, target, route.suffix), fx.parentTok, route.body, nil)
			if code != http.StatusNotFound || asString(out["error"]) != "session not found" {
				t.Fatalf("%s %s target access: status=%d error=%v", route.method, route.suffix, code, out["error"])
			}
		}
		code, _, _ := fx.do(route.method, childRoute(fx.parent, mine, route.suffix), otherToken, route.body, nil)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s wrong parent token status=%d", route.method, route.suffix, code)
		}
	}
	code, out, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/children", fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("child list status=%d", code)
	}
	for _, entry := range out["children"].([]any) {
		if entry.(map[string]any)["id"] == reassigned {
			t.Fatal("reassigned private child leaked through list")
		}
	}

}

func TestChildStatusHasAuthoritativeOpenAsksAndListUsesLowercaseFields(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, title: "Child"})
	kept, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "idle"})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, kept); err != nil {
		t.Fatal(err)
	}
	mid, err := db.CreateMessage(t.Context(), fx.pool, child, "ask", "Which option?")
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := fx.do("GET", childRoute(fx.parent, child, ""), fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	asks, ok := out["open_asks"].([]any)
	if !ok || len(asks) != 1 {
		t.Fatalf("missing authoritative asks: %v", out)
	}
	ask := asks[0].(map[string]any)
	if ask["message_id"] != mid || ask["body"] != "Which option?" {
		t.Fatalf("ask shape=%v", ask)
	}
	code, out, _ = fx.do("GET", "/api/sessions/"+fx.parent+"/children", fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("list status=%d", code)
	}
	children := out["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("children=%v", children)
	}
	for _, entry := range children {
		got := entry.(map[string]any)
		for _, key := range []string{"id", "title", "engine", "model", "effort", "runtime", "status", "lifecycle", "started_at", "ended_at", "asking", "keep_running", "delegation_via"} {
			if _, ok := got[key]; !ok {
				t.Errorf("missing lowercase field %s", key)
			}
		}
		if got["ID"] != nil || got["StartedAt"] != nil {
			t.Fatal("Go fields leaked instead of JSON names")
		}
		if got["id"] == child && got["asking"] != true {
			t.Fatal("child ask not listed")
		}
	}
	code, out, _ = fx.do("GET", childRoute(fx.parent, kept, ""), fx.parentTok, nil, nil)
	if code != 200 || len(out["open_asks"].([]any)) != 0 {
		t.Fatal("empty asks must be an explicit empty array")
	}
}

func TestChildMessageAnswersAskAndPreservesSessionSource(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	mid, err := db.CreateMessage(t.Context(), fx.pool, child, "ask", "Choose?")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, child, messageEventID(mid, "ask"), "ask", `{"body":"Choose?"}`); err != nil {
		t.Fatal(err)
	}
	woke := fx.api.waiters.channel(mid)
	defer fx.api.waiters.drop(mid)
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/message"), fx.parentTok, map[string]any{"text": "yes"}, nil)
	if code != 202 || out["answered_ask"] != true {
		t.Fatalf("answer status=%d body=%v", code, out)
	}
	select {
	case <-woke:
	default:
		t.Fatal("answer did not wake the ask waiter")
	}

	row, err := db.GetMessage(t.Context(), fx.pool, mid)
	if err != nil || row == nil || row.Answer == nil || *row.Answer != "yes" {
		t.Fatal("ask was not answered")
	}
	code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/message"), fx.parentTok, map[string]any{"text": "continue"}, nil)
	if code != 202 {
		t.Fatalf("message status=%d", code)
	}
	found := false
	for len(fx.dc.send) > 0 {
		var msg protocol.AgentUserMessage
		if err := json.Unmarshal(<-fx.dc.send, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "agent_user_message" && msg.Text == "continue" {
			found = true
			if msg.Source != "session" {
				t.Fatal("parent source lost")
			}
		}
	}
	if !found {
		t.Fatal("no child message frame")
	}
}

func TestChildEventsLongPollReturnsOnAppendAndCancels(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	done := make(chan *httptest.ResponseRecorder, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequest("GET", childRoute(fx.parent, child, "/events?wait=3"), nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+fx.parentTok)
	go func() { rec := httptest.NewRecorder(); fx.mux.ServeHTTP(rec, request); done <- rec }()
	select {
	case <-done:
		t.Fatal("empty long-poll returned immediately")
	case <-time.After(50 * time.Millisecond):
	}
	if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, child, newUUID(), "assistant_text", `{"done":true,"text":"ready"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ready") {
			t.Fatalf("poll status=%d", rec.Code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("long-poll did not wake after append")
	}
}

func TestChildModelInterruptStopAndResultReuseControls(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/model"), fx.parentTok, map[string]any{"model": "sonnet", "effort": "high"}, nil)
	if code != 202 {
		t.Fatalf("model status=%d", code)
	}
	code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/interrupt"), fx.parentTok, nil, nil)
	if code != 202 {
		t.Fatalf("interrupt status=%d", code)
	}
	if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, child, newUUID(), "assistant_text", `{"done":true,"text":"child outcome"}`); err != nil {
		t.Fatal(err)
	}
	code, out, _ := fx.do("GET", childRoute(fx.parent, child, "/result"), fx.parentTok, nil, nil)
	if code != 200 || out["last_assistant_message"] != "child outcome" {
		t.Fatalf("result status=%d body=%v", code, out)
	}
	code, out, _ = fx.do("POST", childRoute(fx.parent, child, "/stop"), fx.parentTok, nil, nil)
	if code != 200 || out["status"] != "stopped" || !db.TerminallyEnded(fx.row(child)) {
		t.Fatalf("stop status=%d body=%v", code, out)
	}
	found := map[string]bool{}
	for len(fx.dc.send) > 0 {
		var frame map[string]any
		if err := json.Unmarshal(<-fx.dc.send, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["session_id"] != child {
			continue
		}
		kind := asString(frame["type"])
		found[kind] = true
		if kind == "set_session_model" && (frame["model"] != "sonnet" || frame["effort"] != "high") {
			t.Fatal("model frame did not retain requested settings")
		}
	}
	for _, kind := range []string{"set_session_model", "interrupt_session", "kill_session"} {
		if !found[kind] {
			t.Errorf("missing control frame %s", kind)
		}
	}
}

func TestChildUploadRetainsOwnerAndEmitsOnlySessionUpload(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/uploads"), fx.parentTok, "# input\n", map[string]string{"X-Artifact-Name": "input.md", "Content-Type": "application/octet-stream"})
	if code != 201 {
		t.Fatalf("upload status=%d body=%v", code, out)
	}
	id := asString(out["id"])
	if out["origin"] != "user" {
		t.Fatal("parent upload changed artifact origin")
	}
	var uploader string
	if err := fx.pool.QueryRow(t.Context(), `SELECT uploaded_by FROM session_artifacts WHERE id=$1`, id).Scan(&uploader); err != nil || uploader != "owner-1" {
		t.Fatalf("owner attribution=%s err=%v", uploader, err)
	}
	events, err := db.ListAgentEvents(t.Context(), fx.pool, child, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == "user_message" {
			t.Fatal("upload forged a user_message and could close a live turn")
		}
		if event.Kind != "session_upload" {
			continue
		}
		found = true
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["from_session_id"] != fx.parent || payload["from_title"] != "Parent" || payload["artifact_id"] != id || payload["name"] != "input.md" || payload["size"] != float64(8) {
			t.Fatalf("upload event=%v", payload)
		}
	}
	if !found {
		t.Fatal("upload missing session_upload attribution event")
	}
	code, out, _ = fx.do("GET", childRoute(fx.parent, child, "/artifacts"), fx.parentTok, nil, nil)
	if code != 200 || len(out["artifacts"].([]any)) != 1 {
		t.Fatalf("files status=%d body=%v", code, out)
	}
	request := httptest.NewRequest("GET", childRoute(fx.parent, child, "/artifacts/"+id+"/download"), nil)
	request.Header.Set("Authorization", "Bearer "+fx.parentTok)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, request)
	if rec.Code != 200 || rec.Body.String() != "# input\n" {
		t.Fatalf("download status=%d body=%q", rec.Code, rec.Body.String())
	}
	for _, header := range []string{"X-Content-Type-Options", "Content-Security-Policy", "Cache-Control", "Content-Disposition"} {
		if rec.Header().Get(header) == "" {
			t.Errorf("download lacks hardened header %s", header)
		}
	}
	other, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	code, _, _ = fx.do("GET", childRoute(fx.parent, other, "/artifacts/"+id+"/download"), fx.parentTok, nil, nil)
	if code != 404 {
		t.Fatalf("cross-child artifact download status=%d", code)
	}
	foreign, foreignToken := fx.session(sessOpts{owner: "owner-1"})
	code, _, _ = fx.do("GET", childRoute(foreign, child, "/artifacts/"+id+"/download"), foreignToken, nil, nil)
	if code != 404 {
		t.Fatalf("same-owner foreign parent read private child artifact: status=%d", code)
	}
	code, _, _ = fx.do("GET", childRoute(foreign, child, "/artifacts"), foreignToken, nil, nil)
	if code != 404 {
		t.Fatalf("same-owner foreign parent listed private child artifacts: status=%d", code)
	}

}

func TestChildRoutesAllowRecoverableOfflineParent(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.UnregisterIfCurrent(fx.dc)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='error',ended_at=now(),end_reason='daemon_lost' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/result", "/events", "/artifacts"} {
		code, _, _ := fx.do("GET", childRoute(fx.parent, child, suffix), fx.parentTok, nil, nil)
		if code != 200 {
			t.Fatalf("offline read %s status=%d", suffix, code)
		}
	}
	code, _, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/children", fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("offline list status=%d", code)
	}
	code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/stop"), fx.parentTok, nil, nil)
	if code != 200 || !db.TerminallyEnded(fx.row(child)) {
		t.Fatalf("offline child stop status=%d", code)
	}
	code, _, _ = fx.postChild(fx.parent, fx.parentTok, codexBody(), newUUID())
	if code != 409 {
		t.Fatalf("offline parent started a new child: status=%d", code)
	}
}

func TestChildBodiesRejectControlInjectionAndTrailingJSON(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	for _, body := range []string{`{"text":"hi","source":"human"}`, `{"text":"hi"} {}`, `{"model":"sonnet","engine":"codex"}`} {
		suffix := "/message"
		if strings.Contains(body, "model") {
			suffix = "/model"
		}
		code, _, _ := fx.do("POST", childRoute(fx.parent, child, suffix), fx.parentTok, body, nil)
		if code != 400 {
			t.Fatalf("invalid child body status=%d", code)
		}
	}
	for _, query := range []string{"after_seq=-1", "before_seq=bad", "limit=-1", "wait=bad"} {
		code, _, _ := fx.do("GET", childRoute(fx.parent, child, "/events?"+query), fx.parentTok, nil, nil)
		if code != 400 {
			t.Fatalf("invalid event query %s status=%d", query, code)
		}
	}
}

func TestChildEventsCancellationAndPagination(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	for _, text := range []string{"one", "two", "three"} {
		payload, _ := json.Marshal(map[string]any{"text": text, "done": true})
		if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, child, newUUID(), "assistant_text", string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := fx.do("GET", childRoute(fx.parent, child, "/events?limit=1"), fx.parentTok, nil, nil)
	if code != 200 || out["has_more"] != true || len(out["events"].([]any)) != 1 {
		t.Fatalf("first page status=%d body=%v", code, out)
	}
	events := out["events"].([]any)
	seq := int64(events[0].(map[string]any)["seq"].(float64))
	code, out, _ = fx.do("GET", childRoute(fx.parent, child, "/events?after_seq="+strconv.FormatInt(seq, 10)), fx.parentTok, nil, nil)
	if code != 200 || out["has_more"] != false || len(out["events"].([]any)) != 2 {
		t.Fatalf("next page status=%d body=%v", code, out)
	}
	code, out, _ = fx.do("GET", childRoute(fx.parent, child, "/events?before_seq=0&limit=1"), fx.parentTok, nil, nil)
	if code != 200 || out["has_older"] != true || len(out["events"].([]any)) != 1 {
		t.Fatalf("tail status=%d body=%v", code, out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest("GET", childRoute(fx.parent, child, "/events?after_seq=999999&wait=25"), nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+fx.parentTok)
	done := make(chan struct{})
	go func() { fx.mux.ServeHTTP(httptest.NewRecorder(), request); close(done) }()
	select {
	case <-done:
		cancel()
		t.Fatal("long-poll failed to wait")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled poll leaked a waiter")
	}
}
