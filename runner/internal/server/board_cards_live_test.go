package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
)

// liveCardBoard is a board that holds card #7 of board-a, in "Inbox", and answers a move.
type liveCardBoard struct {
	fakeSessionBoard
	mu     sync.Mutex
	column string
	refuse bool
}

func (b *liveCardBoard) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	_, _ = b.fakeSessionBoard.CallBoard(ctx, c)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuse {
		return json.RawMessage(`{"content":[{"type":"text","text":"forbidden: card.write"}],"isError":true}`), nil
	}
	var args map[string]any
	_ = json.Unmarshal(c.Arguments, &args)
	text := ""
	switch c.Tool {
	case "blerg_card_move":
		b.column = args["column_id"].(string)
		text = `{"ok":true}`
	case "blerg_card_get":
		names := map[string]string{"col-inbox": "Inbox", "col-doing": "In progress"}
		card := map[string]any{
			"id": "card-7", "board_id": "board-a", "number": 7, "title": "Fix the flaky test", "type": "bug",
			"priority": "high", "column_id": b.column, "column_name": names[b.column], "tags": []string{"ci"},
			"repos": []string{"example-org/app"}, "origin": map[string]any{"repo": "example-org/app"},
			"columns": []map[string]any{{"id": "col-inbox", "name": "Inbox"}, {"id": "col-doing", "name": "In progress"}},
			"trail":   []map[string]any{{"type": "created", "actor": "agent", "created_at": "2026-10-20T10:00:00Z"}},
		}
		raw, _ := json.Marshal(card)
		text = string(raw)
	}
	raw, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": text}}})
	return raw, nil
}

func newLiveCardAPI(t *testing.T) (*API, *liveCardBoard, func(caps ...string) string) {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"board_url": "https://board.example.test"})
	}))
	t.Cleanup(core.Close)
	api := NewAPI(NewHub(), nil, "fixture-daemon-key", nil, "")
	api.coreURL = core.URL
	board := &liveCardBoard{column: "col-inbox"}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	return api, board, enableBrowserAuth(t, api)
}

func liveCardDo(api *API, h http.HandlerFunc, method, path, board, number, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("board", board)
	r.SetPathValue("number", number)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// The panel reads a pinned board card live, as the signed-in person may: a read-only credential
// exchanged with their own sign-in, blerg_card_get with_trail, and the card's Board page inside
// Blerg. A move goes through with a write credential of theirs, and the card after comes back.
func TestLiveBoardCardIsReadAndMovedWithThePersonsSignIn(t *testing.T) {
	api, board, token := newLiveCardAPI(t)
	tok := token("session.start")
	rec := liveCardDo(api, api.HandleGetLiveBoardCard, http.MethodGet, "/api/board-cards/board-a/7?session=sess-1", "board-a", "7", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("live card = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Card liveBoardCard `json:"card"`
		URL  string        `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Card.ColumnName != "Inbox" || got.Card.Title != "Fix the flaky test" || len(got.Card.Tags) != 1 || len(got.Card.Trail) != 1 || len(got.Card.Columns) != 2 {
		t.Fatalf("card = %+v", got.Card)
	}
	if got.URL != "https://board.example.test/boards/board-a/cards/7" {
		t.Fatalf("url = %q", got.URL)
	}
	call := board.last()
	var args map[string]any
	_ = json.Unmarshal(call.Arguments, &args)
	if call.Tool != "blerg_card_get" || !call.ReadOnly || call.Proof.AccountID != "user-1" || call.Proof.SessionID != testSID ||
		call.SessionRef != "" || args["with_trail"] != true || args["number"] != float64(7) {
		t.Fatalf("board call = %+v %v", call, args)
	}

	rec = liveCardDo(api, api.HandleMoveLiveBoardCard, http.MethodPost, "/api/board-cards/board-a/7/move", "board-a", "7", tok, `{"column_id":"col-doing"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"column_id":"col-doing"`) {
		t.Fatalf("move = %d %s", rec.Code, rec.Body.String())
	}
	// One credential exchange for a move: the move itself, no read after.
	if board.count() != 2 || board.calls[1].Tool != "blerg_card_move" || board.calls[1].ReadOnly || board.column != "col-doing" {
		t.Fatalf("calls = %+v", board.calls)
	}

	// The board's refusal is the person's answer; nothing else is tried.
	board.refuse = true
	if rec := liveCardDo(api, api.HandleMoveLiveBoardCard, http.MethodPost, "/x", "board-a", "7", tok, `{"column_id":"col-inbox"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("refused move = %d %s", rec.Code, rec.Body.String())
	}
}

func TestLiveBoardCardRefusesWithoutAPersonsSignIn(t *testing.T) {
	api, board, token := newLiveCardAPI(t)
	if rec := liveCardDo(api, api.HandleGetLiveBoardCard, http.MethodGet, "/x", "board-a", "7", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	for _, c := range []struct{ board, number string }{{"board a", "7"}, {"board-a", "0"}, {"board-a", "x"}} {
		if rec := liveCardDo(api, api.HandleGetLiveBoardCard, http.MethodGet, "/x", c.board, c.number, token("session.start"), ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%v = %d", c, rec.Code)
		}
	}
	if board.count() != 0 {
		t.Fatal("a refused request reached the board")
	}
}

// A pinned Blerg Board card names its board and number; the runner sets its address (the Board's
// page, inside Blerg), never the agent.
func TestPinnedBoardCardGetsTheRunnersAddress(t *testing.T) {
	tk, err := ticket.Clean(json.RawMessage(`{"source":"blerg-board","board_id":"board-a","key":"7","title":"Fix","url":"https://evil.example/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	board, number, ok := ticket.BoardCard(tk)
	if !ok || board != "board-a" || number != 7 || tk.Key != "#7" {
		t.Fatalf("board card = %q %d %v (key %q)", board, number, ok, tk.Key)
	}
	api, _, _ := newLiveCardAPI(t)
	if got := api.boardCardURL(context.Background(), board, number); got != "https://board.example.test/boards/board-a/cards/7" {
		t.Fatalf("address = %q", got)
	}
	for _, bad := range []string{
		`{"source":"blerg-board","board_id":"board a","key":"#7","title":"x"}`,
		`{"source":"blerg-board","board_id":"board-a","key":"ABC-1","title":"x"}`,
	} {
		if _, err := ticket.Clean(json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// Another tracker's ticket keeps no board id.
	if tk, err := ticket.Clean(json.RawMessage(`{"source":"jira","board_id":"board-a","key":"ABC-1","title":"x"}`)); err != nil || tk.BoardID != "" {
		t.Fatalf("jira ticket = %+v %v", tk, err)
	}
}

// The audit line names the session a card is read from only when the caller can see it.
func TestLiveBoardCardAuditNamesOnlyAVisibleSession(t *testing.T) {
	f := newPrivFixture(t)
	board := &liveCardBoard{column: "col-inbox"}
	f.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	read := func(tok, session string) string {
		t.Helper()
		rec := liveCardDo(f.api, f.api.HandleGetLiveBoardCard, http.MethodGet, "/api/board-cards/board-a/7?session="+session, "board-a", "7", tok, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("read = %d %s", rec.Code, rec.Body.String())
		}
		return board.last().SessionRef
	}
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	other := f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start")
	if got := read(owner, f.privSession); got != f.privSession {
		t.Fatalf("owner's audit ref = %q", got)
	}
	if got := read(other, f.privSession); got != "" {
		t.Fatalf("another account named a private session it cannot see: %q", got)
	}
	if got := read(other, f.pubSession); got != f.pubSession {
		t.Fatalf("a visible shared session was dropped: %q", got)
	}
}
