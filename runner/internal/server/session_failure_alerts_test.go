package server

import (
	"encoding/json"
	webpush "github.com/SherClockHolmes/webpush-go"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestSessionFailureAlertsSurviveAnEndedCoordinator(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, f.parent, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child, ExitCode: 1, Message: "engine stopped"})
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil || len(rows) != 1 || rows[0].Status != "open" || !strings.Contains(rows[0].Body, "engine stopped") {
		t.Fatalf("failure did not reach Needs you: %+v %v", rows, err)
	}
	if err := f.api.escalateSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Body, "is not responding") {
		t.Fatalf("ended coordinator did not escalate immediately: %+v %v", rows, err)
	}
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child, ExitCode: 1, Message: "engine stopped"})
	rows, err = db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil || len(rows) != 1 || rows[0].Status != "open" {
		t.Fatalf("duplicate report lost alert: %+v %v", rows, err)
	}
}

func TestChildNoWaitAskCallsItsOwner(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	r := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, `{"kind":"ask","no_wait":true,"body":"Which branch?"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("ask: %d %s", r.Code, r.Body.String())
	}
	for raw := range awaitAll(owner.send) {
		var m protocol.MessageCreated
		if json.Unmarshal(raw, &m) == nil && m.Type == "message_created" && m.Message.SessionID == child {
			if m.Quiet || !m.Message.Answerable {
				t.Fatalf("child ask suppressed: %+v", m)
			}
			return
		}
	}
	t.Fatal("child ask never reached owner")
}

func TestFailureAlertsEscalateUntilCoordinatorReadsResult(t *testing.T) {
	for _, acted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhandled", true: "result-read"}[acted], func(t *testing.T) {
			f := newDelegFx(t)
			child := f.childOf(f.parent)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			// Exercise durable reconciliation too: a terminal writer need not have a hub hook.
			now := time.Now()
			if err := db.EndSessionStatus(t.Context(), f.pool, child, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
				t.Fatal(err)
			}
			if err := f.api.reconcileSessionFailures(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			if note := f.parentNote(f.parent, time.Second); note.Source != childTurnSource || !strings.Contains(note.Text, "Work delivery is unconfirmed") {
				t.Fatalf("missing outcome in parent notice: %+v", note)
			}
			if acted {
				if err := db.RecordChildResultRead(t.Context(), f.pool, f.parent, child); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.api.escalateSessionFailures(t.Context(), now.Add(4*time.Minute)); err != nil {
				t.Fatal(err)
			}
			rows, err := db.ListMessagesBySession(t.Context(), f.pool, child)
			if err != nil || len(rows) != 1 {
				t.Fatalf("alerts=%+v %v", rows, err)
			}
			escalated := strings.Contains(rows[0].Body, "is not responding")
			if escalated == acted {
				t.Fatalf("acted=%v alert=%s", acted, rows[0].Body)
			}
		})
	}
}

func TestSessionFailureAlertsIncludeRestartPausesAndKeepCleanStopsQuiet(t *testing.T) {
	for _, reason := range []string{db.EndReasonStoppedByUser, db.EndReasonStoppedByAgent, db.EndReasonJobFinished, db.EndReasonProcessExited, db.EndReasonProcessFailed, db.EndReasonDaemonUnreported, db.EndReasonDaemonLost, "restart"} {
		t.Run(reason, func(t *testing.T) {
			f := newDelegFx(t)
			id := f.parent
			if reason == "restart" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status='idle' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, protocol.SessionPaused{Type: "session_paused", SessionID: id, Reason: protocol.PausedByRestart, Detail: "not recovered"})
			} else {
				now := time.Now()
				if err := db.EndSessionStatus(t.Context(), f.pool, id, "stopped", &now, systemEnd(reason)); err != nil {
					t.Fatal(err)
				}
				if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := db.ListMessagesBySession(t.Context(), f.pool, id)
			if err != nil {
				t.Fatal(err)
			}
			quiet := reason == db.EndReasonStoppedByUser || reason == db.EndReasonStoppedByAgent || reason == db.EndReasonJobFinished || reason == db.EndReasonProcessExited
			if (len(rows) == 0) != quiet {
				t.Fatalf("quiet=%v messages=%+v", quiet, rows)
			}
		})
	}
}

func TestFailureAlertShowsRetainedWorkAndCanBeDismissedAfterParentEnds(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: "owner-1", LaunchGeneration: "failure-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, child, m); err != nil {
		t.Fatal(err)
	}
	results, _ := json.Marshal([]protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Pending: true, SyncError: "delivery failed", RecoveryLocation: "/recovery/child"}})
	if _, err := f.pool.Exec(t.Context(), `UPDATE session_workspaces SET results=$2 WHERE session_id=$1`, child, results); err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child, ExitCode: 1, Message: "freeze failed"})
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Body, "/recovery/child") {
		t.Fatalf("retention missing: %+v %v", rows, err)
	}
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, f.parent, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", rows[0].ID, f.human("owner-1"), `{}`)
	if rec.Code != 200 {
		t.Fatalf("cannot dismiss stopped child's alert: %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetMessage(t.Context(), f.pool, rows[0].ID)
	if err != nil || !row.Dismissed {
		t.Fatalf("not dismissed: %+v %v", row, err)
	}
}

func TestFailureAlertPushAndDurableOutbox(t *testing.T) {
	f := newNoticeFixture(t)
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sid, ExitCode: 1, Message: "runtime stopped"})
	if got := f.pushes.Load(); got != 1 {
		t.Fatalf("failure pushes=%d, want 1", got)
	}
	// Simulate a runner crash between committing the alert and sending the push.
	if _, err := f.pool.Exec(t.Context(), `UPDATE session_failure_alerts SET person_notified=false WHERE session_id=$1`, f.sid); err != nil {
		t.Fatal(err)
	}
	a := &API{hub: f.hub, dbPool: f.pool}
	if err := a.deliverPendingFailureAlerts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.pushes.Load(); got != 2 {
		t.Fatalf("outbox not replayed: pushes=%d", got)
	}
	if err := a.deliverPendingFailureAlerts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.pushes.Load(); got != 2 {
		t.Fatalf("settled outbox repeated: pushes=%d", got)
	}
}

func TestPrivateFailureAlertNeverPushesSessionDetails(t *testing.T) {
	f := newNoticeFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET private=true,spawning_account_id='owner',title='Private task' WHERE id=$1`, f.sid); err != nil {
		t.Fatal(err)
	}
	owner := &BrowserConn{ID: "owner-alert", AccountID: "owner", send: make(chan []byte, 32)}
	other := &BrowserConn{ID: "other-alert", AccountID: "other", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	f.hub.RegisterBrowser(other)
	saved := sendWebPush
	sendWebPush = func(raw []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var p map[string]string
		if json.Unmarshal(raw, &p) != nil || p["url"] != "" || strings.Contains(string(raw), "Private task") || strings.Contains(string(raw), "private reason") {
			t.Errorf("private details in push: %s", raw)
		}
		return saved(raw, nil, nil)
	}
	t.Cleanup(func() { sendWebPush = saved })
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sid, ExitCode: 1, Message: "private reason"})
	if got := f.pushes.Load(); got != 1 {
		t.Fatalf("generic push count=%d", got)
	}
	found := false
	for raw := range awaitAll(owner.send) {
		var m protocol.MessageCreated
		if json.Unmarshal(raw, &m) == nil && m.Type == "message_created" {
			found = true
			if !m.Message.Answerable {
				t.Fatal("owner cannot acknowledge alert")
			}
		}
	}
	if !found {
		t.Fatal("owner received no alert")
	}
	for raw := range awaitAll(other.send) {
		if strings.Contains(string(raw), "private reason") {
			t.Fatalf("private reason reached other viewer: %s", raw)
		}
	}
}

func TestDeliveryFailureIsRecoveredWithoutATerminalReport(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	f.hub.SetChildTurnHook(nil) // runner restarts after persistence but before notification
	seedEvent(t, f.pool, child, time.Now(), "error", json.RawMessage(`{"code":"child_sync","message":"freeze failed; work retained"}`))
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Body, "freeze failed") {
		t.Fatalf("delivery failure stayed silent: %+v %v", rows, err)
	}
}

func TestAcknowledgingAFailureNeverStartsAnotherTurnOrAlert(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetChildTurnHook(nil)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET engine='codex' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.parent, ExitCode: 1, Message: "engine stopped"})
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", rows[0].ID, f.human("owner-1"), `{"answer":"Checked the saved work"}`)
	if rec.Code != 200 {
		t.Fatalf("acknowledge=%d %s", rec.Code, rec.Body.String())
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil || len(rows) != 1 || rows[0].Status != "answered" {
		t.Fatalf("acknowledgment created another alert: %+v %v", rows, err)
	}
	if n := f.parentNote(f.parent, 100*time.Millisecond); n.Type != "" {
		t.Fatalf("acknowledgment restarted ended session: %+v", n)
	}
}

func TestFailureWorkNeverCallsAFailedPushDelivered(t *testing.T) {
	f := newDelegFx(t)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", Runtime: "sandbox", OwnerID: "owner-1", LaunchGeneration: "push-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, f.parent, m); err != nil {
		t.Fatal(err)
	}
	results, _ := json.Marshal([]protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Branch: "wip/" + f.parent, PushError: "delivery failed"}})
	if _, err := f.pool.Exec(t.Context(), `UPDATE session_workspaces SET results=$2 WHERE session_id=$1`, f.parent, results); err != nil {
		t.Fatal(err)
	}
	work, retained := failureWork(t.Context(), f.pool, f.row(f.parent))
	if !retained || strings.Contains(work, "Commits delivered") {
		t.Fatalf("failed push was reported as delivered: %s, retained=%v", work, retained)
	}
}

func TestEscalatedFailureStillReachesACoordinatorThatReturns(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetChildTurnHook(nil)
	child := f.childOf(f.parent)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	for len(f.dc.send) < cap(f.dc.send) {
		f.dc.send <- []byte(`{}`)
	}
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child, ExitCode: 1, Message: "runtime stopped"})
	if err := f.api.escalateSessionFailures(t.Context(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for len(f.dc.send) > 0 {
		<-f.dc.send
	}
	if err := f.api.escalateSessionFailures(t.Context(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := f.parentNote(f.parent, time.Second); n.Source != childTurnSource || !strings.Contains(n.Text, "runtime stopped") {
		t.Fatalf("returning coordinator never heard about the failure: %+v", n)
	}
}
