package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// TestAssistSpawn_ConnectedDaemon verifies the core assist spawn scenario:
// 202 response, board_tokens row minted with board+message caps,
// SpawnSession with Assist=true/BoardID/non-empty BoardToken sent to daemon,
// and the session row written with assist=true and board_id.
func TestAssistSpawn_ConnectedDaemon(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000a01"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "assist-daemon-1", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Alpha", nil, []string{"alpha-repo"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	hub := NewHub()
	dc := &DaemonConn{
		ID:   daemonID,
		Name: "assist-daemon-1",
		send: make(chan []byte, 256),
	}
	hub.Register(dc)

	api := NewAPI(hub, pool, "tok", nil, "")

	req := httptest.NewRequest(http.MethodPost, "/api/boards/"+board.ID+"/assist", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.SetPathValue("id", board.ID)
	rec := httptest.NewRecorder()
	api.HandlePostAssist(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("HandlePostAssist = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatal("session_id missing in response")
	}

	// Read SpawnSession from daemon's send channel.
	select {
	case raw := <-dc.send:
		var spawn protocol.SpawnSession
		if err := json.Unmarshal(raw, &spawn); err != nil {
			t.Fatalf("unmarshal SpawnSession: %v", err)
		}
		if !spawn.Assist {
			t.Errorf("SpawnSession.Assist = false, want true")
		}
		if spawn.BoardID != board.ID {
			t.Errorf("SpawnSession.BoardID = %q, want %q", spawn.BoardID, board.ID)
		}
		if spawn.BoardToken == "" {
			t.Errorf("SpawnSession.BoardToken is empty")
		}
		if spawn.SessionID != resp.SessionID {
			t.Errorf("SpawnSession.SessionID = %q, want %q", spawn.SessionID, resp.SessionID)
		}

		// Validate board token exists and has board+message capabilities.
		tokenRow, err := db.ValidateBoardToken(ctx, pool, spawn.BoardToken)
		if err != nil {
			t.Fatalf("ValidateBoardToken: %v", err)
		}
		var hasBoardCap, hasMessageCap bool
		for _, c := range tokenRow.Capabilities {
			if c == "board" {
				hasBoardCap = true
			}
			if c == "message" {
				hasMessageCap = true
			}
		}
		if !hasBoardCap || !hasMessageCap {
			t.Errorf("token caps = %v, want [board message]", tokenRow.Capabilities)
		}

		// ExpiresAt should be ~24h out (tolerance ±5m).
		wantExpiry := time.Now().Add(24 * time.Hour)
		if delta := tokenRow.ExpiresAt.Sub(wantExpiry); delta < -5*time.Minute || delta > 5*time.Minute {
			t.Errorf("token ExpiresAt = %v, want ~%v (delta %v)", tokenRow.ExpiresAt, wantExpiry, delta)
		}

	default:
		t.Fatal("no message sent to daemon")
	}

	// Check session row has assist=true, board_id set.
	var assistFlag bool
	var boardIDInDB string
	err = pool.QueryRow(ctx, `SELECT assist, board_id FROM sessions WHERE id = $1`, resp.SessionID).Scan(&assistFlag, &boardIDInDB)
	if err != nil {
		t.Fatalf("query session assist/board_id: %v", err)
	}
	if !assistFlag {
		t.Errorf("sessions.assist = false, want true")
	}
	if boardIDInDB != board.ID {
		t.Errorf("sessions.board_id = %q, want %q", boardIDInDB, board.ID)
	}
}

// TestAssistSpawn_TicketScoped verifies that a ticket-scoped spawn binds tickets.session_id.
func TestAssistSpawn_TicketScoped(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000a02"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "assist-daemon-2", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Beta", nil, []string{"beta-repo"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	ticket, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Fix the bug",
		Repos:    []string{"beta-repo"},
		Priority: "medium",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	hub := NewHub()
	dc := &DaemonConn{
		ID:   daemonID,
		Name: "assist-daemon-2",
		send: make(chan []byte, 256),
	}
	hub.Register(dc)

	api := NewAPI(hub, pool, "tok", nil, "")

	body := bytes.NewBufferString(`{"ticket_id":"` + ticket.ID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/boards/"+board.ID+"/assist", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.SetPathValue("id", board.ID)
	rec := httptest.NewRecorder()
	api.HandlePostAssist(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("HandlePostAssist = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatal("session_id missing in response")
	}

	// Verify SpawnSession carries ticket_id.
	select {
	case raw := <-dc.send:
		var spawn protocol.SpawnSession
		if err := json.Unmarshal(raw, &spawn); err != nil {
			t.Fatalf("unmarshal SpawnSession: %v", err)
		}
		if spawn.TicketID != ticket.ID {
			t.Errorf("SpawnSession.TicketID = %q, want %q", spawn.TicketID, ticket.ID)
		}
	default:
		t.Fatal("no message sent to daemon")
	}

	// Verify tickets.session_id is bound.
	ticketDetail, err := db.GetTicket(ctx, pool, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if ticketDetail.SessionID == nil || *ticketDetail.SessionID != resp.SessionID {
		t.Errorf("tickets.session_id = %v, want %q", ticketDetail.SessionID, resp.SessionID)
	}
}

// TestAssistSpawn_NoDaemon verifies that 503 is returned when no daemon is connected.
func TestAssistSpawn_NoDaemon(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Gamma", nil, []string{"gamma-repo"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// Hub with no connected daemons.
	hub := NewHub()
	api := NewAPI(hub, pool, "tok", nil, "")

	req := httptest.NewRequest(http.MethodPost, "/api/boards/"+board.ID+"/assist", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.SetPathValue("id", board.ID)
	rec := httptest.NewRecorder()
	api.HandlePostAssist(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("HandlePostAssist = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAssistSessionEnded_RevokesToken verifies that HandleSessionEnded revokes
// the board token (subsequent ValidateBoardToken returns error) and clears
// tickets.session_id for sessions bound to the ended session.
func TestAssistSessionEnded_RevokesToken(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000a04"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "assist-daemon-4", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Delta", nil, []string{"delta-repo"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	ticket, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Bind me",
		Repos:    []string{"delta-repo"},
		Priority: "medium",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// Pre-create assist session (as HandlePostAssist would do).
	sessionID := "00000000-0000-0000-0000-000000000a05"
	ticketID := ticket.ID
	if err := db.CreateAssistSession(ctx, pool, sessionID, daemonID, board.ID, &ticketID, "delta-repo", "Assist: Delta"); err != nil {
		t.Fatalf("CreateAssistSession: %v", err)
	}

	// Bind ticket to session.
	if err := db.SetTicketSession(ctx, pool, ticket.ID, sessionID); err != nil {
		t.Fatalf("SetTicketSession: %v", err)
	}

	// Mint a board token for the session.
	rawToken, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"board", "message"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken: %v", err)
	}

	// Token should be valid before session ends.
	if _, err := db.ValidateBoardToken(ctx, pool, rawToken); err != nil {
		t.Fatalf("ValidateBoardToken before end: %v (want valid)", err)
	}

	// Simulate session end.
	hub := NewHub()
	HandleSessionEnded(ctx, hub, pool, protocol.SessionEnded{
		Type:      "session_ended",
		SessionID: sessionID,
		ExitCode:  0,
	})

	// Token should now be revoked.
	if _, err := db.ValidateBoardToken(ctx, pool, rawToken); err == nil {
		t.Error("ValidateBoardToken after session_ended: expected error (token revoked), got nil")
	}

	// Ticket should have session_id cleared.
	ticketDetail, err := db.GetTicket(ctx, pool, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket after session_ended: %v", err)
	}
	if ticketDetail.SessionID != nil {
		t.Errorf("tickets.session_id = %q after session_ended, want nil", *ticketDetail.SessionID)
	}
}

// TestAssistSpawn_NoRepos verifies that a board with no repos returns 422.
func TestAssistSpawn_NoRepos(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000a06"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "assist-daemon-6", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	// Create a board with one repo, then remove it so the board has zero repos.
	board, err := db.CreateBoard(ctx, pool, "Epsilon", nil, []string{"epsilon-repo"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM board_repos WHERE board_id = $1`, board.ID); err != nil {
		t.Fatalf("delete board_repos: %v", err)
	}

	hub := NewHub()
	hub.Register(&DaemonConn{ID: daemonID, Name: "assist-daemon-6", send: make(chan []byte, 256)})
	api := NewAPI(hub, pool, "tok", nil, "")

	req := httptest.NewRequest(http.MethodPost, "/api/boards/"+board.ID+"/assist", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.SetPathValue("id", board.ID)
	rec := httptest.NewRecorder()
	api.HandlePostAssist(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("HandlePostAssist = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAssistSpawn_SendFailureRollsBack verifies that when the daemon's send
// channel is full, the handler returns 503 AND rolls back all pre-created state:
// the token no longer validates, the session status is "ended", and the
// ticket binding is cleared.
func TestAssistSpawn_SendFailureRollsBack(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000a07"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "assist-daemon-7", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Zeta", nil, []string{"zeta-repo"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	ticket, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Roll me back",
		Repos:    []string{"zeta-repo"},
		Priority: "medium",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	hub := NewHub()
	// Unbuffered send channel with no reader → the non-blocking select hits default.
	dc := &DaemonConn{ID: daemonID, Name: "assist-daemon-7", send: make(chan []byte)}
	hub.Register(dc)
	api := NewAPI(hub, pool, "tok", nil, "")

	body := bytes.NewBufferString(`{"ticket_id":"` + ticket.ID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/boards/"+board.ID+"/assist", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.SetPathValue("id", board.ID)
	rec := httptest.NewRecorder()
	api.HandlePostAssist(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HandlePostAssist = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}

	// The session ID isn't returned on failure, so look it up by board/ticket binding.
	var sessionID string
	if err := pool.QueryRow(ctx, `SELECT id FROM sessions WHERE board_id = $1`, board.ID).Scan(&sessionID); err != nil {
		t.Fatalf("lookup pre-created session: %v", err)
	}

	// Session status must be "ended".
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, sessionID).Scan(&status); err != nil {
		t.Fatalf("query session status: %v", err)
	}
	if status != "ended" {
		t.Errorf("session status = %q after send failure, want %q", status, "ended")
	}

	// Any minted token must no longer validate (revoked).
	var tokenCount, liveTokenCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_tokens WHERE session_id = $1`, sessionID).Scan(&tokenCount); err != nil {
		t.Fatalf("count board_tokens: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_tokens WHERE session_id = $1 AND revoked_at IS NULL`, sessionID).Scan(&liveTokenCount); err != nil {
		t.Fatalf("count live board_tokens: %v", err)
	}
	if tokenCount == 0 {
		t.Error("expected a board_token row to have been minted")
	}
	if liveTokenCount != 0 {
		t.Errorf("live (non-revoked) board_tokens = %d after send failure, want 0", liveTokenCount)
	}

	// Ticket binding must be cleared.
	ticketDetail, err := db.GetTicket(ctx, pool, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if ticketDetail.SessionID != nil {
		t.Errorf("tickets.session_id = %q after send failure, want nil", *ticketDetail.SessionID)
	}
}
