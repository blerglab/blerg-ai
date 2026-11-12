package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A daemon's session_paused sets the row's paused reason for its own session, closes the session's
// asks (nobody waits for their answers any more) and tells the browsers; the sessions list carries
// the reason while the session waits; the session's next run clears it, and so do an end and a
// revive. A row still "running" from before the restart (what the report usually meets) is
// paused immediately, including in its persisted status. A report for another daemon's session changes nothing.
// The open asks of a daemon's sessions are sent to it after its hello. Requires TEST_DATABASE_URL.
func TestSessionPausedByARestart(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	daemonID := "00000000-0000-0000-0000-0000000000c1"
	otherID := "00000000-0000-0000-0000-0000000000c9"
	sessionID := "00000000-0000-0000-0000-0000000000c2"
	runningID := "00000000-0000-0000-0000-0000000000c3"
	for _, d := range []string{daemonID, otherID} {
		if err := db.UpsertDaemon(ctx, pool, d, "laptop-"+d[len(d)-2:], "daemon", ""); err != nil {
			t.Fatalf("UpsertDaemon: %v", err)
		}
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle", "/w/proj", "proj", "T", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := db.InsertSession(ctx, pool, runningID, daemonID, "running", "/w/proj", "proj", "T", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	askID, err := db.CreateMessage(ctx, pool, sessionID, "ask", "Which database?")
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	noteID, err := db.CreateMessage(ctx, pool, sessionID, "note", "FYI")
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	hub := NewHub()
	browser := &BrowserConn{ID: "b1", send: make(chan []byte, 32)}
	hub.RegisterBrowser(browser)
	dc := &DaemonConn{ID: daemonID, send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetWorkBranches(true)  // current daemon: immutable recorded branch names
	other := &DaemonConn{ID: otherID, send: make(chan []byte, 8)}
	other.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	other.SetWorkBranches(true)  // current daemon: immutable recorded branch names
	told := func(kind string) []string {
		var out []string
		for {
			select {
			case raw := <-browser.send:
				if strings.Contains(string(raw), `"`+kind+`"`) {
					out = append(out, string(raw))
				}
			default:
				return out
			}
		}
	}
	paused := func(id string) string {
		t.Helper()
		row, err := db.GetSession(ctx, pool, id)
		if err != nil || row == nil {
			t.Fatalf("GetSession: %v", err)
		}
		return row.PausedReason
	}
	report := func(c *DaemonConn, id string) {
		raw, err := json.Marshal(protocol.SessionPaused{Type: "session_paused", SessionID: id, Reason: protocol.PausedByRestart, Detail: "Workspace stop interrupted; work retained."})
		if err != nil {
			t.Fatal(err)
		}
		if !handleDaemonFrame(ctx, hub, c, pool, raw, envelope{Type: "session_paused", SessionID: id}) {
			t.Fatal("pause frame refused")
		}
	}

	// After the hello: the daemon hears which of its sessions have a question open.
	sendOpenAsks(ctx, pool, dc, []string{sessionID, runningID})
	var asks protocol.OpenAsks
	if err := json.Unmarshal(<-dc.send, &asks); err != nil || asks.Type != "open_asks" || len(asks.SessionIDs) != 1 || asks.SessionIDs[0] != sessionID {
		t.Fatalf("open_asks: %+v (%v)", asks, err)
	}

	report(other, sessionID)
	if paused(sessionID) != "" {
		t.Fatal("a report from another daemon paused it")
	}
	// The restart report replaces stale running state immediately.
	report(dc, runningID)
	if got := paused(runningID); got != protocol.PausedByRestart {
		t.Fatalf("a running row was not paused: %q", got)
	}
	if row, _ := db.GetSession(ctx, pool, runningID); row.Status != "waiting" || sessionRowToInfo(*row, "").PausedReason != protocol.PausedByRestart || sessionRowToInfo(*row, "").PausedDetail != "Workspace stop interrupted; work retained." {
		t.Error("restart pause did not replace stale running state")
	}
	if err := db.UpdateSessionStatus(ctx, pool, runningID, "idle", nil); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetSession(ctx, pool, runningID); sessionRowToInfo(*row, "").PausedReason != protocol.PausedByRestart || sessionRowToInfo(*row, "").PausedDetail != "Workspace stop interrupted; work retained." {
		t.Error("idle after the report, it is not shown paused")
	}
	_ = told("session_paused_changed")
	report(dc, sessionID)
	if got := paused(sessionID); got != protocol.PausedByRestart {
		t.Fatalf("paused_reason = %q", got)
	}
	if got := told("session_paused_changed"); len(got) != 1 || !strings.Contains(got[0], `"paused_reason":"restart"`) {
		t.Errorf("browsers were told %v", got)
	}
	if m, _ := db.GetMessage(ctx, pool, askID); m == nil || m.Status != "answered" {
		t.Errorf("the dead ask stays open: %+v", m)
	}
	if m, _ := db.GetMessage(ctx, pool, noteID); m == nil || m.Status != "open" {
		t.Errorf("a note was closed: %+v", m)
	}
	row, _ := db.GetSession(ctx, pool, sessionID)
	if info := sessionRowToInfo(*row, ""); info.PausedReason != protocol.PausedByRestart {
		t.Errorf("the sessions list does not carry it: %+v", info)
	}
	report(dc, sessionID)
	if got := told("session_paused_changed"); len(got) != 0 {
		t.Errorf("a repeated report was broadcast again: %v", got)
	}

	// The session runs again: no longer paused.
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	if paused(sessionID) != "" {
		t.Error("a running session is still paused")
	}
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "idle", nil); err != nil {
		t.Fatal(err)
	}
	if paused(sessionID) != "" {
		t.Error("idle again after running, it reads as paused")
	}
	// An end clears it, and a revive.
	report(dc, sessionID)
	if err := db.EndSessionStatus(ctx, pool, sessionID, "stopped", nil, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
		t.Fatal(err)
	}
	if paused(sessionID) != "" {
		t.Error("an ended session is still paused")
	}
	if err := db.InsertSession(ctx, pool, runningID, daemonID, "starting", "/w/proj", "proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if paused(runningID) != "" {
		t.Error("a revived session is still paused")
	}
}

// A message for a session whose daemon is away is held, not taken as the answer to its open ask
// (that `ask` died with the restart), and a message accepted for a paused session ends the pause
// for every browser at once, so a second tab cannot resume it twice. Requires TEST_DATABASE_URL.
func TestMessageForAPausedSessionIsHeldAndEndsThePause(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	withGrace(t, time.Minute)
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	hub.SetSessionOwner(sessionID, dc.ID)
	askID, err := db.CreateMessage(ctx, pool, sessionID, "ask", "Which database?")
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(ctx, hub, pool, sessionID, messageEventID(askID, "ask"), "ask", messagingPayload{Body: "Which database?", MessageID: askID})
	browser := &BrowserConn{ID: "b1", send: make(chan []byte, 32)}
	hub.RegisterBrowser(browser)
	unpaused := func() int {
		n := 0
		for {
			select {
			case raw := <-browser.send:
				if strings.Contains(string(raw), `"session_paused_changed"`) && !strings.Contains(string(raw), `"paused_reason":"restart"`) {
					n++
				}
			default:
				return n
			}
		}
	}
	askOpen := func() bool {
		m, _ := db.GetMessage(ctx, pool, askID)
		return m != nil && m.Status == "open"
	}

	// The daemon restarts: the message typed meanwhile is held, the dead ask is left alone.
	handleDaemonDisconnect(hub, dc, pool)
	if _, err := db.SetSessionPaused(ctx, pool, sessionID, protocol.PausedByRestart); err != nil {
		t.Fatal(err)
	}
	handleBrowserAgentMessage(ctx, hub, browser, pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: sessionID, Text: "Postgres", Source: "chat"})
	if !askOpen() {
		t.Fatal("a message typed while the daemon was away answered its dead ask")
	}
	if _, apiErr := api.SendMessage(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID, "Postgres", "human"); apiErr == nil || !askOpen() {
		t.Fatalf("the API answered the dead ask while the daemon was away: %+v", apiErr)
	}
	row, _ := db.GetSession(ctx, pool, sessionID)
	if row.PausedReason != "" || unpaused() != 1 {
		t.Errorf("the held message did not end the pause for every browser: %q", row.PausedReason)
	}
	dc2 := &DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte, 8)}
	dc2.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc2.SetWorkBranches(true)  // current daemon: immutable recorded branch names
	hub.Register(dc2)
	hub.deliverHeld(t.Context(), dc2)
	select {
	case got := <-dc2.send:
		if !strings.Contains(string(got), "Postgres") {
			t.Errorf("delivered %s", got)
		}
	default:
		t.Fatal("the held message was not handed to the returning daemon")
	}

	// Paused again (the ask closed by the pause): Resume all's message, through the API as through
	// the browser, is delivered and ends the pause; a second one finds nothing to end.
	if _, err := db.CloseOpenAsksForSession(ctx, pool, sessionID); err != nil {
		t.Fatal(err)
	}
	for _, send := range []func(){
		func() {
			if _, apiErr := api.SendMessage(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID, "Continue the task you were working on.", "human"); apiErr != nil {
				t.Fatal(apiErr)
			}
		},
		func() {
			handleBrowserAgentMessage(ctx, hub, browser, pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: sessionID, Text: "Continue the task you were working on.", Source: "chat"})
		},
	} {
		if _, err := db.SetSessionPaused(ctx, pool, sessionID, protocol.PausedByRestart); err != nil {
			t.Fatal(err)
		}
		send()
		if row, _ := db.GetSession(ctx, pool, sessionID); row.PausedReason != "" {
			t.Error("an accepted message left the session paused")
		}
		if n := unpaused(); n != 1 {
			t.Errorf("browsers told %d times", n)
		}
		select {
		case <-dc2.send:
		default:
			t.Error("the message did not reach the daemon")
		}
		send()
		if n := unpaused(); n != 0 {
			t.Errorf("a second message broadcast again (%d)", n)
		}
		<-dc2.send
	}

	// A message the daemon's full queue refuses was not sent: the session stays paused.
	full := &DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte)}
	full.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	full.SetWorkBranches(true)  // current daemon: immutable recorded branch names
	hub.Register(full)
	if _, err := db.SetSessionPaused(ctx, pool, sessionID, protocol.PausedByRestart); err != nil {
		t.Fatal(err)
	}
	handleBrowserAgentMessage(ctx, hub, browser, pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: sessionID, Text: "Continue the task you were working on.", Source: "chat"})
	if row, _ := db.GetSession(ctx, pool, sessionID); row.PausedReason != protocol.PausedByRestart || unpaused() != 0 {
		t.Errorf("a message that never left ended the pause: %q", row.PausedReason)
	}
}

// Accepting input retries adoption; it does not establish that recovery succeeded.
func TestUnrecoveredMessageKeepsFailureAlertOpen(t *testing.T) {
	f := newEndFixture(t, "running", 8)
	f.own()
	ctx := t.Context()
	if _, err := db.SetSessionPausedWithDetail(ctx, f.pool, f.sessionID, protocol.PausedByRestart, "Recovery paused: Workspace stop is unfinished. Send a message to retry."); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionFailure(ctx, f.hub, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}
	clearPausedOnMessage(ctx, f.hub, f.pool, f.sessionID)
	if err := resolveSessionFailureAlerts(ctx, f.hub, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if row := f.row(t); row.PausedReason != protocol.PausedByRestart {
		t.Fatalf("accepted input falsely cleared recovery pause: %+v", row)
	}
	var resolved int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'text'='Session recovered; its failure alert was resolved.'`, f.sessionID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 0 {
		t.Fatalf("unrecovered session produced %d recovery marks", resolved)
	}
	// Only a runtime acknowledgement can turn the retry into a recovery.
	if err := HandleSessionStateChanged(ctx, f.hub, f.pool, protocol.SessionStateChanged{SessionID: f.sessionID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if row := f.row(t); row.PausedReason != "" {
		t.Fatal("confirmed recovery stayed paused")
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'text'='Session recovered; its failure alert was resolved.'`, f.sessionID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("confirmed recovery produced %d recovery marks, want one", resolved)
	}

}

func TestPausedEndWithoutWorkspaceKeepsWorkRetained(t *testing.T) {
	f := newEndFixture(t, "running", 8)
	f.own()
	if _, err := db.SetSessionPausedWithDetail(t.Context(), f.pool, f.sessionID, protocol.PausedByRestart, "Recovery paused: Workspace stop is unfinished."); err != nil {
		t.Fatal(err)
	}
	if code := f.delete(t); code != 409 {
		t.Fatalf("End claimed success without cleanup evidence: %d", code)
	}
	if row := f.row(t); row.PausedReason == "" || row.EndedAt != nil {
		t.Fatalf("End lost the paused workspace: %+v", row)
	}
	select {
	case <-f.dc.send:
		t.Fatal("End dispatched an unverified kill")
	default:
	}
}
