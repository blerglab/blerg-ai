package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/gorilla/websocket"
)

// liveSubscribe opens a board's change stream the way the browser does and
// waits until the hub has registered it, so a write made after this returns
// must reach the socket.
func liveSubscribe(t *testing.T, srv *httptest.Server, boardID, token string) *websocket.Conn {
	t.Helper()
	before := api.HubSubscriberCountForTest(srvAPI.Hub, boardID)
	conn := dialWS(t, srv, boardID, token)
	deadline := time.Now().Add(15 * time.Second)
	for api.HubSubscriberCountForTest(srvAPI.Hub, boardID) <= before {
		if time.Now().After(deadline) {
			t.Fatal("websocket subscription did not register")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return conn
}

// expectPing reads the next change ping, failing if none arrives within a
// second: "live" means the open board refreshes right away, not on a reload.
func expectPing(t *testing.T, conn *websocket.Conn, what, want string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("%s: no live change ping within 1s: %v", what, err)
	}
	if !strings.Contains(string(msg), want) {
		t.Fatalf("%s: ping %s, want %s", what, msg, want)
	}
}

func okJSON(t *testing.T, resp *http.Response, what string, out any) {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s: HTTP %d: %s", what, resp.StatusCode, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("%s: decode: %v (%s)", what, err, body)
		}
	}
}

// TestCardWritesThroughAPIPingOpenBoards is the 0.4.3 "board doesn't update
// live" regression: an open board must hear about every card change, whatever
// wrote it — the REST API a script or agent calls, the MCP tools, or the UI.
func TestCardWritesThroughAPIPingOpenBoards(t *testing.T) {
	restore := api.SetKeepaliveIntervals(time.Hour, time.Hour)
	defer restore()

	srv, pool := testServer(t)
	ctx := context.Background()
	f := false
	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "live", RequireRepo: &f})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil || len(cols) < 2 {
		t.Fatalf("columns: %v (%d)", err, len(cols))
	}
	_, agent, err := db.MintToken(ctx, pool, &board.ID, "agent", "live agent",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	human := humanToken(t, srv)
	conn := liveSubscribe(t, srv, board.ID, human)

	// REST, as a script or agent with a bearer token does it.
	var card db.Card
	okJSON(t, request(t, srv, "POST", "/api/boards/"+board.ID+"/cards", agent, nil,
		map[string]any{"title": "made by a script"}), "REST create", &card)
	expectPing(t, conn, "REST create", "card_changed")

	okJSON(t, request(t, srv, "PATCH", "/api/cards/"+card.ID, agent, nil,
		map[string]any{"title": "renamed by a script"}), "REST update", nil)
	expectPing(t, conn, "REST update", "card_changed")

	okJSON(t, request(t, srv, "POST", "/api/cards/"+card.ID+"/move", agent, nil,
		map[string]any{"column_id": cols[1].ID}), "REST move", nil)
	expectPing(t, conn, "REST move", "card_changed")

	okJSON(t, request(t, srv, "POST", "/api/cards/"+card.ID+"/archive", agent, nil, nil),
		"REST archive", nil)
	expectPing(t, conn, "REST archive", "card_changed")

	// MCP, as an agent session does it.
	mcp := func(tool string, args map[string]any) {
		t.Helper()
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		okJSON(t, request(t, srv, "POST", "/mcp", agent, nil, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": args},
		}), "MCP "+tool, &out)
		if out.Result.IsError {
			t.Fatalf("MCP %s errored", tool)
		}
	}
	mcp("blerg_card_create", map[string]any{"board_id": board.ID, "title": "made by an agent"})
	expectPing(t, conn, "MCP create", "card_changed")
}

// Running a card moves it to the work column before the session starts. When
// the start then fails, the move has still happened: the open board must hear
// about it, not keep showing the card where it was.
func TestRunCardMovePingsOpenBoardsEvenWhenStartFails(t *testing.T) {
	restore := api.SetKeepaliveIntervals(time.Hour, time.Hour)
	defer restore()

	srv, pool := testServer(t)
	ctx := context.Background()
	human := humanToken(t, srv)
	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("no connected daemon")}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "live-run", Repos: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("run me"), Repos: &[]string{"app"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	conn := liveSubscribe(t, srv, board.ID, human)

	resp := request(t, srv, "POST", "/api/cards/"+res.Card.ID+"/spawn", human, nil, map[string]string{})
	_ = resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatalf("spawn with a failing runner: HTTP %d, want an error", resp.StatusCode)
	}
	moved, _ := db.GetCard(ctx, pool, res.Card.ID)
	if moved.ColumnID == nil || res.Card.ColumnID != nil && *moved.ColumnID == *res.Card.ColumnID {
		t.Fatal("precondition: running the card should have moved it to the work column")
	}
	expectPing(t, conn, "run-card move", "card_changed")
}
