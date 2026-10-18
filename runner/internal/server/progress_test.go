package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestProgressEndpointRecordsOwnSessionAndCompletesLatestSnapshot(t *testing.T) {
	fx := newWorkFx(t)
	code, _ := fx.postWork("progress", fx.sess, fx.sessTok, map[string]any{"completed": 3, "total": 7, "label": "Long script"})
	if code != http.StatusCreated {
		t.Fatalf("set progress = %d, want201", code)
	}
	rec := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	if !strings.Contains(rec.Body.String(), `"completed":3`) || !strings.Contains(rec.Body.String(), `"label":"Long script"`) {
		t.Fatal("live state lost explicit progress")
	}
	code, _ = fx.postWork("progress", fx.sess, fx.sessTok, map[string]any{"done": true})
	if code != http.StatusCreated {
		t.Fatalf("complete progress = %d", code)
	}
	var v struct {
		Progress struct{ Completed, Total int }
	}
	rec = fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Progress.Completed != 7 || v.Progress.Total != 7 {
		t.Fatal("done did not complete prior snapshot")
	}
}

func TestProgressEndpointRejectsOtherSessionsAndMalformedScopes(t *testing.T) {
	fx := newWorkFx(t)
	for _, body := range []any{map[string]any{"completed": 1, "total": 2, "label": "Work", "child_id": "forged"}, map[string]any{"completed": 3, "total": 2, "label": "Work"}, map[string]any{"completed": 0, "total": 1000001, "label": "Work"}, map[string]any{"done": true, "total": 1}, map[string]any{"completed": 1, "total": 2, "label": "Work", "call_id": "a", "native_thread_id": "b"}} {
		if code, _ := fx.postWork("progress", fx.sess, fx.sessTok, body); code != 400 {
			t.Fatalf("invalid request accepted: %d", code)
		}
	}
	if code, _ := fx.postWork("progress", fx.privSess, fx.sessTok, map[string]any{"completed": 1, "total": 2, "label": "Work"}); code != 403 {
		t.Fatalf("cross-session progress=%d", code)
	}
	if code, _ := fx.postWork("progress", fx.sess, fx.token(topicOwner, "human"), map[string]any{"completed": 1, "total": 2, "label": "Work"}); code != 401 && code != 403 {
		t.Fatalf("browser wrote agent progress=%d", code)
	}
	if code, _ := fx.postWork("progress", fx.sess, fx.sessTok, map[string]any{"done": true}); code != 409 {
		t.Fatalf("done without snapshot=%d", code)
	}
	if got := fx.kindPayloads(fx.sess, "progress_updated"); len(got) != 0 {
		t.Fatal("refused progress was persisted")
	}
}

func TestProgressOriginBrokerRejectsWrongDaemonSessionAndTimeout(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	result := make(chan error, 1)
	go func() { _, err := h.requestProgressOrigin(t.Context(), dc, "session", "call", ""); result <- err }()
	var req protocol.ResolveProgressOrigin
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	res := protocol.ProgressOriginResult{RequestID: req.RequestID, SessionID: "session", OK: true, ChildID: "child"}
	h.deliverProgressOriginResult(&DaemonConn{ID: "other"}, res)
	wrong := res
	wrong.SessionID = "other-session"
	h.deliverProgressOriginResult(dc, wrong)
	select {
	case <-result:
		t.Fatal("forged origin response accepted")
	default:
	}
	h.deliverProgressOriginResult(dc, res)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	h.deliverProgressOriginResult(dc, res)
	h.progressOriginReqs.mu.Lock()
	pending := len(h.progressOriginReqs.pending)
	h.progressOriginReqs.mu.Unlock()
	if pending != 0 {
		t.Fatal("completed origin lookup leaked")
	}
	synctest.Test(t, func(t *testing.T) {
		h := NewHub()
		dc := &DaemonConn{ID: "old", send: make(chan []byte, 8)}
		h.Register(dc)
		h.SetSessionOwner("session", dc.ID)
		start := time.Now()
		_, err := h.requestProgressOrigin(context.Background(), dc, "session", "call", "")
		if err == nil || time.Since(start) != 5*time.Second {
			t.Fatal("old daemon request did not time out")
		}
	})
}

func TestProgressEndpointResolvesChildAndNeverFallsBackToRoot(t *testing.T) {
	fx := newWorkFx(t)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 16)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	fx.event(fx.sess, "subagent_started", map[string]any{"child_id": "child", "summary": "Child work"})
	post := func(body map[string]any, ok bool) (int, map[string]any) {
		t.Helper()
		type reply struct {
			code int
			body map[string]any
		}
		done := make(chan reply, 1)
		go func() { code, v := fx.postWork("progress", fx.sess, fx.sessTok, body); done <- reply{code, v} }()
		var req protocol.ResolveProgressOrigin
		if err := json.Unmarshal(<-dc.send, &req); err != nil {
			t.Fatal(err)
		}
		if req.SessionID != fx.sess || req.CallID != "fixture-call" {
			t.Fatal("origin request lost session/call binding")
		}
		fx.api.hub.deliverProgressOriginResult(dc, protocol.ProgressOriginResult{RequestID: req.RequestID, SessionID: fx.sess, OK: ok, ChildID: "child"})
		got := <-done
		return got.code, got.body
	}
	if code, _ := post(map[string]any{"completed": 2, "total": 5, "label": "Child script", "call_id": "fixture-call"}, true); code != 201 {
		t.Fatalf("attributed progress=%d", code)
	}
	payloads := fx.kindPayloads(fx.sess, "progress_updated")
	if len(payloads) != 1 || payloads[0]["child_id"] != "child" {
		t.Fatal("child progress was stored on root")
	}
	rec := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	var out struct {
		Progress  any `json:"progress"`
		Subagents []struct {
			Key      string
			Progress map[string]any
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Progress != nil || len(out.Subagents) != 1 || out.Subagents[0].Progress["completed"] != float64(2) {
		t.Fatal("child progress did not remain separate from root")
	}
	if code, _ := post(map[string]any{"done": true, "call_id": "fixture-call"}, true); code != 201 {
		t.Fatalf("attributed done=%d", code)
	}
	before := len(fx.kindPayloads(fx.sess, "progress_updated"))
	if code, _ := post(map[string]any{"completed": 1, "total": 2, "label": "Unknown", "call_id": "fixture-call"}, false); code != 409 {
		t.Fatalf("unknown origin=%d", code)
	}
	fx.event(fx.sess, "subagent_done", map[string]any{"child_id": "child", "summary": "Done"})
	if code, _ := post(map[string]any{"done": true, "call_id": "fixture-call"}, true); code != 409 {
		t.Fatalf("ended child=%d", code)
	}
	fx.event(fx.sess, "subagent_started", map[string]any{"child_id": "child", "summary": "New incarnation"})
	if code, _ := post(map[string]any{"done": true, "call_id": "fixture-call"}, true); code != 409 {
		t.Fatalf("old incarnation done=%d", code)
	}
	if len(fx.kindPayloads(fx.sess, "progress_updated")) != before {
		t.Fatal("unknown/ended origin mutated root or new incarnation")
	}
}

func TestProgressPermanentAuthorityRefusalReleasesOnlyMatchingIngestHold(t *testing.T) {
	fx := newWorkFx(t)
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	id := newUUID()
	fx.api.hub.ingest.failed(fx.sess, id, errors.New("fixture transient database failure"))
	if fx.api.hub.ingest.admit(fx.sess, "later") {
		t.Fatal("fixture hold did not block later event")
	}
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: id, Kind: protocol.ProgressUpdatedKind, Payload: json.RawMessage(`{"completed":1,"total":2,"label":"Late child","child_id":"ended-child"}`)})
	if !fx.api.hub.ingest.admit(fx.sess, "later") {
		t.Fatal("permanently refused event left retry hold blocking all later events")
	}
	fx.api.hub.ingest.failed(fx.sess, "other", errors.New("fixture database failure"))
	fx.api.hub.ingest.stored(fx.sess, id)
	if fx.api.hub.ingest.admit(fx.sess, "later") {
		t.Fatal("unrelated refusal cleared an earlier event's hold")
	}
}

func TestProgressRuntimeRotationBetweenOriginAndSessionLockRefusesAppend(t *testing.T) {
	fx := newWorkFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	runtimeRaw, _, err := db.MintRuntimeLaunch(t.Context(), fx.pool, fx.sess, fx.sessTok, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runtimeToken, err := db.ValidateRuntimeToken(t.Context(), fx.pool, runtimeRaw)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: topicDaemonID, RuntimeTokenID: runtimeToken.ID, BoundSessionID: fx.sess, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	block, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = block.Rollback(t.Context()) }()
	if err := db.LockSessionWork(t.Context(), block, "progress", fx.sess); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		code, _ := fx.postWork("progress", fx.sess, fx.sessTok, map[string]any{"completed": 1, "total": 2, "label": "Old pod", "call_id": "old-call"})
		done <- code
	}()
	var req protocol.ResolveProgressOrigin
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	fx.api.hub.deliverProgressOriginResult(dc, protocol.ProgressOriginResult{RequestID: req.RequestID, SessionID: fx.sess, OK: true})
	// Observe the blocked advisory acquisition, proving the early pool check passed before
	// rotation. No sleep guesses where the request is in its transaction.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks w JOIN pg_locks b USING(locktype,database,classid,objid,objsubid) WHERE b.pid=$1 AND b.granted AND NOT w.granted AND w.locktype='advisory')`, block.Conn().PgConn().PID()).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if _, _, err := db.MintRuntimeLaunch(t.Context(), fx.pool, fx.sess, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := block.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 409 {
		t.Fatalf("old runtime appended after rotation: %d", code)
	}
	if len(fx.kindPayloads(fx.sess, "progress_updated")) != 0 {
		t.Fatal("old runtime progress persisted")
	}
}
