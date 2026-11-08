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
	if err != nil || len(rows) != 1 || rows[0].Status != "answered" || !strings.Contains(rows[0].Body, "engine stopped") {
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
	if err != nil || len(rows) != 1 || rows[0].Status != "answered" {
		t.Fatalf("duplicate report lost alert: %+v %v", rows, err)
	}
}

func TestChildNoWaitAskStaysWithDisconnectedMain(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	r := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, `{"kind":"ask","no_wait":true,"body":"Which branch?"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("ask: %d %s", r.Code, r.Body.String())
	}
	var posted struct{ ID, Recipient string }
	_ = json.Unmarshal(r.Body.Bytes(), &posted)
	if posted.Recipient != "parent" {
		t.Fatalf("disconnected Main lost its question: %s", r.Body.String())
	}
	if err := f.api.reconcileChildQuestions(t.Context(), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	quiet, escalated := 0, 0
	for raw := range awaitAll(owner.send) {
		var m protocol.MessageCreated
		if json.Unmarshal(raw, &m) == nil && m.Type == "message_created" && m.Message.SessionID == child {
			switch {
			case m.Quiet && !m.Message.Answerable:
				quiet++
			case !m.Quiet && m.Message.Answerable && strings.Contains(m.Message.Body, "parent"):
				escalated++
			default:
				t.Fatalf("question routing: %+v", m)
			}
		}
	}
	if quiet != 1 || escalated != 0 {
		t.Fatalf("initial quiet notices=%d, escalations=%d", quiet, escalated)
	}
	row, err := db.GetMessage(t.Context(), f.pool, posted.ID)
	if err != nil || row == nil || row.Status != "open" || row.ChildEscalated || questionCallsOwner(f.row(child), *row) {
		t.Fatalf("worker question left Main: %+v %v", row, err)
	}
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
			if note := f.parentNote(f.parent, time.Second); note.Source != childTurnSource || !isWorkerNotice(note.Text, child, childSessionFailed) || strings.Contains(note.Text, "Work delivery is unconfirmed") {
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

func TestFailureNoticeShowsRetainedWorkWithoutADismissQuestion(t *testing.T) {
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
	if rec.Code != http.StatusNotFound {
		t.Fatalf("notice exposed a question dismissal: %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetMessage(t.Context(), f.pool, rows[0].ID)
	if err != nil || row.Dismissed {
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

func TestUnattendedRestartPausePushesOnce(t *testing.T) {
	f := newNoticeFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET interaction='unattended' WHERE id=$1`, f.sid); err != nil {
		t.Fatal(err)
	}
	msg := protocol.SessionPaused{SessionID: f.sid, Reason: protocol.PausedByRestart, Detail: "check the interrupted install command"}
	handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, msg)
	if got := f.pushes.Load(); got != 1 {
		t.Fatalf("unattended restart pause pushes=%d, want 1", got)
	}
	handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, msg)
	if got := f.pushes.Load(); got != 1 {
		t.Fatalf("duplicate pause pushes=%d, want 1", got)
	}
}

func TestPrivateFailureAlertNeverPushesSessionDetails(t *testing.T) {
	f := newNoticeFixture(t)
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/non-owner", "p", "a", "other", testSID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET private=true,spawning_account_id='owner',title='Private task' WHERE id=$1`, f.sid); err != nil {
		t.Fatal(err)
	}
	owner := &BrowserConn{ID: "owner-alert", AccountID: "owner", send: make(chan []byte, 32)}
	other := &BrowserConn{ID: "other-alert", AccountID: "other", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	f.hub.RegisterBrowser(other)
	saved := sendWebPush
	sendWebPush = func(raw []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		if sub.Endpoint == "https://push.example.com/non-owner" {
			t.Fatal("private failure pushed to non-owner")
		}
		var p map[string]string
		if json.Unmarshal(raw, &p) != nil || p["body"] != "A workspace needs your attention." || p["url"] != "" || strings.Contains(string(raw), "Private task") || strings.Contains(string(raw), "private reason") {
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
			if m.Message.Answerable || m.Message.Kind != "update" {
				t.Fatal("notice was offered as a question")
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

func TestFailureNoticeCannotBeAnsweredOrStartAnotherTurn(t *testing.T) {
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
	if rec.Code != http.StatusConflict {
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
	if n := f.parentNote(f.parent, time.Second); n.Source != childTurnSource || !isWorkerNotice(n.Text, child, childSessionFailed) || strings.Contains(n.Text, "runtime stopped") {
		t.Fatalf("returning coordinator never heard about the failure: %+v", n)
	}
}

func TestFailureAlertResolvesAfterRecovery(t *testing.T) {
	f := newDelegFx(t)
	id := f.parent
	if err := db.UpdateSessionStatus(t.Context(), f.pool, id, "idle", nil); err != nil {
		t.Fatal(err)
	}
	handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, protocol.SessionPaused{Type: "session_paused", SessionID: id, Reason: protocol.PausedByRestart})
	if err := HandleSessionStateChanged(t.Context(), f.hub, f.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: id, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	assertFailureAlertsResolved(t, f, id, "recovered")
}

func assertFailureAlertsResolved(t *testing.T, f *delegFx, id, reason string) {
	t.Helper()
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, id)
	if err != nil || len(rows) == 0 {
		t.Fatalf("missing alert history: %+v %v", rows, err)
	}
	for _, m := range rows {
		if m.Status != "withdrawn" {
			t.Fatalf("stale alert: %+v", m)
		}
	}
	var n int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'text' LIKE '%'||$2||'%'`, id, reason).Scan(&n); err != nil || n != 1 {
		t.Fatalf("resolution History marks=%d: %v", n, err)
	}
	if err = f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var again int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='mark'`, id).Scan(&again); err != nil || again != n {
		t.Fatalf("duplicate History mark: %d: %v", again, err)
	}
}

func TestFailureAlertDeduplicatesErrorAndStoppedReports(t *testing.T) {
	f := newDelegFx(t)
	id := f.parent
	if err := db.UpdateSessionStatus(t.Context(), f.pool, id, "error", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionError(t.Context(), f.pool, id, "engine stopped"); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, id, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("one failure raised multiple alerts: %+v %v", rows, err)
	}
}

func TestFailureAlertResolvesWhenDiscarded(t *testing.T) {
	f := newDelegFx(t)
	id := f.parent
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, id, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginSessionDiscard(t.Context(), f.pool, id, "owner-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ArchiveDiscardedSession(t.Context(), f.pool, id, "owner-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	assertFailureAlertsResolved(t, f, id, "discarded")
}

func TestFailureAlertReconciliationClosesLegacyDuplicates(t *testing.T) {
	f := newDelegFx(t)
	id := f.parent
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, id, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-fix error:N / ended:N pair already stored by old servers.
	if _, err := f.pool.Exec(t.Context(), `WITH m AS (INSERT INTO messages(session_id,kind,body,status,owner_only,no_wait) VALUES($1,'ask','Duplicate stopped report','open',true,true) RETURNING id) INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id) SELECT $1,'legacy:0',0,id FROM m`, id); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id WHERE f.session_id=$1 AND m.status<>'withdrawn'`, id).Scan(&open); err != nil || open != 1 {
		t.Fatalf("legacy open alerts=%d: %v", open, err)
	}
	var marks int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'text'='Duplicate session failure alert resolved.'`, id).Scan(&marks); err != nil || marks != 1 {
		t.Fatalf("duplicate resolution marks=%d: %v", marks, err)
	}
	var duplicateNotices int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='update' AND payload->>'body'='Duplicate stopped report'`, id).Scan(&duplicateNotices); err != nil || duplicateNotices != 0 {
		t.Fatalf("withdrawn duplicate was delivered again: %d %v", duplicateNotices, err)
	}
}

func TestRecoveredSessionKeepsUnresolvedWorkAlert(t *testing.T) {
	f := newDelegFx(t)
	child := f.childOf(f.parent)
	seedEvent(t, f.pool, child, time.Now(), "error", map[string]string{"code": childSyncErrorCode, "message": "work delivery failed"})
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, child); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil || len(rows) != 1 || rows[0].Status != "answered" {
		t.Fatalf("unresolved work alert was cleared: %+v %v", rows, err)
	}
}

func TestFailureAlertRecoveryKeepsPrivateSessionScope(t *testing.T) {
	f := newDelegFx(t)
	id, _ := f.session(sessOpts{owner: "owner-1", private: true})
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	other := &BrowserConn{ID: "other", AccountID: "owner-2", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	f.hub.RegisterBrowser(other)
	if err := db.UpdateSessionStatus(t.Context(), f.pool, id, "idle", nil); err != nil {
		t.Fatal(err)
	}
	handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, protocol.SessionPaused{Type: "session_paused", SessionID: id, Reason: protocol.PausedByRestart})
	drain(owner.send)
	drain(other.send)
	if err := HandleSessionStateChanged(t.Context(), f.hub, f.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: id, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var resolved bool
	for raw := range awaitAll(owner.send) {
		var m protocol.MessageAnswered
		if json.Unmarshal(raw, &m) == nil && m.Type == "message_answered" && m.Message.SessionID == id {
			resolved = m.Message.Status == "withdrawn"
		}
	}
	if !resolved {
		t.Fatal("owner did not receive the resolved alert")
	}
	if len(other.send) != 0 {
		t.Fatal("private alert recovery reached another account")
	}
}

func TestFailureAlertStartsNewOccurrenceAfterRecovery(t *testing.T) {
	f := newDelegFx(t)
	id := f.parent
	if err := db.UpdateSessionStatus(t.Context(), f.pool, id, "error", nil); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(t.Context(), f.pool, id, "running", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	// A later failure is a different occurrence, even without another user input.
	if err := db.UpdateSessionStatus(t.Context(), f.pool, id, "error", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := recordSessionFailure(t.Context(), f.hub, f.pool, id); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, m := range rows {
		if m.Kind == "update" && m.Status == "answered" {
			open++
		}
	}
	if len(rows) != 1 || open != 1 {
		t.Fatalf("new failure was suppressed or duplicated: %+v", rows)
	}
}

func TestFailureAlertRoleWording(t *testing.T) {
	for _, tc := range []struct {
		name, origin, noun string
	}{
		{"person", "", "Workspace"},
		{"delegated", "session", "Worker"},
		{"agent", "agent", "Worker"},
		{"runner", "runner_key", "Worker"},
		{"scheduled", "cron", "Run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNoticeFixture(t)
			if _, err := f.pool.Exec(t.Context(), "UPDATE sessions SET started_by_kind=$2 WHERE id=$1", f.sid, tc.origin); err != nil {
				t.Fatal(err)
			}
			saved := sendWebPush
			sendWebPush = func(raw []byte, sub *webpush.Subscription, options *webpush.Options) (*http.Response, error) {
				var payload map[string]string
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["title"] != tc.noun+" needs you" || !strings.HasPrefix(payload["body"], tc.noun+" ") {
					t.Errorf("push wording: %s", raw)
				}
				return saved(raw, sub, options)
			}
			t.Cleanup(func() { sendWebPush = saved })
			HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sid, ExitCode: 1, Message: "runtime stopped"})
			if f.pushes.Load() != 1 {
				t.Fatal("failure alert push missing")
			}
			rows, err := db.ListMessagesBySession(t.Context(), f.pool, f.sid)
			if err != nil || len(rows) != 1 {
				t.Fatalf("Needs you: %+v %v", rows, err)
			}
			if !strings.HasPrefix(rows[0].Body, tc.noun+" ") || strings.Contains(strings.ToLower(rows[0].Body), "session") {
				t.Fatalf("Needs you wording: %s", rows[0].Body)
			}
		})
	}
}

func TestPersonWorkNounUsesOriginAndParent(t *testing.T) {
	parent, cron := "parent", "schedule"
	for _, tc := range []struct {
		row  db.SessionRow
		want string
	}{
		{db.SessionRow{}, "Workspace"},
		{db.SessionRow{ParentSessionID: &parent}, "Channel"},
		{db.SessionRow{StartedByKind: "session", ParentSessionID: &parent}, "Worker"},
		{db.SessionRow{CronID: &cron, StartedByKind: "session"}, "Run"},
	} {
		if got := personWorkNoun(&tc.row); got != tc.want {
			t.Errorf("role = %q, want %q", got, tc.want)
		}
	}
}

func TestFailureAlertDismissRecoverFailSameTurn(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetChildTurnHook(nil)
	if err := db.UpdateSessionStatus(t.Context(), f.pool, f.parent, "idle", nil); err != nil {
		t.Fatal(err)
	}
	handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, protocol.SessionPaused{Type: "session_paused", SessionID: f.parent, Reason: protocol.PausedByRestart})
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", rows[0].ID, f.human("owner-1"), `{"answer":"Checked"}`)
	if rec.Code != http.StatusConflict {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if err = HandleSessionStateChanged(t.Context(), f.hub, f.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: f.parent, Status: "running"}); err != nil {
		t.Fatal(err)
	}

	if err = db.UpdateSessionStatus(t.Context(), f.pool, f.parent, "error", nil); err != nil {
		t.Fatal(err)
	}
	if err = recordSessionFailure(t.Context(), f.hub, f.pool, f.parent); err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "answered" {
		t.Fatalf("later failure suppressed: %+v", rows)
	}
}

func TestFailureAlertFlappingReusesWithdrawnAlert(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetChildTurnHook(nil)
	for range 5 {
		if err := db.UpdateSessionStatus(t.Context(), f.pool, f.parent, "error", nil); err != nil {
			t.Fatal(err)
		}
		if err := recordSessionFailure(t.Context(), f.hub, f.pool, f.parent); err != nil {
			t.Fatal(err)
		}
		rows, err := db.ListMessagesBySession(t.Context(), f.pool, f.parent)
		if err != nil || len(rows) != 1 || rows[0].Status != "answered" {
			t.Fatalf("flapping multiplied alerts: %+v %v", rows, err)
		}
		if err := db.UpdateSessionStatus(t.Context(), f.pool, f.parent, "running", nil); err != nil {
			t.Fatal(err)
		}
		if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFailureAlertNewInputIsNotRecovery(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetChildTurnHook(nil)
	if err := db.UpdateSessionStatus(t.Context(), f.pool, f.parent, "error", nil); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(t.Context(), f.hub, f.pool, f.parent); err != nil {
		t.Fatal(err)
	}
	// The failure update already owns an event sequence. Append like production
	// instead of relying on the process-global seed counter and test order.
	if _, _, err := db.AppendAgentEventAt(t.Context(), f.pool, f.parent, newUUID(), "user_message", `{"text":"Continue"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil || len(rows) != 1 || rows[0].Status != "answered" {
		t.Fatalf("failed session called recovered: %+v %v", rows, err)
	}
}

func TestFailureAlertFlappingSendsOnePush(t *testing.T) {
	f := newNoticeFixture(t)
	for range 5 {
		if err := db.UpdateSessionStatus(t.Context(), f.pool, f.sid, "error", nil); err != nil {
			t.Fatal(err)
		}
		if err := recordSessionFailure(t.Context(), f.hub, f.pool, f.sid); err != nil {
			t.Fatal(err)
		}
		if err := db.UpdateSessionStatus(t.Context(), f.pool, f.sid, "running", nil); err != nil {
			t.Fatal(err)
		}
		if err := resolveSessionFailureAlerts(t.Context(), f.hub, f.pool, f.sid); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.pushes.Load(); got != 1 {
		t.Fatalf("flapping sent %d pushes, want 1", got)
	}
}

func TestFailureNoticeReachesConversationAndNeverNeedsAnAnswer(t *testing.T) {
	f := newDelegFx(t)
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.parent, ExitCode: 1, Message: "engine stopped"})
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	messages, err := db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil || len(messages) != 1 {
		t.Fatalf("notices: %+v %v", messages, err)
	}
	m := messages[0]
	if m.Kind != "update" || m.Status != "answered" || m.Answer != nil || m.OwnerOnly || questionCallsOwner(f.row(f.parent), m) {
		t.Fatalf("notice needs an answer: %+v", m)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='update' AND payload->>'message_id'=$2`, f.parent, m.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conversation notices=%d: %v", count, err)
	}
	asks, err := db.ListOpenAsks(t.Context(), f.pool, f.parent)
	if err != nil || len(asks) != 0 {
		t.Fatalf("notice became a question: %+v %v", asks, err)
	}
}

// A cluster runtime publishes its initial pending recovery location before clone.
// Pending work in a live checkout is normal, not a failed worker.
func TestHealthyClusterPendingWorkDoesNotNotifyFailure(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, runtime: "cluster"})
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "cluster", OwnerID: "owner-1", LaunchGeneration: "healthy-launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, child, m); err != nil {
		t.Fatal(err)
	}
	results, _ := json.Marshal([]protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: m.LaunchGeneration, MemberID: "one"}, Branch: "wip/fixture", Pending: true, RecoveryLocation: "projects/one"}})
	if _, err := f.pool.Exec(t.Context(), `UPDATE session_workspaces SET results=$2 WHERE session_id=$1`, child, results); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"starting", "running", "idle"} {
		if err := db.UpdateSessionStatusGuarded(t.Context(), f.pool, child, status, nil); err != nil {
			t.Fatal(err)
		}
		row := f.row(child)
		work, failed := failureWork(t.Context(), f.pool, row)
		if failed || !strings.Contains(work, "pending") {
			t.Fatalf("healthy %s work misclassified: %s", status, work)
		}
		if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_failure_alerts WHERE session_id=$1`, child).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("healthy %s worker produced a failure alert and kind=failed notice", status)
		}
	}
	for raw := range awaitAll(f.dc.send) {
		if strings.Contains(string(raw), "kind=failed") {
			t.Fatal("healthy launch notified parent of failure")
		}
	}
	// An explicit delivery error while the engine is still alive is a real failure.
	if _, err := f.pool.Exec(t.Context(), `UPDATE session_workspaces SET results=jsonb_set(results,'{0,push_error}','"delivery failed"') WHERE session_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_failure_alerts WHERE session_id=$1 AND recovered_at IS NULL`, child).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("live worker delivery error was suppressed")
	}
	note := f.parentNote(f.parent, time.Second)
	if !strings.Contains(note.Text, "kind=failed") {
		t.Fatalf("real failure did not notify parent: %+v", note)
	}
}
