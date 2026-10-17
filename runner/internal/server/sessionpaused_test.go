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
// paused too, shown once it is idle. A report for another daemon's session changes nothing.
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
	other := &DaemonConn{ID: otherID, send: make(chan []byte, 8)}
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
		handleSessionPaused(ctx, hub, pool, c, protocol.SessionPaused{Type: "session_paused", SessionID: id, Reason: protocol.PausedByRestart})
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
	// The row still reads "running" from before the restart: paused all the same, shown once idle.
	report(dc, runningID)
	if got := paused(runningID); got != protocol.PausedByRestart {
		t.Fatalf("a running row was not paused: %q", got)
	}
	if row, _ := db.GetSession(ctx, pool, runningID); sessionRowToInfo(*row, "").PausedReason != "" {
		t.Error("a running session is shown paused")
	}
	if err := db.UpdateSessionStatus(ctx, pool, runningID, "idle", nil); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetSession(ctx, pool, runningID); sessionRowToInfo(*row, "").PausedReason != protocol.PausedByRestart {
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
	hub.Register(dc2)
	hub.deliverHeld(dc2)
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
	hub.Register(full)
	if _, err := db.SetSessionPaused(ctx, pool, sessionID, protocol.PausedByRestart); err != nil {
		t.Fatal(err)
	}
	handleBrowserAgentMessage(ctx, hub, browser, pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: sessionID, Text: "Continue the task you were working on.", Source: "chat"})
	if row, _ := db.GetSession(ctx, pool, sessionID); row.PausedReason != protocol.PausedByRestart || unpaused() != 0 {
		t.Errorf("a message that never left ended the pause: %q", row.PausedReason)
	}
}
