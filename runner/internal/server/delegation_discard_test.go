package server

import (
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChildDiscardArchivesAndRetainsHistory(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(map[bool]string{false: "delegate", true: "human"}[human], func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			fx.hub.SetSessionOwner(child, fx.dc.ID)
			seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture", 12, 3, 0, 0))
			token := fx.parentTok
			if human {
				token = fx.human("owner-1")
			}
			before := childrenLiveGet(t, fx, "owner-1", "").Header().Get("ETag")
			code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), token, nil, nil)
			if code != http.StatusOK {
				t.Fatalf("discard=%d, want200", code)
			}
			var archived *time.Time
			if err := fx.pool.QueryRow(t.Context(), `SELECT archived_at FROM sessions WHERE id=$1`, child).Scan(&archived); err != nil || archived == nil {
				t.Fatalf("archive not durable: %v", err)
			}
			row := fx.row(child)
			if row.EndedAt == nil || row.Status != "ended" {
				t.Fatal("discard did not stop child")
			}
			if got := childrenLiveGet(t, fx, "owner-1", before); got.Code != 200 {
				t.Fatal("discard did not invalidate live cache")
			}
			var live struct {
				Children []liveChild `json:"children"`
			}
			if err := json.Unmarshal(childrenLiveGet(t, fx, "owner-1", "").Body.Bytes(), &live); err != nil || len(live.Children) != 0 {
				t.Fatal("archived child remains live")
			}
			rows, err := db.ListVisibleChildLiveRows(t.Context(), fx.pool, fx.parent, "owner-1")
			if err != nil || len(rows) != 1 {
				t.Fatal("archive lost insights/history rows")
			}
			r := httptest.NewRequest("GET", "/api/sessions/"+fx.parent+"/insights", nil)
			r.SetPathValue("id", fx.parent)
			r.Header.Set("Authorization", "Bearer "+fx.human("owner-1"))
			w := httptest.NewRecorder()
			fx.api.HandleGetSessionInsights(w, r)
			var insights struct {
				StartedSessions struct {
					Count int `json:"count"`
				} `json:"started_sessions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &insights); err != nil || w.Code != 200 || insights.StartedSessions.Count != 1 {
				t.Fatal("archive lost started-session totals")
			}
			code, _, _ = fx.do("GET", childRoute(fx.parent, child, "/result"), fx.parentTok, nil, nil)
			if code != 200 {
				t.Fatal("archive removed direct child access")
			}
			code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/message"), fx.parentTok, map[string]any{"text": "cannot revive hidden worker"}, nil)
			if code != http.StatusConflict {
				t.Fatalf("archived input accepted: %d", code)
			}
			blocked, err := db.DelegationResumeBlocked(t.Context(), fx.pool, child)
			if err != nil || !blocked {
				t.Fatal("archived child can be adopted or resumed")
			}
			code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/discard"), token, nil, nil)
			if code != 200 {
				t.Fatal("discard is not idempotent")
			}
		})
	}
}

func TestChildDiscardOwnChildrenOnly(t *testing.T) {
	fx := newDelegFx(t)
	mine, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	other, _ := fx.session(sessOpts{owner: "owner-1"})
	theirs := fx.childOf(other)
	for _, token := range []string{fx.parentTok, fx.human("owner-1")} {
		for _, id := range []string{theirs, other, fx.parent, newUUID(), "bad-id"} {
			code, _, _ := fx.do("POST", childRoute(fx.parent, id, "/discard"), token, nil, nil)
			if code != 404 {
				t.Fatalf("foreign target=%d", code)
			}
		}
	}
	code, _, _ := fx.do("POST", childRoute(fx.parent, mine, "/discard"), fx.human("owner-2"), nil, nil)
	if code != 404 {
		t.Fatalf("non-owner=%d", code)
	}
	if fx.row(mine).EndedAt != nil {
		t.Fatal("unauthorized discard stopped child")
	}
}

func TestChildDiscardRefusesToHideChildWhenStopCannotBeDelivered(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	for len(fx.dc.send) < cap(fx.dc.send) {
		fx.dc.send <- []byte(`{}`)
	}
	code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if code != 503 {
		t.Fatalf("undeliveredstop=%d", code)
	}
	if fx.row(child).EndedAt != nil {
		t.Fatal("failed stop was recorded as ended")
	}
}

// A stop is asynchronous. Discard must wait for its authenticated final receipt
// instead of making the caller repeat a request that already reached the daemon.
func TestChildDiscardWaitsForWorkspaceFinalOnFirstAttempt(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: "owner-1", LaunchGeneration: "discard-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, child, m); err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	raw, _ := json.Marshal(binding)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE session_workspaces SET runtime_generation=1,runtime_binding=$2 WHERE session_id=$1`, child, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO local_workspace_authority(session_id,token,launch_generation,runtime_generation,connection_generation) VALUES($1,'discard-token',$2,1,'connection')`, child, m.LaunchGeneration); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case raw := <-fx.dc.send:
				var kill protocol.KillSession
				if json.Unmarshal(raw, &kill) != nil || kill.Type != "kill_session" || kill.SessionID != child {
					continue
				}
				time.Sleep(40 * time.Millisecond)
				final := protocol.WorkspaceFinalization{Binding: binding, Message: protocol.SessionEnded{Type: "session_ended", SessionID: child}, Results: []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Branch: "wip/" + child}}}
				_, err := db.CommitLocalWorkspaceFinal(t.Context(), fx.pool, child, "discard-token", final)
				done <- err
				return
			case <-t.Context().Done():
				done <- t.Context().Err()
				return
			}
		}
	}()
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code != 200 {
		t.Fatalf("first discard=%d %+v; want 200 after final receipt", code, out)
	}
	if fx.row(child).ArchivedAt == nil {
		t.Fatal("settled child not archived")
	}
}

// An open question is visible in Needs you before discard, and is still open
// in the child history afterward. It never becomes an invented empty answer.
func TestChildDiscardPreservesUnansweredQuestion(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	id, err := db.CreateQuestion(t.Context(), fx.pool, child, "Which release should I use?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE messages SET child_escalated=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	q, err := db.GetMessage(t.Context(), fx.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	fx.api.recordMessageEvent(t.Context(), *fx.row(child), *q)
	fx.api.RegisterLiveRoutes(fx.mux)
	code, before, _ := fx.do("GET", "/api/sessions/"+child+"/live", fx.human("owner-1"), nil, nil)
	if code != 200 || len(before["asks"].([]any)) != 1 {
		t.Fatalf("before discard Needs you: %d %v", code, before)
	}
	parentCode, parentBefore, _ := fx.do("GET", "/api/sessions/"+fx.parent+"/live", fx.human("owner-1"), nil, nil)
	if parentCode != 200 {
		t.Fatalf("parent live: %d", parentCode)
	}
	// Diagnostic for BLG-350's visibility audit, without making the current
	// parent/child routing a permanent invariant for BLG-343.
	t.Logf("Before discard: child Needs you has %d asks; parent Needs you has %d asks", len(before["asks"].([]any)), len(parentBefore["asks"].([]any)))
	code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("discard: %d", code)
	}
	// The daemon can report its end after the archive operation too.
	HandleSessionEnded(t.Context(), fx.hub, fx.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child})
	q, err = db.GetMessage(t.Context(), fx.pool, id)
	if err != nil || q.Status != "withdrawn" || q.Answer != nil || q.SessionID != child || !q.NoWait {
		t.Fatalf("unanswered question after discard: %+v, %v", q, err)
	}
	code, after, _ := fx.do("GET", "/api/sessions/"+child+"/live", fx.human("owner-1"), nil, nil)
	if code != 200 || len(after["asks"].([]any)) != 0 {
		t.Fatalf("withdrawn question still in Needs you: %d %v", code, after)
	}
	note := fx.parentNote(fx.parent, 5*time.Second)
	if !strings.Contains(note.Text, "kind=discard_unanswered") && !strings.Contains(note.Text, "kind=question_resolved") {
		t.Fatalf("parent not told: %+v", note)
	}
	code, _, _ = fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("retry: %d", code)
	}
}

func TestDiscardWithdrawalNeverOverwritesAnAnswerOrDismissal(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	owner := &BrowserConn{ID: "withdraw-owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	other := &BrowserConn{ID: "withdraw-other", AccountID: "owner-2", send: make(chan []byte, 32)}
	fx.hub.RegisterBrowser(owner)
	fx.hub.RegisterBrowser(other)
	ctx := t.Context()
	open, err := db.CreateAsk(ctx, fx.pool, child, "Private approval?", true)
	if err != nil {
		t.Fatal(err)
	}
	answered, err := db.CreateQuestion(ctx, fx.pool, child, "Answered question")
	if err != nil {
		t.Fatal(err)
	}
	dismissed, err := db.CreateQuestion(ctx, fx.pool, child, "Dismissed question")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AnswerMessage(ctx, fx.pool, answered, "Use staging"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DismissMessage(ctx, fx.pool, dismissed, "owner-1"); err != nil {
		t.Fatal(err)
	}
	// A stop callback can close the snapshot's still-open ask before archive.
	if _, err = db.CloseOpenMessagesForSession(ctx, fx.pool, child); err != nil {
		t.Fatal(err)
	}
	if err = fx.api.withdrawDiscardQuestions(ctx, child, []string{open, answered, dismissed}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{open, answered, dismissed} {
		q, err := db.GetMessage(ctx, fx.pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if q.SessionID != child {
			t.Fatal("private question moved to public parent")
		}
		if id == open && (q.Status != "withdrawn" || q.Answer != nil) {
			t.Fatalf("not withdrawn: %+v", q)
		}
		if id == answered && (q.Status != "answered" || q.Answer == nil || *q.Answer != "Use staging") {
			t.Fatalf("answer lost: %+v", q)
		}
		if id == dismissed && (!q.Dismissed || q.Status != "answered") {
			t.Fatalf("dismissal lost: %+v", q)
		}
	}
	seen := false
	for raw := range awaitAll(owner.send) {
		var msg protocol.MessageAnswered
		if json.Unmarshal(raw, &msg) == nil && msg.Type == "message_answered" && msg.Message.ID == open && msg.Message.Status == "withdrawn" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("owner missed withdrawal")
	}
	for raw := range awaitAll(other.send) {
		var msg protocol.MessageAnswered
		if json.Unmarshal(raw, &msg) == nil && msg.Type == "message_answered" {
			t.Fatal("private withdrawal reached another account")
		}
	}
	var response map[string]any
	fx.mux.HandleFunc("GET /api/messages/{id}/answer", fx.api.HandleGetMessageAnswer)
	code, response, _ := fx.do("GET", "/api/messages/"+open+"/answer", delegDaemonToken, nil, nil)
	if code != 200 || response["withdrawn"] != true || response["answered"] != false {
		t.Fatalf("withdrawn poll: %d %v", code, response)
	}
}

func TestParentDiscardWithdrawsOwnAndChildQuestions(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	var ids []string
	for _, sid := range []string{fx.parent, child} {
		id, err := db.CreateQuestion(t.Context(), fx.pool, sid, "Which release?")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("discard: %d", code)
	}
	for _, id := range ids {
		q, err := db.GetMessage(t.Context(), fx.pool, id)
		if err != nil || q.Status != "withdrawn" || q.Answer != nil {
			t.Fatalf("question: %+v %v", q, err)
		}
	}
}

func TestDiscardWithdrawsRealPendingChildQuestions(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "no-wait"}[noWait], func(t *testing.T) {
			fx := newDelegFx(t)
			fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
			child, token := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			fx.hub.SetSessionOwner(child, fx.dc.ID)
			body, _ := json.Marshal(map[string]any{"kind": "ask", "body": "Which branch?", "no_wait": noWait})
			rec := privDo(fx.api.HandlePostMessages, "POST", "/x", "", token, string(body))
			if rec.Code != 201 {
				t.Fatal(rec.Body.String())
			}
			var posted struct{ ID string }
			_ = json.Unmarshal(rec.Body.Bytes(), &posted)
			pending, _ := db.GetMessage(t.Context(), fx.pool, posted.ID)
			if pending.ChildEscalated {
				t.Fatal("fixture question escalated")
			}
			_ = fx.parentNote(fx.parent, 5*time.Second)
			code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
			if code != 200 {
				t.Fatalf("discard: %d %v", code, out)
			}
			row, _ := db.GetMessage(t.Context(), fx.pool, posted.ID)
			if row.Status != "withdrawn" || row.Answer != nil || row.ChildEscalated {
				t.Fatalf("pending discard: %+v", row)
			}
			note := fx.parentNote(fx.parent, 5*time.Second)
			if !strings.Contains(note.Text, "kind=discard_unanswered") && !strings.Contains(note.Text, "kind=question_resolved") {
				t.Fatalf("parent missed withdrawal: %+v", note)
			}
			if err := fx.api.reconcileChildQuestions(t.Context(), time.Now().Add(4*time.Minute)); err != nil {
				t.Fatal(err)
			}
			row, _ = db.GetMessage(t.Context(), fx.pool, posted.ID)
			if row.Status != "withdrawn" || row.ChildEscalated {
				t.Fatalf("discarded question resurfaced: %+v", row)
			}
		})
	}
}

func TestDiscardQuestionSharesEscalationLock(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	id, err := db.CreateQuestion(t.Context(), fx.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	fx.hub.RegisterBrowser(owner)
	done := make(chan int, 1)
	err = withLimitResumeActionLock(t.Context(), fx.pool, child, func(_ *pgxpool.Conn) error {
		go func() {
			code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
			done <- code
		}()
		select {
		case code := <-done:
			t.Errorf("discard bypassed escalation lock: %d", code)
			done <- code
		case <-time.After(100 * time.Millisecond):
		}
		return fx.api.escalateChildQuestionUnlocked(t.Context(), fx.row(child), fx.row(fx.parent), id)
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("discard: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("discard did not finish")
	}
	last := ""
	for raw := range awaitAll(owner.send) {
		var event struct{ Message struct{ ID, Status string } }
		_ = json.Unmarshal(raw, &event)
		if event.Message.ID == id {
			last = event.Message.Status
		}
	}
	if last != "withdrawn" {
		t.Fatalf("browser retained stale escalation: %q", last)
	}
}

// The human owner must be able to archive a stopped child after the parent
// process has gone away. No daemon delivery is needed without pending cleanup.
func TestChildDiscardStoppedWorkerWithoutLiveParent(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	now := time.Now()
	for _, id := range []string{fx.parent, child} {
		if err := db.EndSessionStatus(t.Context(), fx.pool, id, "stopped", &now, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("discard without live parent = %d: %+v", code, out)
	}
	if row := fx.row(child); row.ArchivedAt == nil || row.Status != "stopped" {
		t.Fatalf("stopped child not safely archived: %+v", row)
	}
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("stopped child needed a live worker: %s", raw)
	default:
	}
}

func TestRestartPauseInterruptThroughAPI(t *testing.T) {
	fx := newDelegFx(t)
	detail := "Recovery paused: workspace stop is not finished. Work is retained."
	raw, err := json.Marshal(protocol.SessionPaused{Type: "session_paused", SessionID: fx.parent, Reason: protocol.PausedByRestart, Detail: detail})
	if err != nil {
		t.Fatal(err)
	}
	if !handleDaemonFrame(t.Context(), fx.hub, fx.dc, fx.pool, raw, envelope{Type: "session_paused", SessionID: fx.parent}) {
		t.Fatal("pause frame refused")
	}
	row := fx.row(fx.parent)
	if row.Status != "waiting" || sessionRowToInfo(*row, "").PausedDetail != detail {
		t.Fatalf("pause projection: %+v", row)
	}
	code, out, _ := fx.do("POST", "/api/runner/sessions/"+fx.parent+"/interrupt", fx.human("owner-1"), nil, nil)
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out), detail) {
		t.Fatalf("interrupt = %d: %+v", code, out)
	}
	if fx.row(fx.parent).Status != "waiting" {
		t.Fatal("Stop restarted paused work")
	}
}

func TestChildDiscardRetainsUnfinishedWorkspaceWithoutDaemon(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: "owner-1", LaunchGeneration: "discard-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), fx.pool, child, m); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO local_workspace_authority(session_id,token,launch_generation,runtime_generation,connection_generation) VALUES($1,'discard-token',$2,1,'connection')`, child, m.LaunchGeneration); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), fx.pool, child, "stopped", &now, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
		t.Fatal(err)
	}
	// This stopped worker has no session-to-daemon route, and no final receipt.
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.human("owner-1"), nil, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(fmt.Sprint(out), "All work is retained") {
		t.Fatalf("pending discard = %d: %+v", code, out)
	}
	if fx.row(child).ArchivedAt != nil {
		t.Fatal("unproven workspace archived")
	}
	var retained bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT stop_requested AND final_digest IS NULL AND NOT aborted FROM local_workspace_authority WHERE session_id=$1`, child).Scan(&retained); err != nil || !retained {
		t.Fatalf("cleanup intent not retained: %v", err)
	}
}
