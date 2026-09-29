package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// TestBoardCLIIntegration is a DB-gated end-to-end test. It:
//  1. Boots the board API over httptest.
//  2. Seeds a board + board token.
//  3. Runs the real Python blerg-runner CLI as subprocesses pointed at the test
//     server, exercising create → add-tag → move → archive.
//  4. Asserts final DB state (priority, tag, archived_at, column_id).
//  5. Asserts a subscribed WS client received ticket_created, ticket_updated,
//     ticket_moved, and ticket_archived board events.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestBoardCLIIntegration(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// ── Seed board ────────────────────────────────────────────────────────────

	const repo = "cli-int-repo"
	board, err := db.CreateBoard(ctx, pool, "CLI Integration Board", nil, []string{repo}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// ListColumns returns [Backlog, Doing, Done] ordered by rank.
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(cols) < 3 {
		t.Fatalf("expected 3 seeded columns (Backlog/Doing/Done), got %d", len(cols))
	}
	doneCol := cols[2]
	if !doneCol.IsTerminal {
		t.Fatalf("cols[2] (%q) is not terminal; board seeding unexpected", doneCol.Name)
	}

	// ── Mint board token ──────────────────────────────────────────────────────

	const (
		daemonID  = "00000000-0000-0000-0000-00000000d701"
		sessionID = "00000000-0000-0000-0000-00000000d702"
	)
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cli-int-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running",
		"/repos/app", repo, "CLI Integration", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	rawToken, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID,
		[]string{"board", "message"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken: %v", err)
	}

	// ── Boot in-test API server ───────────────────────────────────────────────

	hub := NewHub()
	api := NewAPI(hub, pool, "daemon-tok", nil, "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(pool, allowWSForTest))
	mux.HandleFunc("GET /api/boards/{id}", api.HandleGetBoard)
	mux.HandleFunc("GET /api/boards/{id}/tickets", api.HandleListTickets)
	mux.HandleFunc("POST /api/boards/{id}/tickets", api.HandlePostTicket)
	mux.HandleFunc("GET /api/tickets/{id}", api.HandleGetTicket)
	mux.HandleFunc("PATCH /api/tickets/{id}", api.HandlePatchTicket)
	mux.HandleFunc("POST /api/tickets/{id}/archive", api.HandleArchiveTicket)
	mux.HandleFunc("PATCH /api/columns/{id}", api.HandlePatchColumn)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ── Subscribe a WS browser client to the board ────────────────────────────

	wsConn := dialBrowserTestServer(t, srv)
	defer wsConn.Close()

	// Drain initial_state.
	_ = readMessageWithDeadline(t, wsConn)

	// Send subscribe_board so we receive events for this board.
	subMsg, _ := json.Marshal(protocol.SubscribeBoard{
		Type:    "subscribe_board",
		BoardID: board.ID,
	})
	if err := wsConn.WriteMessage(websocket.TextMessage, subMsg); err != nil {
		t.Fatalf("write subscribe_board: %v", err)
	}
	// Let the read-pump goroutine process the subscribe message.
	time.Sleep(150 * time.Millisecond)

	// Collect board events in the background until the connection is closed.
	eventCh := make(chan string, 64)
	wsDone := make(chan struct{})
	go func() {
		defer close(wsDone)
		for {
			_, raw, err := wsConn.ReadMessage()
			if err != nil {
				return // connection closed or server error
			}
			var env struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &env) == nil && env.Type != "" {
				select {
				case eventCh <- env.Type:
				default: // buffer full, drop
				}
			}
		}
	}()

	// ── CLI subprocess helpers ────────────────────────────────────────────────

	// The test binary's working directory is the package directory
	// (internal/server/). The script lives at
	// <repo-root>/.claude/skills/session-messaging/blerg-runner — two levels up.
	scriptPath := filepath.Join("..", "..", ".claude", "skills", "session-messaging", "blerg-runner")

	cliEnv := append(os.Environ(),
		"BLERG_RUNNER_SERVER_HTTP="+srv.URL,
		"BLERG_RUNNER_BOARD_ID="+board.ID,
		"BLERG_RUNNER_BOARD_TOKEN="+rawToken,
	)

	// runCLI runs the blerg-runner script with the given args and returns trimmed stdout.
	// Fatalf on non-zero exit.
	runCLI := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("python3", append([]string{scriptPath}, args...)...)
		cmd.Env = cliEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("blerg-runner %v: %v\noutput:\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// ── 1. ticket create ──────────────────────────────────────────────────────

	createOut := runCLI("ticket", "create", board.ID,
		"--title", "Wire logo cleanup",
		"--repos", repo,
		"--priority", "high",
	)

	// Output format: "Ticket created: Wire logo cleanup  (<uuid>)"
	uuidRE := regexp.MustCompile(
		`\(([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)`,
	)
	m := uuidRE.FindStringSubmatch(createOut)
	if len(m) < 2 {
		t.Fatalf("could not parse ticket ID from create output: %q", createOut)
	}
	ticketID := m[1]
	t.Logf("created ticket %s", ticketID)

	// ── 2. ticket update — add tag 'logo' ─────────────────────────────────────

	runCLI("ticket", "update", ticketID, "--add-tag", "logo")

	// ── 3. ticket move → Done ─────────────────────────────────────────────────

	runCLI("ticket", "move", ticketID, "--column", "Done")

	// Verify the move landed in the terminal Done column BEFORE archiving (which
	// clears column_id). Without this, a move to the wrong column would slip by.
	movedTicket, err := db.GetTicket(ctx, pool, ticketID)
	if err != nil {
		t.Fatalf("GetTicket after move: %v", err)
	}
	if movedTicket.ColumnID == nil {
		t.Fatalf("column_id is nil after move; want Done column %s", doneCol.ID)
	}
	if *movedTicket.ColumnID != doneCol.ID {
		t.Errorf("column_id = %q after move, want Done column %q", *movedTicket.ColumnID, doneCol.ID)
	}

	// ── 4. ticket archive ─────────────────────────────────────────────────────

	runCLI("ticket", "archive", ticketID)

	// Allow lagging WS frames to arrive before closing the connection.
	time.Sleep(300 * time.Millisecond)
	wsConn.Close()
	<-wsDone

	// Drain the event channel.
	close(eventCh)
	var collectedEvents []string
	for e := range eventCh {
		collectedEvents = append(collectedEvents, e)
	}

	// ── DB assertions ─────────────────────────────────────────────────────────

	ticket, err := db.GetTicket(ctx, pool, ticketID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}

	// Priority set at create.
	if ticket.Priority != "high" {
		t.Errorf("priority = %q, want 'high'", ticket.Priority)
	}

	// Tag added via update.
	var hasLogo bool
	for _, tag := range ticket.Tags {
		if tag == "logo" {
			hasLogo = true
			break
		}
	}
	if !hasLogo {
		t.Errorf("expected tag 'logo', got %v", ticket.Tags)
	}

	// Archive clears column_id and sets archived_at.
	if ticket.ArchivedAt == nil {
		t.Error("archived_at should be set after archive")
	}
	if ticket.ColumnID != nil {
		t.Errorf("column_id should be nil after archive, got %q", *ticket.ColumnID)
	}

	// ── WS event assertions ───────────────────────────────────────────────────

	evtSet := make(map[string]bool, len(collectedEvents))
	for _, e := range collectedEvents {
		evtSet[e] = true
	}
	t.Logf("received WS event types: %v", collectedEvents)

	for _, want := range []string{
		"ticket_created",
		"ticket_updated",
		"ticket_moved",
		"ticket_archived",
	} {
		if !evtSet[want] {
			t.Errorf("WS: expected event %q not received; all received: %v", want, collectedEvents)
		}
	}
}
